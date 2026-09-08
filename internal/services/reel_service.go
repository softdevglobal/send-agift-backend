package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrReelNotFound     = errors.New("reel not found")
	ErrInvalidReel      = errors.New("invalid reel")
	ErrReelProduct      = errors.New("product does not belong to this shop")
	ErrInvalidCursor    = errors.New("invalid cursor")
	ErrInvalidReelScope = errors.New("invalid reel scope")
)

// maxReelMediaItems caps how many files one reel can carry (1 video, or a photo carousel).
const maxReelMediaItems = 10

// defaultReelFeedLimit / maxReelFeedLimit bound the public feed page size.
const (
	defaultReelFeedLimit = 20
	maxReelFeedLimit     = 50
)

// ReelService owns seller reel creation and the public customer feed.
type ReelService struct {
	reels   *repository.ReelRepository
	sellers *repository.SellerRepository
	s3      *S3Service
	bucket  string
}

func NewReelService(
	reels *repository.ReelRepository,
	sellers *repository.SellerRepository,
	s3 *S3Service,
	bucket string,
) *ReelService {
	return &ReelService{reels: reels, sellers: sellers, s3: s3, bucket: bucket}
}

// ReelMediaInput is one already-uploaded file (via /media/presign-upload) attached to a reel.
type ReelMediaInput struct {
	ObjectPath string          `json:"object_path"` // S3 key returned by presign-upload
	MimeType   string          `json:"mime_type"`   // video/mp4, image/jpeg, ...
	SizeBytes  int64           `json:"size_bytes"`
	Metadata   json.RawMessage `json:"metadata"` // width, height, duration, etc.
}

// ReelInput is the POST/PUT body for a seller reel.
// On update, a nil Media keeps the existing files.
type ReelInput struct {
	ProductID  *string          `json:"product_id"`
	Caption    *string          `json:"caption"`
	Hashtags   []string         `json:"hashtags"`
	Visibility string           `json:"visibility"` // public | private
	Status     string           `json:"status"`     // draft | published | archived
	DurationMs *int             `json:"duration_ms"`
	Thumbnail  *ReelMediaInput  `json:"thumbnail"`
	Media      []ReelMediaInput `json:"media"`
}

// Create stores a new reel on one of the seller's shops.
// Tagging a product is optional here — leave product_id empty for a shop-only reel.
func (s *ReelService) Create(ctx context.Context, sellerID, shopID string, in ReelInput) (*models.ReelDetails, error) {
	shop, err := s.sellers.GetShopByID(ctx, sellerID, shopID)
	if err != nil {
		if errors.Is(err, repository.ErrShopNotFound) {
			return nil, ErrShopNotFound
		}
		return nil, err
	}
	productID, err := s.resolveProductID(ctx, in.ProductID, shopID)
	if err != nil {
		return nil, err
	}
	return s.create(ctx, sellerID, shop.ID, productID, in)
}

// CreateForProduct stores a reel already tagged to one of the seller's products.
// The shop is taken from the product, and any product_id in the body is ignored.
func (s *ReelService) CreateForProduct(ctx context.Context, sellerID, productID string, in ReelInput) (*models.ReelDetails, error) {
	pid, shopID, err := s.reels.ProductShopForSeller(ctx, sellerID, productID)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	return s.create(ctx, sellerID, shopID, &pid, in)
}

