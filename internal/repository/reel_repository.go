package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var ErrReelNotFound = errors.New("reel not found")

// ReelRepository persists seller reels (seller.reels + seller.reel_media)
// together with the underlying files in media.media_assets.
type ReelRepository struct {
	db *pgxpool.Pool
}

func NewReelRepository(db *pgxpool.Pool) *ReelRepository {
	return &ReelRepository{db: db}
}

const reelSelectCols = `
	r.id, r.seller_id, r.shop_id, r.product_id, r.thumbnail_media_id, r.reel_type,
	r.caption, r.hashtags, r.visibility, r.status, r.duration_ms, r.view_count,
	r.like_count, r.comment_count,
	r.published_at, r.created_at, r.updated_at`

func scanReel(scanner interface{ Scan(dest ...any) error }, r *models.Reel) error {
	return scanner.Scan(
		&r.ID, &r.SellerID, &r.ShopID, &r.ProductID, &r.ThumbnailMediaID, &r.ReelType,
		&r.Caption, &r.Hashtags, &r.Visibility, &r.Status, &r.DurationMs, &r.ViewCount,
		&r.LikeCount, &r.CommentCount,
		&r.PublishedAt, &r.CreatedAt, &r.UpdatedAt,
	)
}

// ReelFeedQuery filters the public customer-facing reel feed.
// CursorPublishedAt/CursorID page through results in (published_at, id) desc order.
// Scope: "" = all, "shop" = shop-only reels (no product tagged), "product" = product reels.
type ReelFeedQuery struct {
	ShopID            string
	ProductID         string
	Scope             string
	Limit             int
	CursorPublishedAt *time.Time
	CursorID          *uuid.UUID
}

