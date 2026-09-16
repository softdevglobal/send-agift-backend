package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrProductReviewNotFound      = errors.New("product review not found")
	ErrProductReviewDuplicate     = errors.New("product review already exists for this order item")
	ErrProductReviewVoteNotFound  = errors.New("product review vote not found")
)

// ProductReviewRepository persists marketplace.product_reviews (+ media + votes).
type ProductReviewRepository struct {
	db *pgxpool.Pool
}

func NewProductReviewRepository(db *pgxpool.Pool) *ProductReviewRepository {
	return &ProductReviewRepository{db: db}
}

const productReviewSelectCols = `
	r.id, r.product_id, r.shop_id, r.seller_id, r.customer_id, r.order_id, r.order_item_id,
	r.rating, r.product_quality_rating, r.shipping_rating, r.seller_service_rating,
	r.title, r.body, r.is_anonymous, r.status, r.seller_reply, r.seller_replied_at,
	r.helpful_count, r.created_at, r.updated_at`

func scanProductReview(scanner interface{ Scan(dest ...any) error }, r *models.ProductReview) error {
	return scanner.Scan(
		&r.ID, &r.ProductID, &r.ShopID, &r.SellerID, &r.CustomerID, &r.OrderID, &r.OrderItemID,
		&r.Rating, &r.ProductQualityRating, &r.ShippingRating, &r.SellerServiceRating,
		&r.Title, &r.Body, &r.IsAnonymous, &r.Status, &r.SellerReply, &r.SellerRepliedAt,
		&r.HelpfulCount, &r.CreatedAt, &r.UpdatedAt,
	)
}

// ProductReviewListQuery filters / pages review lists.
type ProductReviewListQuery struct {
	ProductID       string
	ShopID          string
	SellerID        string
	CustomerID      string
	Status          string // empty = published only for public; "any" for owner lists
	Limit           int
	CursorCreatedAt *time.Time
	CursorID        *uuid.UUID
}

