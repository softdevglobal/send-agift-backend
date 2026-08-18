package services

import (
	"context" // context for the service
	"errors" // errors for the service
	"strings" // strings for the service

	"myapp/internal/models" // models for the service
	"myapp/internal/repository" // repository for the service
)

var ErrCountryNotFound = errors.New("country not found") // error for the service
var ErrCountryConflict = errors.New("country already exists") // error for the service
var ErrInvalidCurrency = errors.New("invalid currency") // error for the service
var ErrInvalidISOCode = errors.New("invalid iso code") // error for the service	

// knownCurrencies is a small ISO 4217 allow-list used for country defaults.
var knownCurrencies = map[string]struct{}{
	"USD": {}, "EUR": {}, "GBP": {}, "INR": {}, "LKR": {}, "AUD": {}, "CAD": {},
	"SGD": {}, "AED": {}, "JPY": {}, "CNY": {}, "CHF": {}, "NZD": {}, "HKD": {},
	"MYR": {}, "THB": {}, "IDR": {}, "PHP": {}, "PKR": {}, "BDT": {}, "SAR": {},
}

type CountryService struct {
	countries *repository.CountryRepository // repository for the service
}

func NewCountryService(countries *repository.CountryRepository) *CountryService { // NewCountryService is a function that creates a new CountryService
	return &CountryService{countries: countries}
}

type CountryInput struct {
	ISOCode         string	// ISO code for the country
	Name            string	// Name for the country
	DefaultCurrency string	// Default currency for the country
	DefaultTimezone string	// Default timezone for the country
	Status          string	// Status for the country
}

func (s *CountryService) List(ctx context.Context) ([]models.Country, error) { // List is a function that lists all the countries
	return s.countries.List(ctx)
}

func (s *CountryService) GetByID(ctx context.Context, id string) (*models.Country, error) { // GetByID is a function that gets a country by ID
	c, err := s.countries.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrCountryNotFound // return an error if the country is not found
		}
		return nil, err // return an error if the country is not found
	}
	return c, nil // return the country
}

func (s *CountryService) Create(ctx context.Context, in CountryInput) (*models.Country, error) {
	in = normalizeCountryInput(in)
	if err := validateCountryInput(in); err != nil {
		return nil, err
	}

	c := &models.Country{ // create a new country
		ISOCode:         in.ISOCode,
		Name:            in.Name,
		DefaultCurrency: in.DefaultCurrency,
		DefaultTimezone: in.DefaultTimezone,
		Status:          in.Status,
	}
	if err := s.countries.Create(ctx, c); err != nil {
		if errors.Is(err, repository.ErrCountryDuplicate) {
			return nil, ErrCountryConflict // return an error if the country already exists
		}
		return nil, err // return an error if the country is not created
	}
	return c, nil // return the country
}

func (s *CountryService) Update(ctx context.Context, id string, in CountryInput) (*models.Country, error) { // Update is a function that updates a country
	in = normalizeCountryInput(in)
	if err := validateCountryInput(in); err != nil {
		return nil, err // return an error if the input is invalid
	}

	existing, err := s.countries.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrCountryNotFound // return an error if the country is not found
		}
		return nil, err // return an error if the country is not found
	}

	existing.ISOCode = in.ISOCode
	existing.Name = in.Name
	existing.DefaultCurrency = in.DefaultCurrency
	existing.DefaultTimezone = in.DefaultTimezone
	existing.Status = in.Status

	if err := s.countries.Update(ctx, existing); err != nil {
		switch {
		case errors.Is(err, repository.ErrCountryNotFound):
			return nil, ErrCountryNotFound // return an error if the country is not found
		case errors.Is(err, repository.ErrCountryDuplicate):
			return nil, ErrCountryConflict // return an error if the country already exists
		default:
			return nil, err // return an error if the country is not updated
		}
	}
	return existing, nil // return the country	
}

func (s *CountryService) Delete(ctx context.Context, id string) error { // Delete is a function that deletes a country
	err := s.countries.Delete(ctx, id)
	if errors.Is(err, repository.ErrCountryNotFound) {
		return ErrCountryNotFound // return an error if the country is not found
	}
	return err // return an error if the country is not deleted
}

func normalizeCountryInput(in CountryInput) CountryInput { // normalizeCountryInput is a function that normalizes the country input
	in.ISOCode = strings.ToUpper(strings.TrimSpace(in.ISOCode))
	in.Name = strings.TrimSpace(in.Name)
	in.DefaultCurrency = strings.ToUpper(strings.TrimSpace(in.DefaultCurrency))
	in.DefaultTimezone = strings.TrimSpace(in.DefaultTimezone)
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	if in.Status == "" {
		in.Status = "active"
	}
	return in // return the normalized country input
}

func validateCountryInput(in CountryInput) error {
	if in.ISOCode == "" || in.Name == "" || in.DefaultCurrency == "" || in.DefaultTimezone == "" {
		return ErrInvalidInput // return an error if the input is invalid
	}
	if len(in.ISOCode) != 2 {
		return ErrInvalidISOCode // return an error if the ISO code is invalid
	}
	if _, ok := knownCurrencies[in.DefaultCurrency]; !ok {
		return ErrInvalidCurrency // return an error if the currency is invalid
	}
	return nil // return nil if the input is valid
}
