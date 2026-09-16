package models

import (
	"time"

	"github.com/google/uuid"
)

// ProductReview maps to marketplace.product_reviews.
type ProductReview struct {
	ID                    uuid.UUID  `json:"id"`
	ProductID             uuid.UUID  `json:"product_id"`
	ShopID                uuid.UUID  `json:"shop_id"`
	SellerID              uuid.UUID  `json:"seller_id"`
	CustomerID            uuid.UUID  `json:"customer_id"`
	OrderID               uuid.UUID  `json:"order_id"`
	OrderItemID           uuid.UUID  `json:"order_item_id"`
	Rating                int        `json:"rating"`
	ProductQualityRating  int        `json:"product_quality_rating"`
	ShippingRating        int        `json:"shipping_rating"`
	SellerServiceRating   int        `json:"seller_service_rating"`
	Title                 *string    `json:"title,omitempty"`
	Body                  *string    `json:"body,omitempty"`
	IsAnonymous           bool       `json:"is_anonymous"`
	Status                string     `json:"status"` // pending | published | hidden | rejected
	SellerReply           *string    `json:"seller_reply,omitempty"`
	SellerRepliedAt       *time.Time `json:"seller_replied_at,omitempty"`
	HelpfulCount          int        `json:"helpful_count"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// ProductReviewMediaItem is one photo on a review, joined with media.media_assets.
type ProductReviewMediaItem struct {
	MediaAssetID uuid.UUID `json:"media_asset_id"`
	Position     int       `json:"position"`
	AssetType    string    `json:"asset_type"`
	Bucket       string    `json:"bucket"`
	ObjectPath   string    `json:"object_path"`
	CDNURL       *string   `json:"cdn_url,omitempty"`
	MimeType     string    `json:"mime_type"`
	SizeBytes    int64     `json:"size_bytes"`
}

// ProductReviewCustomer is the public reviewer identity shown on a review.
type ProductReviewCustomer struct {
	DisplayName *string `json:"display_name,omitempty"`
	ImageURL    *string `json:"image_url,omitempty"`
}

// ProductReviewDetails is a review with media and optional customer preview.
type ProductReviewDetails struct {
	ProductReview
	Media    []ProductReviewMediaItem  `json:"media"`
	Customer *ProductReviewCustomer    `json:"customer,omitempty"`
	// VotedHelpful is set when the authenticated customer has voted on this review.
	VotedHelpful *bool `json:"voted_helpful,omitempty"`
}

// ProductReviewSummary aggregates published ratings for a product (AliExpress-style).
type ProductReviewSummary struct {
	ProductID                  uuid.UUID       `json:"product_id"`
	ReviewCount                int             `json:"review_count"`
	AvgRating                  float64         `json:"avg_rating"`
	AvgProductQualityRating    float64         `json:"avg_product_quality_rating"`
	AvgShippingRating          float64         `json:"avg_shipping_rating"`
	AvgSellerServiceRating     float64         `json:"avg_seller_service_rating"`
	RatingBreakdown            map[string]int  `json:"rating_breakdown"` // "1".."5" → count
}

// ProductReviewList is a page of reviews with an optional next cursor.
type ProductReviewList struct {
	Items      []ProductReviewDetails `json:"items"`
	NextCursor *string                `json:"next_cursor,omitempty"`
}

// ProductReviewVote maps to marketplace.product_review_votes.
type ProductReviewVote struct {
	ID         uuid.UUID `json:"id"`
	ReviewID   uuid.UUID `json:"review_id"`
	CustomerID uuid.UUID `json:"customer_id"`
	IsHelpful  bool      `json:"is_helpful"`
	CreatedAt  time.Time `json:"created_at"`
}