func (s *ReelService) create(
	ctx context.Context,
	sellerID string,
	shopID uuid.UUID,
	productID *uuid.UUID,
	in ReelInput,
) (*models.ReelDetails, error) {
	if len(in.Media) == 0 {
		return nil, ErrInvalidReel
	}
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrInvalidReel
	}
	assets, err := s.buildAssets(sid, in.Media)
	if err != nil {
		return nil, err
	}
	thumbnail, err := s.buildThumbnail(sid, in.Thumbnail)
	if err != nil {
		return nil, err
	}

	reel := &models.Reel{
		SellerID:  sid,
		ShopID:    shopID,
		ProductID: productID,
		ReelType:  reelTypeFromAssets(assets),
		Caption:   normalizeCaption(in.Caption),
		Hashtags:  normalizeHashtags(in.Hashtags),
	}
	if err := applyReelVisibilityStatus(reel, in.Visibility, in.Status, nil); err != nil {
		return nil, err
	}
	if err := applyReelDuration(reel, in.DurationMs); err != nil {
		return nil, err
	}

	if err := s.reels.Create(ctx, reel, assets, thumbnail); err != nil {
		return nil, err
	}
	return s.reels.GetByIDForSeller(ctx, sellerID, reel.ID.String())
}

// Update rewrites a reel the seller owns. Media is replaced only when provided.
func (s *ReelService) Update(ctx context.Context, sellerID, reelID string, in ReelInput) (*models.ReelDetails, error) {
	existing, err := s.reels.GetByIDForSeller(ctx, sellerID, reelID)
	if err != nil {
		if errors.Is(err, repository.ErrReelNotFound) {
			return nil, ErrReelNotFound
		}
		return nil, err
	}

	productID, err := s.resolveProductID(ctx, in.ProductID, existing.ShopID.String())
	if err != nil {
		return nil, err
	}

	reel := existing.Reel
	reel.ProductID = productID
	reel.Caption = normalizeCaption(in.Caption)
	reel.Hashtags = normalizeHashtags(in.Hashtags)
	if err := applyReelVisibilityStatus(&reel, in.Visibility, in.Status, existing.PublishedAt); err != nil {
		return nil, err
	}
	if err := applyReelDuration(&reel, in.DurationMs); err != nil {
		return nil, err
	}

	replaceMedia := in.Media != nil
	var assets []models.MediaAsset
	if replaceMedia {
		if len(in.Media) == 0 {
			return nil, ErrInvalidReel
		}
		assets, err = s.buildAssets(reel.SellerID, in.Media)
		if err != nil {
			return nil, err
		}
		reel.ReelType = reelTypeFromAssets(assets)
	}
	thumbnail, err := s.buildThumbnail(reel.SellerID, in.Thumbnail)
	if err != nil {
		return nil, err
	}

	if err := s.reels.Update(ctx, &reel, assets, thumbnail, replaceMedia); err != nil {
		if errors.Is(err, repository.ErrReelNotFound) {
			return nil, ErrReelNotFound
		}
		return nil, err
	}
	return s.reels.GetByIDForSeller(ctx, sellerID, reelID)
}

// Delete removes the reel, its media rows, and best-effort deletes the S3 objects.
func (s *ReelService) Delete(ctx context.Context, sellerID, reelID string) error {
	paths, err := s.reels.Delete(ctx, sellerID, reelID)
	if err != nil {
		if errors.Is(err, repository.ErrReelNotFound) {
			return ErrReelNotFound
		}
		return err
	}
	for _, path := range paths {
		if err := s.s3.Delete(ctx, path); err != nil {
			log.Printf("reel delete: could not remove object %s: %v", path, err)
		}
	}
	return nil
}

// Get returns one of the seller's own reels (any status).
func (s *ReelService) Get(ctx context.Context, sellerID, reelID string) (*models.ReelDetails, error) {
	details, err := s.reels.GetByIDForSeller(ctx, sellerID, reelID)
	if err != nil {
		if errors.Is(err, repository.ErrReelNotFound) {
			return nil, ErrReelNotFound
		}
		return nil, err
	}
	return details, nil
}

// ListBySeller returns all reels owned by the seller.
func (s *ReelService) ListBySeller(ctx context.Context, sellerID string) ([]models.ReelDetails, error) {
	return s.reels.ListBySeller(ctx, sellerID)
}