// Create inserts the media assets, the reel, and the ordered reel_media links in one transaction.
// assets must be ordered as they should appear; thumbnail is optional.
func (r *ReelRepository) Create(
	ctx context.Context,
	reel *models.Reel,
	assets []models.MediaAsset,
	thumbnail *models.MediaAsset,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if thumbnail != nil {
		if err := insertMediaAssetTx(ctx, tx, thumbnail); err != nil {
			return err
		}
		reel.ThumbnailMediaID = &thumbnail.ID
	}

	err = tx.QueryRow(ctx, `
		insert into seller.reels (
			seller_id, shop_id, product_id, thumbnail_media_id, reel_type,
			caption, hashtags, visibility, status, duration_ms, published_at
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		returning id, view_count, created_at, updated_at`,
		reel.SellerID, reel.ShopID, reel.ProductID, reel.ThumbnailMediaID, reel.ReelType,
		reel.Caption, reel.Hashtags, reel.Visibility, reel.Status, reel.DurationMs, reel.PublishedAt,
	).Scan(&reel.ID, &reel.ViewCount, &reel.CreatedAt, &reel.UpdatedAt)
	if err != nil {
		return err
	}

	if err := insertReelMediaTx(ctx, tx, reel.ID, assets); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Update rewrites the reel row. When assets is non-nil the existing media is replaced;
// when thumbnail is non-nil the old thumbnail asset is replaced.
func (r *ReelRepository) Update(
	ctx context.Context,
	reel *models.Reel,
	assets []models.MediaAsset,
	thumbnail *models.MediaAsset,
	replaceMedia bool,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if thumbnail != nil {
		if err := insertMediaAssetTx(ctx, tx, thumbnail); err != nil {
			return err
		}
		reel.ThumbnailMediaID = &thumbnail.ID
	}

	if replaceMedia {
		// Deleting the old assets cascades to seller.reel_media.
		var oldAssetIDs []uuid.UUID
		rows, err := tx.Query(ctx,
			`select media_asset_id from seller.reel_media where reel_id = $1`, reel.ID)
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
		if err := insertReelMediaTx(ctx, tx, reel.ID, assets); err != nil {
			return err
		}
	}

	err = tx.QueryRow(ctx, `
		update seller.reels
		set product_id = $2,
		    thumbnail_media_id = coalesce($3, thumbnail_media_id),
		    reel_type = $4,
		    caption = $5,
		    hashtags = $6,
		    visibility = $7,
		    status = $8,
		    duration_ms = $9,
		    published_at = $10,
		    updated_at = now()
		where id = $1
		returning view_count, created_at, updated_at`,
		reel.ID, reel.ProductID, reel.ThumbnailMediaID, reel.ReelType,
		reel.Caption, reel.Hashtags, reel.Visibility, reel.Status, reel.DurationMs, reel.PublishedAt,
	).Scan(&reel.ViewCount, &reel.CreatedAt, &reel.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReelNotFound
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Delete removes the reel and its media asset rows, returning the deleted object paths
// so the caller can clean up storage.
func (r *ReelRepository) Delete(ctx context.Context, sellerID, reelID string) ([]string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx,
		`select exists(select 1 from seller.reels where id = $1 and seller_id = $2)`,
		reelID, sellerID,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrReelNotFound
	}

	rows, err := tx.Query(ctx, `
		select ma.id, ma.object_path
		from media.media_assets ma
		where ma.id in (
			select media_asset_id from seller.reel_media where reel_id = $1
			union
			select thumbnail_media_id from seller.reels where id = $1 and thumbnail_media_id is not null
		)`, reelID)
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

	if _, err := tx.Exec(ctx, `delete from seller.reels where id = $1`, reelID); err != nil {
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

// GetByIDForSeller returns one of the seller's own reels in any status.
func (r *ReelRepository) GetByIDForSeller(ctx context.Context, sellerID, reelID string) (*models.ReelDetails, error) {
	reel := &models.Reel{}
	err := scanReel(r.db.QueryRow(ctx, `
		select `+reelSelectCols+`
		from seller.reels r
		where r.id = $1 and r.seller_id = $2`, reelID, sellerID), reel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReelNotFound
	}
	if err != nil {
		return nil, err
	}
	details := []models.ReelDetails{{Reel: *reel}}
	if err := r.attachRelations(ctx, details); err != nil {
		return nil, err
	}
	return &details[0], nil
}

// ListBySeller returns every reel owned by the seller, newest first.
func (r *ReelRepository) ListBySeller(ctx context.Context, sellerID string) ([]models.ReelDetails, error) {
	return r.listReels(ctx, `
		select `+reelSelectCols+`
		from seller.reels r
		where r.seller_id = $1
		order by r.created_at desc`, sellerID)
}

// ListByShopForSeller returns the seller's reels for one of their shops.
func (r *ReelRepository) ListByShopForSeller(ctx context.Context, sellerID, shopID string) ([]models.ReelDetails, error) {
	return r.listReels(ctx, `
		select `+reelSelectCols+`
		from seller.reels r
		where r.seller_id = $1 and r.shop_id = $2
		order by r.created_at desc`, sellerID, shopID)
}

// GetPublicByID returns a published public reel from an active shop (customer view).
func (r *ReelRepository) GetPublicByID(ctx context.Context, reelID string) (*models.ReelDetails, error) {
	reel := &models.Reel{}
	err := scanReel(r.db.QueryRow(ctx, `
		select `+reelSelectCols+`
		from seller.reels r
		inner join seller.shops s on s.id = r.shop_id
		where r.id = $1
		  and r.status = 'published'
		  and r.visibility = 'public'
		  and s.status = 'active'`, reelID), reel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReelNotFound
	}
	if err != nil {
		return nil, err
	}
	details := []models.ReelDetails{{Reel: *reel}}
	if err := r.attachRelations(ctx, details); err != nil {
		return nil, err
	}
	return &details[0], nil
}

// ListPublicFeed returns published public reels newest first, with keyset pagination.
func (r *ReelRepository) ListPublicFeed(ctx context.Context, q ReelFeedQuery) ([]models.ReelDetails, error) {
	if q.Limit <= 0 {
		q.Limit = 20
	}
	return r.listReels(ctx, `
		select `+reelSelectCols+`
		from seller.reels r
		inner join seller.shops s on s.id = r.shop_id
		where r.status = 'published'
		  and r.visibility = 'public'
		  and s.status = 'active'
		  and ($1::uuid is null or r.shop_id = $1::uuid)
		  and ($2::uuid is null or r.product_id = $2::uuid)
		  and (
		        $3 = ''
		        or ($3 = 'shop' and r.product_id is null)
		        or ($3 = 'product' and r.product_id is not null)
		      )
		  and (
		        $4::timestamptz is null
		        or (r.published_at, r.id) < ($4::timestamptz, $5::uuid)
		      )
		order by r.published_at desc, r.id desc
		limit $6`,
		nullableUUID(q.ShopID), nullableUUID(q.ProductID), q.Scope,
		q.CursorPublishedAt, q.CursorID, q.Limit,
	)
}

// ListByProductForSeller returns the seller's reels tagged to one of their products.
func (r *ReelRepository) ListByProductForSeller(ctx context.Context, sellerID, productID string) ([]models.ReelDetails, error) {
	return r.listReels(ctx, `
		select `+reelSelectCols+`
		from seller.reels r
		where r.seller_id = $1 and r.product_id = $2
		order by r.created_at desc`, sellerID, productID)
}

// ProductShopForSeller returns the product's shop id, proving the seller owns the product.
func (r *ReelRepository) ProductShopForSeller(ctx context.Context, sellerID, productID string) (uuid.UUID, uuid.UUID, error) {
	var shopID, pid uuid.UUID
	err := r.db.QueryRow(ctx, `
		select p.id, p.shop_id
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		where p.id = $1 and s.seller_id = $2`, productID, sellerID,
	).Scan(&pid, &shopID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, ErrProductNotFound
	}
	return pid, shopID, err
}

// IncrementViewCount bumps the view counter for a reel (best effort).
func (r *ReelRepository) IncrementViewCount(ctx context.Context, reelID string) error {
	_, err := r.db.Exec(ctx, `
		update seller.reels
		set view_count = view_count + 1
		where id = $1`, reelID)
	return err
}

// ProductBelongsToShop guards tagging a product that is not in the reel's shop.
func (r *ReelRepository) ProductBelongsToShop(ctx context.Context, productID, shopID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		select exists(
			select 1 from seller.products where id = $1 and shop_id = $2
		)`, productID, shopID,
	).Scan(&exists)
	return exists, err
}

func (r *ReelRepository) listReels(ctx context.Context, query string, args ...any) ([]models.ReelDetails, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.ReelDetails{}
	for rows.Next() {
		var reel models.Reel
		if err := scanReel(rows, &reel); err != nil {
			return nil, err
		}
		items = append(items, models.ReelDetails{Reel: reel})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.attachRelations(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

// attachRelations loads media items, thumbnails, shop, and tagged product for the given reels.
func (r *ReelRepository) attachRelations(ctx context.Context, items []models.ReelDetails) error {
	if len(items) == 0 {
		return nil
	}

	reelIDs := make([]uuid.UUID, 0, len(items))
	shopIDs := make([]uuid.UUID, 0, len(items))
	productIDs := make([]uuid.UUID, 0, len(items))
	thumbIDs := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		reelIDs = append(reelIDs, it.ID)
		shopIDs = append(shopIDs, it.ShopID)
		if it.ProductID != nil {
			productIDs = append(productIDs, *it.ProductID)
		}
		if it.ThumbnailMediaID != nil {
			thumbIDs = append(thumbIDs, *it.ThumbnailMediaID)
		}
	}

	media, err := r.mediaByReel(ctx, reelIDs)
	if err != nil {
		return err
	}
	shops, err := r.shopsByID(ctx, shopIDs)
	if err != nil {
		return err
	}
	products, err := r.productsByID(ctx, productIDs)
	if err != nil {
		return err
	}
	thumbs, err := r.assetsByID(ctx, thumbIDs)
	if err != nil {
		return err
	}

	for i := range items {
		if list, ok := media[items[i].ID]; ok {
			items[i].Media = list
		} else {
			items[i].Media = []models.ReelMediaItem{}
		}
		if shop, ok := shops[items[i].ShopID]; ok {
			s := shop
			items[i].Shop = &s
		}
		if items[i].ProductID != nil {
			if p, ok := products[*items[i].ProductID]; ok {
				prod := p
				items[i].Product = &prod
			}
		}
		if items[i].ThumbnailMediaID != nil {
			if t, ok := thumbs[*items[i].ThumbnailMediaID]; ok {
				thumb := t
				items[i].Thumbnail = &thumb
			}
		}
	}
	return nil
}

func (r *ReelRepository) mediaByReel(ctx context.Context, reelIDs []uuid.UUID) (map[uuid.UUID][]models.ReelMediaItem, error) {
	out := map[uuid.UUID][]models.ReelMediaItem{}
	if len(reelIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select rm.reel_id, rm.media_asset_id, rm.position,
		       ma.asset_type, ma.bucket, ma.object_path, ma.cdn_url,
		       ma.mime_type, ma.size_bytes, ma.metadata
		from seller.reel_media rm
		inner join media.media_assets ma on ma.id = rm.media_asset_id
		where rm.reel_id = any($1)
		order by rm.reel_id, rm.position`, reelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var reelID uuid.UUID
		var item models.ReelMediaItem
		if err := rows.Scan(
			&reelID, &item.MediaAssetID, &item.Position,
			&item.AssetType, &item.Bucket, &item.ObjectPath, &item.CDNURL,
			&item.MimeType, &item.SizeBytes, &item.Metadata,
		); err != nil {
			return nil, err
		}
		out[reelID] = append(out[reelID], item)
	}
	return out, rows.Err()
}

func (r *ReelRepository) assetsByID(ctx context.Context, assetIDs []uuid.UUID) (map[uuid.UUID]models.ReelMediaItem, error) {
	out := map[uuid.UUID]models.ReelMediaItem{}
	if len(assetIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select id, asset_type, bucket, object_path, cdn_url, mime_type, size_bytes, metadata
		from media.media_assets
		where id = any($1)`, assetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var item models.ReelMediaItem
		if err := rows.Scan(
			&item.MediaAssetID, &item.AssetType, &item.Bucket, &item.ObjectPath,
			&item.CDNURL, &item.MimeType, &item.SizeBytes, &item.Metadata,
		); err != nil {
			return nil, err
		}
		out[item.MediaAssetID] = item
	}
	return out, rows.Err()
}

func (r *ReelRepository) shopsByID(ctx context.Context, shopIDs []uuid.UUID) (map[uuid.UUID]models.ReelShopSummary, error) {
	out := map[uuid.UUID]models.ReelShopSummary{}
	if len(shopIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select id, name, slug, image_url
		from seller.shops
		where id = any($1)`, shopIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var s models.ReelShopSummary
		if err := rows.Scan(&s.ID, &s.Name, &s.Slug, &s.ImageURL); err != nil {
			return nil, err
		}
		out[s.ID] = s
	}
	return out, rows.Err()
}

func (r *ReelRepository) productsByID(ctx context.Context, productIDs []uuid.UUID) (map[uuid.UUID]models.ReelProductSummary, error) {
	out := map[uuid.UUID]models.ReelProductSummary{}
	if len(productIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select id, name, slug, price_amount, currency, status, image_url
		from seller.products
		where id = any($1)`, productIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var p models.ReelProductSummary
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Slug, &p.PriceAmount, &p.Currency, &p.Status, &p.ImageURL,
		); err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

// insertMediaAssetTx inserts one media.media_assets row inside a transaction.
func insertMediaAssetTx(ctx context.Context, tx pgx.Tx, asset *models.MediaAsset) error {
	if len(asset.Metadata) == 0 {
		asset.Metadata = []byte(`{}`)
	}
	return tx.QueryRow(ctx, `
		insert into media.media_assets (
			owner_type, owner_id, asset_type, bucket, object_path, cdn_url,
			mime_type, size_bytes, processing_status, moderation_status, metadata
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		returning id, created_at, updated_at`,
		asset.OwnerType, asset.OwnerID, asset.AssetType, asset.Bucket, asset.ObjectPath, asset.CDNURL,
		asset.MimeType, asset.SizeBytes, asset.ProcessingStatus, asset.ModerationStatus, asset.Metadata,
	).Scan(&asset.ID, &asset.CreatedAt, &asset.UpdatedAt)
}

// insertReelMediaTx creates the media assets and links them to the reel in order.
func insertReelMediaTx(ctx context.Context, tx pgx.Tx, reelID uuid.UUID, assets []models.MediaAsset) error {
	for i := range assets {
		if err := insertMediaAssetTx(ctx, tx, &assets[i]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into seller.reel_media (reel_id, media_asset_id, position)
			values ($1,$2,$3)`, reelID, assets[i].ID, i); err != nil {
			return err
		}
	}
	return nil
}

// nullableUUID converts an empty filter string to a nil query arg.
func nullableUUID(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
