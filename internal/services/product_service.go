package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrProductNotFound  = errors.New("product not found")
	ErrProductConflict  = errors.New("product already exists")
	ErrInvalidProduct   = errors.New("invalid product")
	ErrInventoryNotFound = errors.New("inventory not found")
	ErrInvalidInventory = errors.New("invalid inventory")
)

type ProductService struct {
	products *repository.ProductRepository
	sellers  *repository.SellerRepository
}

func NewProductService(products *repository.ProductRepository, sellers *repository.SellerRepository) *ProductService {
	return &ProductService{products: products, sellers: sellers}
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
	ImageURL               *string  `json:"image_url"`
	Inventory              *InventoryInput `json:"inventory"`
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

	product, err := s.buildProduct(uuid.Nil, in)
	if err != nil {
		return nil, err
	}
	sid, err := uuid.Parse(shopID)
	if err != nil {
		return nil, ErrShopNotFound
	}
	product.ShopID = sid

	if err := s.products.Create(ctx, product); err != nil {
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

	return &models.ProductDetails{Product: *product, Inventory: inv}, nil
}

func (s *ProductService) Update(ctx context.Context, sellerID, productID string, in ProductInput) (*models.Product, error) {
	existing, err := s.products.GetByIDForSeller(ctx, sellerID, productID)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}

	product, err := s.buildProduct(existing.ID, in)
	if err != nil {
		return nil, err
	}
	product.ShopID = existing.ShopID
	product.CreatedAt = existing.CreatedAt

	if err := s.products.Update(ctx, product); err != nil {
		if errors.Is(err, repository.ErrProductDuplicate) {
			return nil, ErrProductConflict
		}
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	return product, nil
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

func (s *ProductService) buildProduct(id uuid.UUID, in ProductInput) (*models.Product, error) {
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
		ImageURL:               in.ImageURL,
	}, nil
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