// ListByShop returns the seller's reels for one of their shops.
func (s *ReelService) ListByShop(ctx context.Context, sellerID, shopID string) ([]models.ReelDetails, error) {
	if _, err := s.sellers.GetShopByID(ctx, sellerID, shopID); err != nil {
		if errors.Is(err, repository.ErrShopNotFound) {
			return nil, ErrShopNotFound
		}
		return nil, err
	}
	return s.reels.ListByShopForSeller(ctx, sellerID, shopID)
}

// ListByProduct returns the seller's reels tagged to one of their products.
func (s *ReelService) ListByProduct(ctx context.Context, sellerID, productID string) ([]models.ReelDetails, error) {
	if _, _, err := s.reels.ProductShopForSeller(ctx, sellerID, productID); err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	return s.reels.ListByProductForSeller(ctx, sellerID, productID)
}

// GetPublic returns a published public reel and counts the view (customer view).
func (s *ReelService) GetPublic(ctx context.Context, reelID string) (*models.ReelDetails, error) {
	details, err := s.reels.GetPublicByID(ctx, reelID)
	if err != nil {
		if errors.Is(err, repository.ErrReelNotFound) {
			return nil, ErrReelNotFound
		}
		return nil, err
	}
	if err := s.reels.IncrementViewCount(ctx, reelID); err != nil {
		log.Printf("reel view count: %v", err)
	}
	details.ViewCount++
	return details, nil
}

// ReelFeedFilter narrows the public feed. All fields are optional.
// Scope "shop" returns only shop reels (no product tagged); "product" only product reels.
type ReelFeedFilter struct {
	ShopID    string
	ProductID string
	Scope     string
	Cursor    string
	Limit     int
}

// Feed returns a page of published public reels, newest first.
func (s *ReelService) Feed(ctx context.Context, f ReelFeedFilter) (*models.ReelFeed, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultReelFeedLimit
	}
	if limit > maxReelFeedLimit {
		limit = maxReelFeedLimit
	}
	scope := strings.ToLower(strings.TrimSpace(f.Scope))
	switch scope {
	case "", "all":
		scope = ""
	case "shop", "product":
	default:
		return nil, ErrInvalidReelScope
	}
	q := repository.ReelFeedQuery{
		ShopID:    strings.TrimSpace(f.ShopID),
		ProductID: strings.TrimSpace(f.ProductID),
		Scope:     scope,
		Limit:     limit + 1, // fetch one extra to detect another page
	}
	cursor := f.Cursor
	if strings.TrimSpace(cursor) != "" {
		at, id, err := decodeReelCursor(cursor)
		if err != nil {
			return nil, ErrInvalidCursor
		}
		q.CursorPublishedAt = &at
		q.CursorID = &id
	}

	items, err := s.reels.ListPublicFeed(ctx, q)
	if err != nil {
		return nil, err
	}

	feed := &models.ReelFeed{Items: items}
	if len(items) > limit {
		feed.Items = items[:limit]
		last := feed.Items[len(feed.Items)-1]
		if last.PublishedAt != nil {
			next := encodeReelCursor(*last.PublishedAt, last.ID)
			feed.NextCursor = &next
		}
	}
	return feed, nil
}

// resolveProductID validates that a tagged product exists in the reel's shop.
func (s *ReelService) resolveProductID(ctx context.Context, raw *string, shopID string) (*uuid.UUID, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	pid, err := uuid.Parse(strings.TrimSpace(*raw))
	if err != nil {
		return nil, ErrInvalidReel
	}
	ok, err := s.reels.ProductBelongsToShop(ctx, pid.String(), shopID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrReelProduct
	}
	return &pid, nil
}

// buildAssets converts uploaded file references into media.media_assets rows.
func (s *ReelService) buildAssets(sellerID uuid.UUID, in []ReelMediaInput) ([]models.MediaAsset, error) {
	if len(in) == 0 || len(in) > maxReelMediaItems {
		return nil, ErrInvalidReel
	}
	assets := make([]models.MediaAsset, 0, len(in))
	for _, item := range in {
		asset, err := s.buildAsset(sellerID, item)
		if err != nil {
			return nil, err
		}
		assets = append(assets, *asset)
	}
	return assets, nil
}

