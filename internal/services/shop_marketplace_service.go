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

func (s *MarketplaceService) ListPublishedProductsByShop(ctx context.Context, shopID, customerType string) ([]models.Product, error) {
	customerType = strings.TrimSpace(strings.ToLower(customerType))
	if customerType == "" {
		customerType = "personal"
	}
	if customerType != "personal" && customerType != "corporate" {
		return nil, ErrInvalidCustomerType
	}
	return s.products.ListPublishedByShopForCustomerType(ctx, shopID, customerType)
}

