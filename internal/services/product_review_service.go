package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrProductReviewNotFound     = errors.New("product review not found")
	ErrProductReviewDuplicate    = errors.New("product review already exists")
	ErrProductReviewNotEligible  = errors.New("order item not eligible for review")
	ErrInvalidProductReview      = errors.New("invalid product review")
	ErrProductReviewVoteNotFound = errors.New("product review vote not found")
	ErrProductReviewForbidden    = errors.New("product review forbidden")
)

const maxReviewMediaItems = 9

// ProductReviewService owns verified-purchase product reviews.
type ProductReviewService struct {
	reviews *repository.ProductReviewRepository
	orders  *repository.OrderRepository
	s3      *S3Service
	bucket  string
}

func NewProductReviewService(
	reviews *repository.ProductReviewRepository,
	orders *repository.OrderRepository,
	s3 *S3Service,
	bucket string,
) *ProductReviewService {
	return &ProductReviewService{reviews: reviews, orders: orders, s3: s3, bucket: bucket}
}

// ReviewMediaInput is one already-uploaded file (via /media/presign-upload folder review-photo).
type ReviewMediaInput struct {
	ObjectPath string          `json:"object_path"`
	MimeType   string          `json:"mime_type"`
	SizeBytes  int64           `json:"size_bytes"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// CreateReviewInput is the POST body for creating a review on a delivered order item.
type CreateReviewInput struct {
	Rating               int                `json:"rating"`
	ProductQualityRating int                `json:"product_quality_rating"`
	ShippingRating       int                `json:"shipping_rating"`
	SellerServiceRating  int                `json:"seller_service_rating"`
	Title                *string            `json:"title"`
	Body                 *string            `json:"body"`
	IsAnonymous          bool               `json:"is_anonymous"`
	Media                []ReviewMediaInput `json:"media"`
}

// UpdateReviewInput is the PUT body for editing a customer's own review.
type UpdateReviewInput struct {
	Rating               int                 `json:"rating"`
	ProductQualityRating int                 `json:"product_quality_rating"`
	ShippingRating       int                 `json:"shipping_rating"`
	SellerServiceRating  int                 `json:"seller_service_rating"`
	Title                *string             `json:"title"`
	Body                 *string             `json:"body"`
	IsAnonymous          bool                `json:"is_anonymous"`
	Media                *[]ReviewMediaInput `json:"media"` // nil = keep existing; empty slice = clear
}

// SellerReplyInput is the seller response body.
type SellerReplyInput struct {
	SellerReply string `json:"seller_reply"`
}

// VoteInput is the helpful / not-helpful vote body.
type VoteInput struct {
	IsHelpful bool `json:"is_helpful"`
}

// ReviewListFilter pages public or owner review lists.
type ReviewListFilter struct {
	ProductID  string
	ShopID     string
	SellerID   string
	CustomerID string
	Status     string
	Cursor     string
	Limit      int
}

// Create creates a verified-purchase review for a delivered order line.
func (s *ProductReviewService) Create(ctx context.Context, customerID, orderItemID string, in CreateReviewInput) (*models.ProductReviewDetails, error) {
	if err := validateRatings(in.Rating, in.ProductQualityRating, in.ShippingRating, in.SellerServiceRating); err != nil {
		return nil, err
	}

	item, err := s.orders.GetItemForCustomer(ctx, customerID, orderItemID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderItemNotFound) {
			return nil, ErrProductReviewNotEligible
		}
		return nil, err
	}
	if item.FulfilmentStatus != "delivered" {
		return nil, ErrProductReviewNotEligible
	}

	customerUUID, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrInvalidProductReview
	}

	assets, err := s.buildAssets(customerUUID, in.Media)
	if err != nil {
		return nil, err
	}

	review := &models.ProductReview{
		ProductID:            item.ProductID,
		ShopID:               item.ShopID,
		SellerID:             item.SellerID,
		CustomerID:           customerUUID,
		OrderID:              item.OrderID,
		OrderItemID:          item.ID,
		Rating:               in.Rating,
		ProductQualityRating: in.ProductQualityRating,
		ShippingRating:       in.ShippingRating,
		SellerServiceRating:  in.SellerServiceRating,
		Title:                normalizeOptionalText(in.Title, 120),
		Body:                 normalizeOptionalText(in.Body, 5000),
		IsAnonymous:          in.IsAnonymous,
		Status:               "published",
	}

	if err := s.reviews.Create(ctx, review, assets); err != nil {
		if errors.Is(err, repository.ErrProductReviewDuplicate) {
			return nil, ErrProductReviewDuplicate
		}
		return nil, err
	}
	return s.reviews.GetForCustomer(ctx, customerID, review.ID.String())
}

// Update edits a customer's own review.
func (s *ProductReviewService) Update(ctx context.Context, customerID, reviewID string, in UpdateReviewInput) (*models.ProductReviewDetails, error) {
	if err := validateRatings(in.Rating, in.ProductQualityRating, in.ShippingRating, in.SellerServiceRating); err != nil {
		return nil, err
	}

	existing, err := s.reviews.GetForCustomer(ctx, customerID, reviewID)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}

	customerUUID, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrInvalidProductReview
	}

	review := existing.ProductReview
	review.Rating = in.Rating
	review.ProductQualityRating = in.ProductQualityRating
	review.ShippingRating = in.ShippingRating
	review.SellerServiceRating = in.SellerServiceRating
	review.Title = normalizeOptionalText(in.Title, 120)
	review.Body = normalizeOptionalText(in.Body, 5000)
	review.IsAnonymous = in.IsAnonymous

	var assets []models.MediaAsset
	replaceMedia := false
	if in.Media != nil {
		replaceMedia = true
		assets, err = s.buildAssets(customerUUID, *in.Media)
		if err != nil {
			return nil, err
		}
	}

	if err := s.reviews.Update(ctx, &review, assets, replaceMedia); err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}
	return s.reviews.GetForCustomer(ctx, customerID, reviewID)
}

// Delete removes a customer's review and best-effort deletes S3 objects.
func (s *ProductReviewService) Delete(ctx context.Context, customerID, reviewID string) error {
	paths, err := s.reviews.Delete(ctx, customerID, reviewID)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return ErrProductReviewNotFound
		}
		return err
	}
	for _, path := range paths {
		if s.s3 == nil {
			continue
		}
		if err := s.s3.Delete(ctx, path); err != nil {
			log.Printf("review delete: could not remove object %s: %v", path, err)
		}
	}
	return nil
}

// GetMine returns one of the customer's reviews.
func (s *ProductReviewService) GetMine(ctx context.Context, customerID, reviewID string) (*models.ProductReviewDetails, error) {
	details, err := s.reviews.GetForCustomer(ctx, customerID, reviewID)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}
	return details, nil
}

// ListMine returns all reviews written by the customer.
func (s *ProductReviewService) ListMine(ctx context.Context, customerID string, f ReviewListFilter) (*models.ProductReviewList, error) {
	f.CustomerID = customerID
	f.Status = "any"
	return s.list(ctx, f, "")
}

// GetPublic returns one published review.
func (s *ProductReviewService) GetPublic(ctx context.Context, reviewID, viewerCustomerID string) (*models.ProductReviewDetails, error) {
	details, err := s.reviews.GetPublicByID(ctx, reviewID)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}
	s.attachVote(ctx, details, viewerCustomerID)
	return details, nil
}

// ListPublic returns published reviews for a product or shop.
func (s *ProductReviewService) ListPublic(ctx context.Context, f ReviewListFilter, viewerCustomerID string) (*models.ProductReviewList, error) {
	f.Status = "published"
	return s.list(ctx, f, viewerCustomerID)
}

// Summary returns aggregated published ratings for a product.
func (s *ProductReviewService) Summary(ctx context.Context, productID string) (*models.ProductReviewSummary, error) {
	if _, err := uuid.Parse(productID); err != nil {
		return nil, ErrInvalidProductReview
	}
	return s.reviews.Summary(ctx, productID)
}

// ListForSeller returns reviews on the seller's products.
func (s *ProductReviewService) ListForSeller(ctx context.Context, sellerID string, f ReviewListFilter) (*models.ProductReviewList, error) {
	f.SellerID = sellerID
	f.Status = "any"
	return s.list(ctx, f, "")
}

// GetForSeller returns one review belonging to the seller's catalogue.
func (s *ProductReviewService) GetForSeller(ctx context.Context, sellerID, reviewID string) (*models.ProductReviewDetails, error) {
	details, err := s.reviews.GetForSeller(ctx, sellerID, reviewID)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}
	return details, nil
}

// ReplyAsSeller sets the seller reply text.
func (s *ProductReviewService) ReplyAsSeller(ctx context.Context, sellerID, reviewID string, in SellerReplyInput) (*models.ProductReviewDetails, error) {
	reply := strings.TrimSpace(in.SellerReply)
	if reply == "" || len(reply) > 2000 {
		return nil, ErrInvalidProductReview
	}
	details, err := s.reviews.SetSellerReply(ctx, sellerID, reviewID, &reply)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}
	return details, nil
}

// ClearSellerReply removes the seller reply.
func (s *ProductReviewService) ClearSellerReply(ctx context.Context, sellerID, reviewID string) (*models.ProductReviewDetails, error) {
	details, err := s.reviews.SetSellerReply(ctx, sellerID, reviewID, nil)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}
	return details, nil
}

// Vote sets helpful / not-helpful for a published review.
func (s *ProductReviewService) Vote(ctx context.Context, customerID, reviewID string, in VoteInput) (map[string]any, error) {
	vote, helpfulCount, err := s.reviews.UpsertVote(ctx, reviewID, customerID, in.IsHelpful)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}
	return map[string]any{
		"vote":          vote,
		"helpful_count": helpfulCount,
	}, nil
}

// ClearVote removes the customer's vote.
func (s *ProductReviewService) ClearVote(ctx context.Context, customerID, reviewID string) (map[string]any, error) {
	helpfulCount, err := s.reviews.DeleteVote(ctx, reviewID, customerID)
	if err != nil {
		if errors.Is(err, repository.ErrProductReviewVoteNotFound) {
			return nil, ErrProductReviewVoteNotFound
		}
		if errors.Is(err, repository.ErrProductReviewNotFound) {
			return nil, ErrProductReviewNotFound
		}
		return nil, err
	}
	return map[string]any{"helpful_count": helpfulCount}, nil
}

func (s *ProductReviewService) list(ctx context.Context, f ReviewListFilter, viewerCustomerID string) (*models.ProductReviewList, error) {
	limit := f.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	q := repository.ProductReviewListQuery{
		ProductID:  strings.TrimSpace(f.ProductID),
		ShopID:     strings.TrimSpace(f.ShopID),
		SellerID:   strings.TrimSpace(f.SellerID),
		CustomerID: strings.TrimSpace(f.CustomerID),
		Status:     f.Status,
		Limit:      limit + 1, // fetch one extra to detect next page
	}

	if cursor := strings.TrimSpace(f.Cursor); cursor != "" {
		createdAt, id, err := decodeReviewCursor(cursor)
		if err != nil {
			return nil, ErrInvalidCursor
		}
		q.CursorCreatedAt = &createdAt
		q.CursorID = &id
	}

	items, err := s.reviews.List(ctx, q)
	if err != nil {
		return nil, err
	}

	out := &models.ProductReviewList{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		out.Items = items[:limit]
		cursor := encodeReviewCursor(last.CreatedAt, last.ID)
		out.NextCursor = &cursor
	}
	if out.Items == nil {
		out.Items = []models.ProductReviewDetails{}
	}
	for i := range out.Items {
		s.attachVote(ctx, &out.Items[i], viewerCustomerID)
	}
	return out, nil
}

func (s *ProductReviewService) attachVote(ctx context.Context, details *models.ProductReviewDetails, customerID string) {
	if details == nil || strings.TrimSpace(customerID) == "" {
		return
	}
	vote, err := s.reviews.GetVote(ctx, details.ID.String(), customerID)
	if err != nil {
		return
	}
	v := vote.IsHelpful
	details.VotedHelpful = &v
}

func (s *ProductReviewService) buildAssets(customerID uuid.UUID, in []ReviewMediaInput) ([]models.MediaAsset, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxReviewMediaItems {
		return nil, ErrInvalidProductReview
	}
	assets := make([]models.MediaAsset, 0, len(in))
	for _, item := range in {
		objectPath := strings.TrimSpace(item.ObjectPath)
		mimeType := strings.ToLower(strings.TrimSpace(item.MimeType))
		if objectPath == "" || mimeType == "" || item.SizeBytes < 0 {
			return nil, ErrInvalidProductReview
		}
		if !strings.HasPrefix(objectPath, "public/reviews/") {
			return nil, ErrInvalidProductReview
		}
		if !strings.HasPrefix(mimeType, "image/") && !strings.HasPrefix(mimeType, "video/") {
			return nil, ErrInvalidProductReview
		}
		assetType := "image"
		if strings.HasPrefix(mimeType, "video/") {
			assetType = "video"
		}
		metadata := item.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		owner := customerID
		asset := models.MediaAsset{
			OwnerType:        "customer",
			OwnerID:          &owner,
			AssetType:        assetType,
			Bucket:           s.bucket,
			ObjectPath:       objectPath,
			MimeType:         mimeType,
			SizeBytes:        item.SizeBytes,
			ProcessingStatus: "ready",
			ModerationStatus: "approved",
			Metadata:         metadata,
		}
		if strings.HasPrefix(objectPath, "public/") && s.s3 != nil {
			url := s.s3.PublicURL(objectPath)
			asset.CDNURL = &url
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func validateRatings(overall, quality, shipping, service int) error {
	for _, r := range []int{overall, quality, shipping, service} {
		if r < 1 || r > 5 {
			return ErrInvalidProductReview
		}
	}
	return nil
}

func normalizeOptionalText(v *string, max int) *string {
	if v == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > max {
		trimmed = trimmed[:max]
	}
	return &trimmed
}

func encodeReviewCursor(createdAt time.Time, id uuid.UUID) string {
	raw := fmt.Sprintf("%s|%s", createdAt.UTC().Format(time.RFC3339Nano), id.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeReviewCursor(cursor string) (time.Time, uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	return createdAt, id, nil
}
