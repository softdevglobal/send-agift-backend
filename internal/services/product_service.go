package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrProductNotFound   = errors.New("product not found")
	ErrProductConflict   = errors.New("product already exists")
	ErrInvalidProduct    = errors.New("invalid product")
	ErrInventoryNotFound = errors.New("inventory not found")
	ErrInvalidInventory  = errors.New("invalid inventory")
)

// maxProductMediaItems caps gallery size (images + videos) on one product.
const maxProductMediaItems = 12

type ProductService struct {
	products *repository.ProductRepository
	sellers  *repository.SellerRepository
	s3       *S3Service
	bucket   string
}

func NewProductService(
	products *repository.ProductRepository,
	sellers *repository.SellerRepository,
	s3 *S3Service,
	bucket string,
) *ProductService {
	return &ProductService{products: products, sellers: sellers, s3: s3, bucket: bucket}
}

// ProductMediaInput is one already-uploaded file (via /media/presign-upload).
type ProductMediaInput struct {
	ObjectPath string          `json:"object_path"` // S3 key from presign-upload
	MimeType   string          `json:"mime_type"`   // image/jpeg, video/mp4, ...
	SizeBytes  int64           `json:"size_bytes"`
	Metadata   json.RawMessage `json:"metadata"` // width, height, duration, etc.
}

type ProductInput struct {
	Name                   string   `json:"name"`
	Slug                   string   `json:"slug"`
	Description            *string  `json:"description"`
	ProductType            string   `json:"product_type"`
	PriceAmount            int      `json:"price_amount"`
	Currency               string   `json:"currency"`
	Status                 string   `json:"status"`
	OccasionTags           []string `json:"occasion_tags"`
	CustomerTypeVisibility string   `json:"customer_type_visibility"`
	PointsDisplayEnabled   bool     `json:"points_display_enabled"`
	PrepMinutes            int      `json:"prep_minutes"`
	ImageURL               *string  `json:"image_url"` // cover; auto-set from first image in media when omitted
	// Parcel matches the seller shipping form (length/width/height/weight).
	Parcel *models.ProductParcel `json:"parcel"`
	// Media is the product gallery (images + videos). On update, nil keeps existing;
	// empty slice clears the gallery; non-empty replaces it.
	Media     []ProductMediaInput `json:"media"`
	Inventory *InventoryInput     `json:"inventory"`
}

type InventoryInput struct {
	AvailableQty      int      `json:"available_qty"`
	ReservedQty       int      `json:"reserved_qty"`
	LowStockThreshold int      `json:"low_stock_threshold"`
	UnavailableDates  []string `json:"unavailable_dates"` // YYYY-MM-DD
}

func (s *ProductService) ListByShop(ctx context.Context, sellerID, shopID string) ([]models.Product, error) {
	if _, err := s.sellers.GetShopByID(ctx, sellerID, shopID); err != nil {
		if errors.Is(err, repository.ErrShopNotFound) {
			return nil, ErrShopNotFound
		}
		return nil, err
	}
	return s.products.ListByShopForSeller(ctx, sellerID, shopID)
}

func (s *ProductService) Get(ctx context.Context, sellerID, productID string) (*models.ProductDetails, error) {
	p, err := s.products.GetByIDForSeller(ctx, sellerID, productID)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	details := &models.ProductDetails{Product: *p}
	inv, err := s.products.GetInventoryByProductID(ctx, productID)
	if err == nil {
		details.Inventory = inv
	} else if !errors.Is(err, repository.ErrInventoryNotFound) {
		return nil, err
	}
	return details, nil
}

func (s *ProductService) Create(ctx context.Context, sellerID, shopID string, in ProductInput) (*models.ProductDetails, error) {
	if _, err := s.sellers.GetShopByID(ctx, sellerID, shopID); err != nil {
		if errors.Is(err, repository.ErrShopNotFound) {
			return nil, ErrShopNotFound
		}
		return nil, err
	}

	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrInvalidProduct
	}
	assets, err := s.buildAssets(sid, in.Media)
	if err != nil {
		return nil, err
	}

	product, err := s.buildProduct(uuid.Nil, in, assets)
	if err != nil {
		return nil, err
	}
	shopUUID, err := uuid.Parse(shopID)
	if err != nil {
		return nil, ErrShopNotFound
	}
	product.ShopID = shopUUID

	if err := s.products.Create(ctx, product, assets); err != nil {
		if errors.Is(err, repository.ErrProductDuplicate) {
			return nil, ErrProductConflict
		}
		return nil, err
	}

	invIn := in.Inventory
	if invIn == nil {
		invIn = &InventoryInput{}
	}
	inv, err := s.buildInventory(product.ID, *invIn)
	if err != nil {
		return nil, err
	}
	if err := s.products.CreateInventory(ctx, inv); err != nil {
		return nil, err
	}

	// Reload so Media (with asset ids) is populated.
	created, err := s.products.GetByIDForSeller(ctx, sellerID, product.ID.String())
	if err != nil {
		return &models.ProductDetails{Product: *product, Inventory: inv}, nil
	}
	return &models.ProductDetails{Product: *created, Inventory: inv}, nil
}

