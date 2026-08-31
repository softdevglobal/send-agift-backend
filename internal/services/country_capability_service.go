package services

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrCountryCapabilityNotFound    = errors.New("country capability not found")
	ErrCountryCapabilityConflict    = errors.New("country capability already exists")
	ErrCustomerRegistrationDisabled = errors.New("customer registration disabled")
	ErrSellerRegistrationDisabled   = errors.New("seller registration disabled")
)

type CountryCapabilityService struct {
	capabilities *repository.CountryCapabilityRepository
	countries    *repository.CountryRepository
}

func NewCountryCapabilityService(
	capabilities *repository.CountryCapabilityRepository,
	countries *repository.CountryRepository,
) *CountryCapabilityService {
	return &CountryCapabilityService{
		capabilities: capabilities,
		countries:    countries,
	}
}

type CountryCapabilityInput struct {
	CustomerRegistrationEnabled  bool
	SellerRegistrationEnabled    bool
	SellerPayoutsEnabled         bool
	DomesticDeliveryEnabled      bool
	InternationalDeliveryEnabled bool
	MembershipsEnabled           bool
	PointsEarningEnabled         bool
	PointsUsageEnabled           bool
	SkillCompetitionsEnabled     bool
	AppStoreAvailable            bool
}

func (s *CountryCapabilityService) List(ctx context.Context) ([]models.CountryCapabilityDetails, error) {
	return s.capabilities.ListWithCountries(ctx)
}

func (s *CountryCapabilityService) GetByCountryID(ctx context.Context, countryID string) (*models.CountryCapabilityDetails, error) {
	if _, err := s.validateCountryID(ctx, countryID); err != nil {
		return nil, err
	}
	item, err := s.capabilities.GetDetailsByCountryID(ctx, countryID)
	if err != nil {
		if errors.Is(err, repository.ErrCountryCapabilityNotFound) {
			return nil, ErrCountryCapabilityNotFound
		}
		return nil, err
	}
	return item, nil
}

func (s *CountryCapabilityService) Create(ctx context.Context, countryID string, in CountryCapabilityInput) (*models.CountryCapability, error) {
	parsedCountryID, err := s.validateCountryID(ctx, countryID)
	if err != nil {
		return nil, err
	}

	item := &models.CountryCapability{
		CountryID:                    parsedCountryID,
		CustomerRegistrationEnabled:  in.CustomerRegistrationEnabled,
		SellerRegistrationEnabled:    in.SellerRegistrationEnabled,
		SellerPayoutsEnabled:         in.SellerPayoutsEnabled,
		DomesticDeliveryEnabled:      in.DomesticDeliveryEnabled,
		InternationalDeliveryEnabled: in.InternationalDeliveryEnabled,
		MembershipsEnabled:           in.MembershipsEnabled,
		PointsEarningEnabled:         in.PointsEarningEnabled,
		PointsUsageEnabled:           in.PointsUsageEnabled,
		SkillCompetitionsEnabled:     in.SkillCompetitionsEnabled,
		AppStoreAvailable:            in.AppStoreAvailable,
		RuleVersion:                  1,
	}
	if err := s.capabilities.Create(ctx, item); err != nil {
		switch {
		case errors.Is(err, repository.ErrCountryCapabilityDuplicate):
			return nil, ErrCountryCapabilityConflict
		case errors.Is(err, repository.ErrCountryNotFound):
			return nil, ErrCountryNotFound
		default:
			return nil, err
		}
	}
	return item, nil
}

func (s *CountryCapabilityService) Update(ctx context.Context, countryID string, in CountryCapabilityInput) (*models.CountryCapability, error) {
	if _, err := s.validateCountryID(ctx, countryID); err != nil {
		return nil, err
	}

	existing, err := s.capabilities.GetByCountryID(ctx, countryID)
	if err != nil {
		if errors.Is(err, repository.ErrCountryCapabilityNotFound) {
			return nil, ErrCountryCapabilityNotFound
		}
		return nil, err
	}

	existing.CustomerRegistrationEnabled = in.CustomerRegistrationEnabled
	existing.SellerRegistrationEnabled = in.SellerRegistrationEnabled
	existing.SellerPayoutsEnabled = in.SellerPayoutsEnabled
	existing.DomesticDeliveryEnabled = in.DomesticDeliveryEnabled
	existing.InternationalDeliveryEnabled = in.InternationalDeliveryEnabled
	existing.MembershipsEnabled = in.MembershipsEnabled
	existing.PointsEarningEnabled = in.PointsEarningEnabled
	existing.PointsUsageEnabled = in.PointsUsageEnabled
	existing.SkillCompetitionsEnabled = in.SkillCompetitionsEnabled
	existing.AppStoreAvailable = in.AppStoreAvailable
	existing.RuleVersion++

	if err := s.capabilities.Update(ctx, existing); err != nil {
		if errors.Is(err, repository.ErrCountryCapabilityNotFound) {
			return nil, ErrCountryCapabilityNotFound
		}
		return nil, err
	}
	return existing, nil
}

func (s *CountryCapabilityService) Delete(ctx context.Context, countryID string) error {
	if _, err := s.validateCountryID(ctx, countryID); err != nil {
		return err
	}
	err := s.capabilities.DeleteByCountryID(ctx, countryID)
	if errors.Is(err, repository.ErrCountryCapabilityNotFound) {
		return ErrCountryCapabilityNotFound
	}
	return err
}

func (s *CountryCapabilityService) validateCountryID(ctx context.Context, countryID string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(countryID))
	if err != nil {
		return uuid.Nil, ErrInvalidInput
	}
	if _, err := s.countries.GetByID(ctx, parsed.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return uuid.Nil, ErrCountryNotFound
		}
		return uuid.Nil, err
	}
	return parsed, nil
}

func (s *CountryCapabilityService) EnsureCustomerRegistrationAllowed(ctx context.Context, countryID string) error {
	cap, err := s.capabilities.GetByCountryID(ctx, countryID)
	if errors.Is(err, repository.ErrCountryCapabilityNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !cap.CustomerRegistrationEnabled {
		return ErrCustomerRegistrationDisabled
	}
	return nil
}

func (s *CountryCapabilityService) EnsureSellerRegistrationAllowed(ctx context.Context, countryID string) error {
	cap, err := s.capabilities.GetByCountryID(ctx, countryID)
	if errors.Is(err, repository.ErrCountryCapabilityNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !cap.SellerRegistrationEnabled {
		return ErrSellerRegistrationDisabled
	}
	return nil
}
