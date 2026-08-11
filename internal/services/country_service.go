package services

import (
	"context"
	"errors"
	"strings"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var ErrCountryNotFound = errors.New("country not found")
var ErrCountryConflict = errors.New("country already exists")
var ErrInvalidCurrency = errors.New("invalid currency")
var ErrInvalidISOCode = errors.New("invalid iso code")

// knownCurrencies is a small ISO 4217 allow-list used for country defaults.
var knownCurrencies = map[string]struct{}{
	"USD": {}, "EUR": {}, "GBP": {}, "INR": {}, "LKR": {}, "AUD": {}, "CAD": {},
	"SGD": {}, "AED": {}, "JPY": {}, "CNY": {}, "CHF": {}, "NZD": {}, "HKD": {},
	"MYR": {}, "THB": {}, "IDR": {}, "PHP": {}, "PKR": {}, "BDT": {}, "SAR": {},
}

type CountryService struct {
	countries *repository.CountryRepository
}

func NewCountryService(countries *repository.CountryRepository) *CountryService {
	return &CountryService{countries: countries}
}

type CountryInput struct {
	ISOCode         string
	Name            string
	DefaultCurrency string
	DefaultTimezone string
	Status          string
}

func (s *CountryService) List(ctx context.Context) ([]models.Country, error) {
	return s.countries.List(ctx)
}

func (s *CountryService) GetByID(ctx context.Context, id string) (*models.Country, error) {
	c, err := s.countries.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrCountryNotFound
		}
		return nil, err
	}
	return c, nil
}

func (s *CountryService) Create(ctx context.Context, in CountryInput) (*models.Country, error) {
	in = normalizeCountryInput(in)
	if err := validateCountryInput(in); err != nil {
		return nil, err
	}

	c := &models.Country{
		ISOCode:         in.ISOCode,
		Name:            in.Name,
		DefaultCurrency: in.DefaultCurrency,
		DefaultTimezone: in.DefaultTimezone,
		Status:          in.Status,
	}
	if err := s.countries.Create(ctx, c); err != nil {
		if errors.Is(err, repository.ErrCountryDuplicate) {
			return nil, ErrCountryConflict
		}
		return nil, err
	}
	return c, nil
}

func (s *CountryService) Update(ctx context.Context, id string, in CountryInput) (*models.Country, error) {
	in = normalizeCountryInput(in)
	if err := validateCountryInput(in); err != nil {
		return nil, err
	}

	existing, err := s.countries.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrCountryNotFound
		}
		return nil, err
	}

	existing.ISOCode = in.ISOCode
	existing.Name = in.Name
	existing.DefaultCurrency = in.DefaultCurrency
	existing.DefaultTimezone = in.DefaultTimezone
	existing.Status = in.Status

	if err := s.countries.Update(ctx, existing); err != nil {
		switch {
		case errors.Is(err, repository.ErrCountryNotFound):
			return nil, ErrCountryNotFound
		case errors.Is(err, repository.ErrCountryDuplicate):
			return nil, ErrCountryConflict
		default:
			return nil, err
		}
	}
	return existing, nil
}

func (s *CountryService) Delete(ctx context.Context, id string) error {
	err := s.countries.Delete(ctx, id)
	if errors.Is(err, repository.ErrCountryNotFound) {
		return ErrCountryNotFound
	}
	return err
}

func normalizeCountryInput(in CountryInput) CountryInput {
	in.ISOCode = strings.ToUpper(strings.TrimSpace(in.ISOCode))
	in.Name = strings.TrimSpace(in.Name)
	in.DefaultCurrency = strings.ToUpper(strings.TrimSpace(in.DefaultCurrency))
	in.DefaultTimezone = strings.TrimSpace(in.DefaultTimezone)
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	if in.Status == "" {
		in.Status = "active"
	}
	return in
}

func validateCountryInput(in CountryInput) error {
	if in.ISOCode == "" || in.Name == "" || in.DefaultCurrency == "" || in.DefaultTimezone == "" {
		return ErrInvalidInput
	}
	if len(in.ISOCode) != 2 {
		return ErrInvalidISOCode
	}
	if _, ok := knownCurrencies[in.DefaultCurrency]; !ok {
		return ErrInvalidCurrency
	}
	return nil
}