// Create inserts the review and optional media assets in one transaction.
func (r *ProductReviewRepository) Create(
	ctx context.Context,
	review *models.ProductReview,
	assets []models.MediaAsset,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		insert into marketplace.product_reviews (
			product_id, shop_id, seller_id, customer_id, order_id, order_item_id,
			rating, product_quality_rating, shipping_rating, seller_service_rating,
			title, body, is_anonymous, status
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		returning id, helpful_count, created_at, updated_at`,
		review.ProductID, review.ShopID, review.SellerID, review.CustomerID,
		review.OrderID, review.OrderItemID,
		review.Rating, review.ProductQualityRating, review.ShippingRating, review.SellerServiceRating,
		review.Title, review.Body, review.IsAnonymous, review.Status,
	).Scan(&review.ID, &review.HelpfulCount, &review.CreatedAt, &review.UpdatedAt)
	if err != nil {
		return mapProductReviewWriteError(err)
	}

	if err := insertProductReviewMediaTx(ctx, tx, review.ID, assets); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Update rewrites text/ratings. When replaceMedia is true, media is replaced with assets
// (empty assets clears photos).
func (r *ProductReviewRepository) Update(
	ctx context.Context,
	review *models.ProductReview,
	assets []models.MediaAsset,
	replaceMedia bool,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		update marketplace.product_reviews
		set rating = $2,
		    product_quality_rating = $3,
		    shipping_rating = $4,
		    seller_service_rating = $5,
		    title = $6,
		    body = $7,
		    is_anonymous = $8,
		    updated_at = now()
		where id = $1 and customer_id = $9
		returning helpful_count, seller_reply, seller_replied_at, status, created_at, updated_at`,
		review.ID, review.Rating, review.ProductQualityRating, review.ShippingRating,
		review.SellerServiceRating, review.Title, review.Body, review.IsAnonymous, review.CustomerID,
	).Scan(
		&review.HelpfulCount, &review.SellerReply, &review.SellerRepliedAt,
		&review.Status, &review.CreatedAt, &review.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProductReviewNotFound
	}
	if err != nil {
		return err
	}

	if replaceMedia {
		var oldAssetIDs []uuid.UUID
		rows, err := tx.Query(ctx,
			`select media_asset_id from marketplace.product_review_media where review_id = $1`, review.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			oldAssetIDs = append(oldAssetIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(oldAssetIDs) > 0 {
			if _, err := tx.Exec(ctx,
				`delete from media.media_assets where id = any($1)`, oldAssetIDs); err != nil {
				return err
			}
		}
		if err := insertProductReviewMediaTx(ctx, tx, review.ID, assets); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// Delete removes a customer's review (cascades media links; deletes media assets first).
func (r *ProductReviewRepository) Delete(ctx context.Context, customerID, reviewID string) ([]string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx,
		`select exists(select 1 from marketplace.product_reviews where id = $1 and customer_id = $2)`,
		reviewID, customerID,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrProductReviewNotFound
	}

	rows, err := tx.Query(ctx, `
		select ma.id, ma.object_path
		from media.media_assets ma
		where ma.id in (
			select media_asset_id from marketplace.product_review_media where review_id = $1
		)`, reviewID)
	if err != nil {
		return nil, err
	}
	var assetIDs []uuid.UUID
	var paths []string
	for rows.Next() {
		var id uuid.UUID
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return nil, err
		}
		assetIDs = append(assetIDs, id)
		paths = append(paths, path)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx,
		`delete from marketplace.product_reviews where id = $1 and customer_id = $2`,
		reviewID, customerID); err != nil {
		return nil, err
	}
	if len(assetIDs) > 0 {
		if _, err := tx.Exec(ctx,
			`delete from media.media_assets where id = any($1)`, assetIDs); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return paths, nil
}

// GetByID loads one review (any status).
func (r *ProductReviewRepository) GetByID(ctx context.Context, reviewID string) (*models.ProductReviewDetails, error) {
	return r.getOne(ctx, `
		select `+productReviewSelectCols+`
		from marketplace.product_reviews r
		where r.id = $1`, reviewID)
}

// GetPublicByID loads one published review.
func (r *ProductReviewRepository) GetPublicByID(ctx context.Context, reviewID string) (*models.ProductReviewDetails, error) {
	return r.getOne(ctx, `
		select `+productReviewSelectCols+`
		from marketplace.product_reviews r
		where r.id = $1 and r.status = 'published'`, reviewID)
}

// GetForCustomer loads a review owned by the customer.
func (r *ProductReviewRepository) GetForCustomer(ctx context.Context, customerID, reviewID string) (*models.ProductReviewDetails, error) {
	return r.getOne(ctx, `
		select `+productReviewSelectCols+`
		from marketplace.product_reviews r
		where r.id = $1 and r.customer_id = $2`, reviewID, customerID)
}

// GetForSeller loads a review for a product belonging to the seller.
func (r *ProductReviewRepository) GetForSeller(ctx context.Context, sellerID, reviewID string) (*models.ProductReviewDetails, error) {
	return r.getOne(ctx, `
		select `+productReviewSelectCols+`
		from marketplace.product_reviews r
		where r.id = $1 and r.seller_id = $2`, reviewID, sellerID)
}

func (r *ProductReviewRepository) getOne(ctx context.Context, query string, args ...any) (*models.ProductReviewDetails, error) {
	var review models.ProductReview
	err := scanProductReview(r.db.QueryRow(ctx, query, args...), &review)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductReviewNotFound
	}
	if err != nil {
		return nil, err
	}
	details := &models.ProductReviewDetails{ProductReview: review, Media: []models.ProductReviewMediaItem{}}
	media, err := r.loadMedia(ctx, []uuid.UUID{review.ID})
	if err != nil {
		return nil, err
	}
	details.Media = media[review.ID]
	if details.Media == nil {
		details.Media = []models.ProductReviewMediaItem{}
	}
	customers, err := r.loadCustomers(ctx, []uuid.UUID{review.CustomerID})
	if err != nil {
		return nil, err
	}
	details.Customer = publicCustomer(customers[review.CustomerID], review.IsAnonymous)
	return details, nil
}

// List returns a page of reviews matching the query.
func (r *ProductReviewRepository) List(ctx context.Context, q ProductReviewListQuery) ([]models.ProductReviewDetails, error) {
	limit := q.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	statusFilter := "published"
	if q.Status == "any" {
		statusFilter = ""
	} else if q.Status != "" {
		statusFilter = q.Status
	}

	rows, err := r.db.Query(ctx, `
		select `+productReviewSelectCols+`
		from marketplace.product_reviews r
		where ($1::uuid is null or r.product_id = $1)
		  and ($2::uuid is null or r.shop_id = $2)
		  and ($3::uuid is null or r.seller_id = $3)
		  and ($4::uuid is null or r.customer_id = $4)
		  and ($5::text = '' or r.status = $5)
		  and (
			$6::timestamptz is null
			or (r.created_at, r.id) < ($6::timestamptz, $7::uuid)
		  )
		order by r.created_at desc, r.id desc
		limit $8`,
		nullableUUID(q.ProductID),
		nullableUUID(q.ShopID),
		nullableUUID(q.SellerID),
		nullableUUID(q.CustomerID),
		statusFilter,
		q.CursorCreatedAt,
		q.CursorID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]models.ProductReviewDetails, 0)
	ids := make([]uuid.UUID, 0)
	customerIDs := make([]uuid.UUID, 0)
	for rows.Next() {
		var review models.ProductReview
		if err := scanProductReview(rows, &review); err != nil {
			return nil, err
		}
		items = append(items, models.ProductReviewDetails{
			ProductReview: review,
			Media:         []models.ProductReviewMediaItem{},
		})
		ids = append(ids, review.ID)
		customerIDs = append(customerIDs, review.CustomerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return items, nil
	}

	media, err := r.loadMedia(ctx, ids)
	if err != nil {
		return nil, err
	}
	customers, err := r.loadCustomers(ctx, customerIDs)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Media = media[items[i].ID]
		if items[i].Media == nil {
			items[i].Media = []models.ProductReviewMediaItem{}
		}
		items[i].Customer = publicCustomer(customers[items[i].CustomerID], items[i].IsAnonymous)
	}
	return items, nil
}

// Summary returns aggregated published ratings for a product.
func (r *ProductReviewRepository) Summary(ctx context.Context, productID string) (*models.ProductReviewSummary, error) {
	out := &models.ProductReviewSummary{
		RatingBreakdown: map[string]int{"1": 0, "2": 0, "3": 0, "4": 0, "5": 0},
	}
	pid, err := uuid.Parse(productID)
	if err != nil {
		return nil, ErrProductReviewNotFound
	}
	out.ProductID = pid

	err = r.db.QueryRow(ctx, `
		select
			count(*)::int,
			coalesce(avg(rating), 0),
			coalesce(avg(product_quality_rating), 0),
			coalesce(avg(shipping_rating), 0),
			coalesce(avg(seller_service_rating), 0)
		from marketplace.product_reviews
		where product_id = $1 and status = 'published'`, productID,
	).Scan(
		&out.ReviewCount,
		&out.AvgRating,
		&out.AvgProductQualityRating,
		&out.AvgShippingRating,
		&out.AvgSellerServiceRating,
	)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.Query(ctx, `
		select rating, count(*)::int
		from marketplace.product_reviews
		where product_id = $1 and status = 'published'
		group by rating`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rating, count int
		if err := rows.Scan(&rating, &count); err != nil {
			return nil, err
		}
		out.RatingBreakdown[itoaRating(rating)] = count
	}
	return out, rows.Err()
}

// SetSellerReply sets or clears the seller reply on a review the seller owns.
func (r *ProductReviewRepository) SetSellerReply(ctx context.Context, sellerID, reviewID string, reply *string) (*models.ProductReviewDetails, error) {
	var repliedAt *time.Time
	if reply != nil {
		now := time.Now().UTC()
		repliedAt = &now
	}
	tag, err := r.db.Exec(ctx, `
		update marketplace.product_reviews
		set seller_reply = $3,
		    seller_replied_at = $4,
		    updated_at = now()
		where id = $1 and seller_id = $2`,
		reviewID, sellerID, reply, repliedAt)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrProductReviewNotFound
	}
	return r.GetForSeller(ctx, sellerID, reviewID)
}

