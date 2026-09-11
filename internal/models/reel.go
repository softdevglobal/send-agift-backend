package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Reel maps to seller.reels — one short video or photo post by a seller.
type Reel struct {
	ID               uuid.UUID  `json:"id"`
	SellerID         uuid.UUID  `json:"seller_id"`
	ShopID           uuid.UUID  `json:"shop_id"`
	ProductID        *uuid.UUID `json:"product_id,omitempty"`
	ThumbnailMediaID *uuid.UUID `json:"thumbnail_media_id,omitempty"`
	ReelType         string     `json:"reel_type"`  // video | photo
	Caption          *string    `json:"caption,omitempty"`
	Hashtags         []string   `json:"hashtags"`
	Visibility       string     `json:"visibility"` // public | private
	Status           string     `json:"status"`     // draft | published | archived
	DurationMs       *int       `json:"duration_ms,omitempty"`
	ViewCount        int64      `json:"view_count"`
	LikeCount        int64      `json:"like_count"`       // denormalized; social.reel_likes
	CommentCount     int64      `json:"comment_count"`    // denormalized; social.reel_comments
	PublishedAt      *time.Time `json:"published_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// ReelMediaItem is one file on a reel, joined with its media.media_assets row.
// MediaAssetID is the only identifier: a thumbnail has no seller.reel_media row,
// so exposing that link id here would mean different things per field.
type ReelMediaItem struct {
	MediaAssetID uuid.UUID       `json:"media_asset_id"`
	Position     int             `json:"position"`
	AssetType    string          `json:"asset_type"` // image | video
	Bucket       string          `json:"bucket"`
	ObjectPath   string          `json:"object_path"`
	CDNURL       *string         `json:"cdn_url,omitempty"`
	MimeType     string          `json:"mime_type"`
	SizeBytes    int64           `json:"size_bytes"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

// ReelProductSummary is the tagged product shown on a reel (customer can tap to buy).
type ReelProductSummary struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	PriceAmount int       `json:"price_amount"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	ImageURL    *string   `json:"image_url,omitempty"`
}

// ReelShopSummary is the shop shown on a reel in the customer feed.
type ReelShopSummary struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Slug     string    `json:"slug"`
	ImageURL *string   `json:"image_url,omitempty"`
}

// ReelDetails is a reel with its media, plus shop/product context for the feed.
type ReelDetails struct {
	Reel
	Media     []ReelMediaItem     `json:"media"`
	Thumbnail *ReelMediaItem      `json:"thumbnail,omitempty"`
	Shop      *ReelShopSummary    `json:"shop,omitempty"`
	Product   *ReelProductSummary `json:"product,omitempty"`
	// LikedByMe is set when the caller sent JWT or X-Guest-Token and already liked.
	LikedByMe bool `json:"liked_by_me"`
	// RecentLikers is the latest 3 public liker names (feed / get reel).
	RecentLikers []ReelLikerPreview `json:"recent_likers"`
	// Comments is every visible comment on this reel (newest first).
	Comments []ReelCommentView `json:"comments"`
}

// ReelFeed is a page of public reels with a cursor for the next page.
type ReelFeed struct {
	Items      []ReelDetails `json:"items"`
	NextCursor *string       `json:"next_cursor,omitempty"`
}
