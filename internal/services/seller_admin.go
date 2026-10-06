package services

import (
	"context"
	"errors"
	"strings"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	// ErrSellerSuspended is a right password for a seller an admin has turned off.
	ErrSellerSuspended = errors.New("this seller account is suspended")
	// ErrSellerAccountStatus is a status other than active or suspended.
	ErrSellerAccountStatus = errors.New("status must be active or suspended")
	// ErrSellerClosed is a seller who deleted their own account.
	ErrSellerClosed = errors.New("a closed seller account stays closed")
	// ErrSellerVerificationStatus is a value other than verified, unverified, or rejected.
	ErrSellerVerificationStatus = errors.New("verification must be verified, unverified, or rejected")
)

// AccountStatus is the seller row's status, used to refuse a suspended session.
func (s *SellerService) AccountStatus(ctx context.Context, id string) (string, error) {
	seller, err := s.sellers.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return "", ErrSellerNotFound
		}
		return "", err
	}
	return seller.Status, nil
}

// AdminListSellers returns a page of sellers. status empty includes every account.
func (s *SellerService) AdminListSellers(ctx context.Context, status, query string, limit, offset int) ([]models.AdminSellerSummary, int, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "", "active", "suspended", "deleted":
	default:
		return nil, 0, ErrSellerAccountStatus
	}
	query = strings.TrimSpace(query)
	if len(query) > 100 {
		query = query[:100]
	}
	limit, offset = clampAdminPage(limit, offset)
	return s.sellers.ListForAdmin(ctx, status, query, limit, offset)
}

// AdminSetSellerStatus turns a seller on (active) or off (suspended).
func (s *SellerService) AdminSetSellerStatus(ctx context.Context, id, status string) (*models.AdminSellerSummary, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "active" && status != "suspended" {
		return nil, ErrSellerAccountStatus
	}
	if err := s.sellers.SetAccountStatus(ctx, id, status); err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		if errors.Is(err, repository.ErrSellerClosed) {
			return nil, ErrSellerClosed
		}
		return nil, err
	}
	item, err := s.sellers.GetForAdmin(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		return nil, err
	}
	return item, nil
}

// AdminSetSellerVerification marks the business verified, unverified, or rejected.
func (s *SellerService) AdminSetSellerVerification(ctx context.Context, id, status string) (*models.AdminSellerSummary, error) {
	status, err := normalizeVerificationStatus(status)
	if err != nil {
		return nil, err
	}
	if err := s.sellers.SetVerificationStatus(ctx, id, status); err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		if errors.Is(err, repository.ErrSellerClosed) {
			return nil, ErrSellerClosed
		}
		return nil, err
	}
	item, err := s.sellers.GetForAdmin(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		return nil, err
	}
	return item, nil
}

func normalizeVerificationStatus(status string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "verified", "unverified", "rejected":
		return strings.ToLower(strings.TrimSpace(status)), nil
	default:
		return "", ErrSellerVerificationStatus
	}
}

// UseAdminReads wires the catalogs an admin opens from a seller row.
func (s *SellerService) UseAdminReads(products *repository.ProductRepository, orders *repository.OrderRepository) {
	s.products = products
	s.orders = orders
}

// AdminGetSeller returns the full seller record: profile, shops, gifts, and order lines.
func (s *SellerService) AdminGetSeller(ctx context.Context, id string) (*models.AdminSellerRecord, error) {
	details, err := s.GetDetails(ctx, id)
	if err != nil {
		return nil, err
	}
	if details.Addresses == nil {
		details.Addresses = []models.SellerAddress{}
	}
	if details.Shops == nil {
		details.Shops = []models.Shop{}
	}
	if details.Identifiers == nil {
		details.Identifiers = []models.SellerIdentifier{}
	}
	if details.TaxRegistrations == nil {
		details.TaxRegistrations = []models.SellerTaxRegistration{}
	}
	country, err := s.countries.GetByID(ctx, details.CountryID.String())
	if err != nil {
		return nil, err
	}
	products := []models.Product{}
	if s.products != nil {
		products, err = s.products.ListBySeller(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	orderLines := []models.SellerOrderItemSummary{}
	if s.orders != nil {
		orderLines, err = s.orders.ListItemsBySeller(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	return &models.AdminSellerRecord{
		SellerDetails: *details,
		CountryName:   country.Name,
		Products:      products,
		Orders:        orderLines,
	}, nil
}

func clampAdminPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