// UpsertVote inserts or updates a helpful vote and refreshes helpful_count.
func (r *ProductReviewRepository) UpsertVote(ctx context.Context, reviewID, customerID string, isHelpful bool) (*models.ProductReviewVote, int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx,
		`select exists(select 1 from marketplace.product_reviews where id = $1 and status = 'published')`,
		reviewID,
	).Scan(&exists); err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, ErrProductReviewNotFound
	}

	vote := &models.ProductReviewVote{}
	err = tx.QueryRow(ctx, `
		insert into marketplace.product_review_votes (review_id, customer_id, is_helpful)
		values ($1, $2, $3)
		on conflict (review_id, customer_id) do update
		set is_helpful = excluded.is_helpful
		returning id, review_id, customer_id, is_helpful, created_at`,
		reviewID, customerID, isHelpful,
	).Scan(&vote.ID, &vote.ReviewID, &vote.CustomerID, &vote.IsHelpful, &vote.CreatedAt)
	if err != nil {
		return nil, 0, err
	}

	var helpfulCount int
	if err := tx.QueryRow(ctx, `
		update marketplace.product_reviews
		set helpful_count = (
			select count(*)::int from marketplace.product_review_votes
			where review_id = $1 and is_helpful = true
		),
		updated_at = now()
		where id = $1
		returning helpful_count`, reviewID,
	).Scan(&helpfulCount); err != nil {
		return nil, 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return vote, helpfulCount, nil
}