func (s *ProductService) Update(ctx context.Context, sellerID, productID string, in ProductInput) (*models.Product, error) {
	existing, err := s.products.GetByIDForSeller(ctx, sellerID, productID)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}

	replaceMedia := in.Media != nil
	var assets []models.MediaAsset
	if replaceMedia {
		sid, parseErr := uuid.Parse(sellerID)
		if parseErr != nil {
			return nil, ErrInvalidProduct
		}
		assets, err = s.buildAssets(sid, in.Media)
		if err != nil {
			return nil, err
		}
	}

	product, err := s.buildProduct(existing.ID, in, assets)
	if err != nil {
		return nil, err
	}
	product.ShopID = existing.ShopID
	product.CreatedAt = existing.CreatedAt
	if !replaceMedia {
		// Keep existing cover when media is not being replaced and body omitted image_url.
		if in.ImageURL == nil {
			product.ImageURL = existing.ImageURL
		}
	}

	if err := s.products.Update(ctx, product, assets, replaceMedia); err != nil {
		if errors.Is(err, repository.ErrProductDuplicate) {
			return nil, ErrProductConflict
		}
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	return s.products.GetByIDForSeller(ctx, sellerID, productID)
}

func (s *ProductService) Delete(ctx context.Context, sellerID, productID string) error {
	if _, err := s.products.GetByIDForSeller(ctx, sellerID, productID); err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return ErrProductNotFound
		}
		return err
	}
	return s.products.Delete(ctx, productID)
}

func (s *ProductService) GetInventory(ctx context.Context, sellerID, productID string) (*models.Inventory, error) {
	if _, err := s.products.GetByIDForSeller(ctx, sellerID, productID); err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	inv, err := s.products.GetInventoryByProductID(ctx, productID)
	if err != nil {
		if errors.Is(err, repository.ErrInventoryNotFound) {
			return nil, ErrInventoryNotFound
		}
		return nil, err
	}
	return inv, nil
}