// buildThumbnail builds the optional cover image asset; nil input means "keep existing".
func (s *ReelService) buildThumbnail(sellerID uuid.UUID, in *ReelMediaInput) (*models.MediaAsset, error) {
	if in == nil {
		return nil, nil
	}
	asset, err := s.buildAsset(sellerID, *in)
	if err != nil {
		return nil, err
	}
	if asset.AssetType != "image" {
		return nil, ErrInvalidReel
	}
	return asset, nil
}

func (s *ReelService) buildAsset(sellerID uuid.UUID, in ReelMediaInput) (*models.MediaAsset, error) {
	objectPath := strings.TrimSpace(in.ObjectPath)
	mimeType := strings.ToLower(strings.TrimSpace(in.MimeType))
	if objectPath == "" || mimeType == "" || in.SizeBytes < 0 {
		return nil, ErrInvalidReel
	}

	assetType, ok := reelAssetTypeFromMime(mimeType)
	if !ok {
		return nil, ErrInvalidReel
	}
	metadata := in.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}

	owner := sellerID
	asset := &models.MediaAsset{
		OwnerType:  "seller",
		OwnerID:    &owner,
		AssetType:  assetType,
		Bucket:     s.bucket,
		ObjectPath: objectPath,
		MimeType:   mimeType,
		SizeBytes:  in.SizeBytes,
		// Seller-uploaded reel files are served straight from the bucket.
		ProcessingStatus: "ready",
		ModerationStatus: "approved",
		Metadata:         metadata,
	}
	if strings.HasPrefix(objectPath, "public/") {
		url := s.s3.PublicURL(objectPath)
		asset.CDNURL = &url
	}
	return asset, nil
}

// reelAssetTypeFromMime maps a MIME type to a media_assets.asset_type value.
func reelAssetTypeFromMime(mimeType string) (string, bool) {
	switch {
	case strings.HasPrefix(mimeType, "video/"):
		return "video", true
	case strings.HasPrefix(mimeType, "image/"):
		return "image", true
	default:
		return "", false
	}
}

// reelTypeFromAssets marks the reel as a video when any file is a video.
func reelTypeFromAssets(assets []models.MediaAsset) string {
	for _, a := range assets {
		if a.AssetType == "video" {
			return "video"
		}
	}
	return "photo"
}

// applyReelVisibilityStatus validates visibility/status and stamps published_at on publish.
func applyReelVisibilityStatus(reel *models.Reel, visibility, status string, existingPublishedAt *time.Time) error {
	visibility = strings.ToLower(strings.TrimSpace(visibility))
	status = strings.ToLower(strings.TrimSpace(status))
	if visibility == "" {
		visibility = "public"
	}
	if status == "" {
		status = "draft"
	}
	switch visibility {
	case "public", "private":
	default:
		return ErrInvalidReel
	}
	switch status {
	case "draft", "published", "archived":
	default:
		return ErrInvalidReel
	}

	reel.Visibility = visibility
	reel.Status = status
	reel.PublishedAt = existingPublishedAt
	if status == "published" && reel.PublishedAt == nil {
		now := time.Now().UTC()
		reel.PublishedAt = &now
	}
	return nil
}

func applyReelDuration(reel *models.Reel, durationMs *int) error {
	if durationMs == nil {
		reel.DurationMs = nil
		return nil
	}
	if *durationMs < 0 {
		return ErrInvalidReel
	}
	reel.DurationMs = durationMs
	return nil
}

func normalizeCaption(caption *string) *string {
	if caption == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*caption)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// normalizeHashtags lowercases tags and strips a leading '#'.
func normalizeHashtags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(tag), "#")))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// encodeReelCursor packs (published_at, id) into an opaque feed cursor.
func encodeReelCursor(publishedAt time.Time, id uuid.UUID) string {
	raw := publishedAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeReelCursor(cursor string) (time.Time, uuid.UUID, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	return at, id, nil
}
