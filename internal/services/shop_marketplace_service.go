package services

import (
	"context"
	"errors"
	"strings"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrInvalidCustomerType = errors.New("invalid customer_type")
)

// MarketplaceService exposes read-only, customer-facing marketplace browsing.
// It is public (no JWT required).
type MarketplaceService struct {
	sellers  *repository.SellerRepository
	products *repository.ProductRepository
}

func NewMarketplaceService(
	sellers *repository.SellerRepository,
	products *repository.ProductRepository,
) *MarketplaceService {
	return &MarketplaceService{
		sellers:  sellers,
		products: products,
	}
}

func (s *MarketplaceService) ListActiveShops(ctx context.Context) ([]models.Shop, error) {
	return s.sellers.ListActiveShops(ctx)
}

// GetActiveShop returns one active shop for a public shop page.
func (s *MarketplaceService) GetActiveShop(ctx context.Context, shopID string) (*models.Shop, error) {
	shop, err := s.sellers.GetActiveShopByID(ctx, shopID)
	if err != nil {
		if errors.Is(err, repository.ErrShopNotFound) {
			return nil, ErrShopNotFound
		}
		return nil, err
	}
	return shop, nil
}

func (s *MarketplaceService) ListPublishedProductsByShop(ctx context.Context, shopID, customerType string) ([]models.Product, error) {
	customerType, err := normalizeCustomerType(customerType)
	if err != nil {
		return nil, err
	}
	return s.products.ListPublishedByShopForCustomerType(ctx, shopID, customerType)
}

// GetPublishedProduct returns one published product plus its shop, for a public product page.
func (s *MarketplaceService) GetPublishedProduct(ctx context.Context, productID, customerType string) (*models.PublicProduct, error) {
	customerType, err := normalizeCustomerType(customerType)
	if err != nil {
		return nil, err
	}
	product, err := s.products.GetPublishedByIDForCustomerType(ctx, productID, customerType)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	return product, nil
}

// normalizeCustomerType defaults the audience filter to 'personal'.
func normalizeCustomerType(customerType string) (string, error) {
	customerType = strings.TrimSpace(strings.ToLower(customerType))
	if customerType == "" {
		return "personal", nil
	}
	if customerType != "personal" && customerType != "corporate" {
		return "", ErrInvalidCustomerType
	}
	return customerType, nil
}