func (s *ProductService) UpdateInventory(ctx context.Context, sellerID, productID string, in InventoryInput) (*models.Inventory, error) {
	p, err := s.products.GetByIDForSeller(ctx, sellerID, productID)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	inv, err := s.buildInventory(p.ID, in)
	if err != nil {
		return nil, err
	}
	if err := s.products.UpsertInventory(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *ProductService) buildProduct(id uuid.UUID, in ProductInput, assets []models.MediaAsset) (*models.Product, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	in.ProductType = strings.TrimSpace(in.ProductType)
	in.Status = strings.TrimSpace(in.Status)
	in.CustomerTypeVisibility = strings.TrimSpace(in.CustomerTypeVisibility)

	if in.Name == "" || in.Currency == "" {
		return nil, ErrInvalidProduct
	}
	if in.PriceAmount < 0 || in.PrepMinutes < 0 {
		return nil, ErrInvalidProduct
	}
	if _, ok := knownCurrencies[in.Currency]; !ok {
		return nil, ErrInvalidCurrency
	}
	if in.ProductType == "" {
		in.ProductType = "gift"
	}
	if in.Status == "" {
		in.Status = "draft"
	}
	switch in.Status {
	case "draft", "published", "paused", "rejected":
	default:
		return nil, ErrInvalidProduct
	}
	if in.CustomerTypeVisibility == "" {
		in.CustomerTypeVisibility = "both"
	}
	switch in.CustomerTypeVisibility {
	case "personal", "corporate", "both":
	default:
		return nil, ErrInvalidProduct
	}
	if in.OccasionTags == nil {
		in.OccasionTags = []string{}
	}

	var parcel *models.ProductParcel
	if in.Parcel != nil {
		p := *in.Parcel
		p.Length = strings.TrimSpace(p.Length)
		p.Width = strings.TrimSpace(p.Width)
		p.Height = strings.TrimSpace(p.Height)
		p.DistanceUnit = strings.ToLower(strings.TrimSpace(p.DistanceUnit))
		p.Weight = strings.TrimSpace(p.Weight)
		p.MassUnit = strings.ToLower(strings.TrimSpace(p.MassUnit))
		if p.Length != "" || p.Width != "" || p.Height != "" || p.Weight != "" {
			if p.DistanceUnit == "" {
				p.DistanceUnit = "cm"
			}
			if p.MassUnit == "" {
				p.MassUnit = "kg"
			}
			switch p.DistanceUnit {
			case "cm", "in":
			default:
				return nil, ErrInvalidProduct
			}
			switch p.MassUnit {
			case "kg", "lb":
			default:
				return nil, ErrInvalidProduct
			}
			parcel = &p
		}
	}

	imageURL := in.ImageURL
	// When gallery is provided and cover omitted, use first image CDN URL as cover.
	if imageURL == nil || strings.TrimSpace(ptrString(imageURL)) == "" {
		if cover := coverURLFromAssets(assets); cover != "" {
			imageURL = &cover
		}
	}

	return &models.Product{
		ID:                     id,
		Name:                   in.Name,
		Slug:                   slugOrFromName(in.Slug, in.Name),
		Description:            in.Description,
		ProductType:            in.ProductType,
		PriceAmount:            in.PriceAmount,
		Currency:               in.Currency,
		Status:                 in.Status,
		OccasionTags:           in.OccasionTags,
		CustomerTypeVisibility: in.CustomerTypeVisibility,
		PointsDisplayEnabled:   in.PointsDisplayEnabled,
		PrepMinutes:            in.PrepMinutes,
		ImageURL:               imageURL,
		Parcel:                 parcel,
	}, nil
}

func ptrString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func coverURLFromAssets(assets []models.MediaAsset) string {
	for _, a := range assets {
		if a.AssetType != "image" {
			continue
		}
		if a.CDNURL != nil && strings.TrimSpace(*a.CDNURL) != "" {
			return strings.TrimSpace(*a.CDNURL)
		}
	}
	return ""
}

// buildAssets converts uploaded file refs into media.media_assets rows.
// Empty input is allowed (product can still use image_url only).
func (s *ProductService) buildAssets(sellerID uuid.UUID, in []ProductMediaInput) ([]models.MediaAsset, error) {
	if in == nil {
		return nil, nil
	}
	if len(in) > maxProductMediaItems {
		return nil, ErrInvalidProduct
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

func (s *ProductService) buildAsset(sellerID uuid.UUID, in ProductMediaInput) (*models.MediaAsset, error) {
	objectPath := strings.TrimSpace(in.ObjectPath)
	mimeType := strings.ToLower(strings.TrimSpace(in.MimeType))
	if objectPath == "" || mimeType == "" || in.SizeBytes < 0 {
		return nil, ErrInvalidProduct
	}
	assetType, ok := productAssetTypeFromMime(mimeType)
	if !ok {
		return nil, ErrInvalidProduct
	}
	metadata := in.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	owner := sellerID
	asset := &models.MediaAsset{
		OwnerType:        "seller",
		OwnerID:          &owner,
		AssetType:        assetType,
		Bucket:           s.bucket,
		ObjectPath:       objectPath,
		MimeType:         mimeType,
		SizeBytes:        in.SizeBytes,
		ProcessingStatus: "ready",
		ModerationStatus: "approved",
		Metadata:         metadata,
	}
	if s.s3 != nil && strings.HasPrefix(objectPath, "public/") {
		url := s.s3.PublicURL(objectPath)
		asset.CDNURL = &url
	}
	return asset, nil
}

func productAssetTypeFromMime(mimeType string) (string, bool) {
	switch {
	case strings.HasPrefix(mimeType, "video/"):
		return "video", true
	case strings.HasPrefix(mimeType, "image/"):
		return "image", true
	default:
		return "", false
	}
}

func (s *ProductService) buildInventory(productID uuid.UUID, in InventoryInput) (*models.Inventory, error) {
	if in.AvailableQty < 0 || in.ReservedQty < 0 || in.LowStockThreshold < 0 {
		return nil, ErrInvalidInventory
	}
	dates := make([]time.Time, 0, len(in.UnavailableDates))
	for _, raw := range in.UnavailableDates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return nil, ErrInvalidInventory
		}
		dates = append(dates, t)
	}
	return &models.Inventory{
		ProductID:         productID,
		AvailableQty:      in.AvailableQty,
		ReservedQty:       in.ReservedQty,
		LowStockThreshold: in.LowStockThreshold,
		UnavailableDates:  dates,
	}, nil
}