// DeleteVote removes a customer's vote and refreshes helpful_count.
func (r *ProductReviewRepository) DeleteVote(ctx context.Context, reviewID, customerID string) (int, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`delete from marketplace.product_review_votes where review_id = $1 and customer_id = $2`,
		reviewID, customerID)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrProductReviewVoteNotFound
	}

	var helpfulCount int
	if err := tx.QueryRow(ctx, `
		update marketplace.product_reviews
		set helpful_count = (
			select count(*)::int from marketplace.product_review_votes
			where review_id = $1 and is_helpful = true
		),
		updated_at = now()
		where id = $1
		returning helpful_count`, reviewID,
	).Scan(&helpfulCount); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrProductReviewNotFound
		}
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return helpfulCount, nil
}

// GetVote returns the caller's vote on a review, if any.
func (r *ProductReviewRepository) GetVote(ctx context.Context, reviewID, customerID string) (*models.ProductReviewVote, error) {
	vote := &models.ProductReviewVote{}
	err := r.db.QueryRow(ctx, `
		select id, review_id, customer_id, is_helpful, created_at
		from marketplace.product_review_votes
		where review_id = $1 and customer_id = $2`, reviewID, customerID,
	).Scan(&vote.ID, &vote.ReviewID, &vote.CustomerID, &vote.IsHelpful, &vote.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductReviewVoteNotFound
	}
	return vote, err
}

func (r *ProductReviewRepository) loadMedia(ctx context.Context, reviewIDs []uuid.UUID) (map[uuid.UUID][]models.ProductReviewMediaItem, error) {
	out := make(map[uuid.UUID][]models.ProductReviewMediaItem, len(reviewIDs))
	if len(reviewIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select rm.review_id, rm.media_asset_id, rm.position,
		       ma.asset_type, ma.bucket, ma.object_path, ma.cdn_url, ma.mime_type, ma.size_bytes
		from marketplace.product_review_media rm
		inner join media.media_assets ma on ma.id = rm.media_asset_id
		where rm.review_id = any($1)
		order by rm.review_id, rm.position`, reviewIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var reviewID uuid.UUID
		var item models.ProductReviewMediaItem
		if err := rows.Scan(
			&reviewID, &item.MediaAssetID, &item.Position,
			&item.AssetType, &item.Bucket, &item.ObjectPath, &item.CDNURL, &item.MimeType, &item.SizeBytes,
		); err != nil {
			return nil, err
		}
		out[reviewID] = append(out[reviewID], item)
	}
	return out, rows.Err()
}

func (r *ProductReviewRepository) loadCustomers(ctx context.Context, customerIDs []uuid.UUID) (map[uuid.UUID]models.ProductReviewCustomer, error) {
	out := make(map[uuid.UUID]models.ProductReviewCustomer, len(customerIDs))
	if len(customerIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select id, display_name, image_url
		from customer.customers
		where id = any($1)`, customerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var c models.ProductReviewCustomer
		if err := rows.Scan(&id, &c.DisplayName, &c.ImageURL); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

func publicCustomer(c models.ProductReviewCustomer, anonymous bool) *models.ProductReviewCustomer {
	if anonymous {
		return &models.ProductReviewCustomer{DisplayName: strPtr("Anonymous")}
	}
	return &c
}

func strPtr(s string) *string { return &s }

func insertProductReviewMediaTx(ctx context.Context, tx pgx.Tx, reviewID uuid.UUID, assets []models.MediaAsset) error {
	for i := range assets {
		if err := insertMediaAssetTx(ctx, tx, &assets[i]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into marketplace.product_review_media (review_id, media_asset_id, position)
			values ($1,$2,$3)`, reviewID, assets[i].ID, i); err != nil {
			return err
		}
	}
	return nil
}

func mapProductReviewWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrProductReviewDuplicate
	}
	return err
}

func itoaRating(n int) string {
	switch n {
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	case 4:
		return "4"
	case 5:
		return "5"
	default:
		return "0"
	}
}
