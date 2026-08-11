package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

var (
	ErrCustomerNotFound  = errors.New("customer not found")
	ErrCustomerConflict  = errors.New("customer already exists")
	ErrAddressNotFound   = errors.New("address not found")
	ErrInvalidCountry    = errors.New("invalid country")
	ErrInvalidAddress    = errors.New("invalid address")
)

type CustomerService struct {
	customers *repository.CustomerRepository
	countries *repository.CountryRepository
	jwtSecret string
	jwtExpiry time.Duration
}

func NewCustomerService(
	customers *repository.CustomerRepository,
	countries *repository.CountryRepository,
	jwtSecret string,
	jwtExpiry time.Duration,
) *CustomerService {
	return &CustomerService{customers: customers, countries: countries, jwtSecret: jwtSecret, jwtExpiry: jwtExpiry}
}

type CustomerRegisterInput struct {
	CountryID    string
	Email        string
	Password     string
	Phone        *string
	DisplayName  *string
	CustomerType string
	DateOfBirth  string
	Addresses    []AddressInput
}

type CustomerUpdateInput struct {
	CountryID    string
	Phone        *string
	DisplayName  *string
	CustomerType string
	DateOfBirth  string
	Status       string
}

type AddressInput struct {
	CountryID   string   `json:"country_id"`
	Label       *string  `json:"label"`
	AddressType string   `json:"address_type"`
	Line1       string   `json:"line1"`
	Line2       *string  `json:"line2"`
	City        string   `json:"city"`
	Region      *string  `json:"region"`
	PostalCode  *string  `json:"postal_code"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	IsDefault   bool     `json:"is_default"`
}

type CustomerLoginResult struct {
	Token string `json:"token"`
}

func (s *CustomerService) Register(ctx context.Context, in CustomerRegisterInput) (*models.CustomerDetails, error) {
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	in.CustomerType = strings.TrimSpace(in.CustomerType)
	if in.CustomerType == "" {
		in.CustomerType = "individual"
	}
	if in.Email == "" || len(in.Password) < 8 {
		return nil, ErrInvalidInput
	}

	countryID, err := uuid.Parse(in.CountryID)
	if err != nil {
		return nil, ErrInvalidCountry
	}
	if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry
		}
		return nil, err
	}

	dob, err := repository.ParseDate(in.DateOfBirth)
	if err != nil {
		return nil, ErrInvalidInput
	}

	hash, err := utils.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	displayName := in.DisplayName
	if displayName == nil || strings.TrimSpace(*displayName) == "" {
		fallback := strings.Split(in.Email, "@")[0]
		displayName = &fallback
	}

	customer := &models.Customer{
		CountryID:    countryID,
		Email:        in.Email,
		Phone:        in.Phone,
		PasswordHash: hash,
		DisplayName:  displayName,
		CustomerType: in.CustomerType,
		DateOfBirth:  dob,
		Status:       "active",
	}
	if err := s.customers.Create(ctx, customer); err != nil {
		if errors.Is(err, repository.ErrCustomerDuplicate) {
			return nil, ErrCustomerConflict
		}
		return nil, err
	}

	addresses := make([]models.CustomerAddress, 0, len(in.Addresses))
	for i, addrIn := range in.Addresses {
		if i == 0 && !addrIn.IsDefault && len(in.Addresses) == 1 {
			addrIn.IsDefault = true
		}
		addr, err := s.buildAddress(customer.ID, addrIn)
		if err != nil {
			return nil, err
		}
		if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
			if errors.Is(err, repository.ErrCountryNotFound) {
				return nil, ErrInvalidCountry
			}
			return nil, err
		}
		if err := s.customers.CreateAddress(ctx, addr); err != nil {
			return nil, err
		}
		addresses = append(addresses, *addr)
	}

	return &models.CustomerDetails{Customer: *customer, Addresses: addresses}, nil
}

func (s *CustomerService) Login(ctx context.Context, email, password string) (*CustomerLoginResult, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	customer, err := s.customers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !utils.CheckPassword(password, customer.PasswordHash) {
		return nil, ErrInvalidCredentials
	}

	token, err := utils.GenerateJWT(customer.ID.String(), customer.Email, "customer", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &CustomerLoginResult{Token: token}, nil
}

func (s *CustomerService) GetDetails(ctx context.Context, customerID string) (*models.CustomerDetails, error) {
	customer, err := s.customers.GetByID(ctx, customerID)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}
	addresses, err := s.customers.ListAddresses(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return &models.CustomerDetails{Customer: *customer, Addresses: addresses}, nil
}

func (s *CustomerService) Update(ctx context.Context, customerID string, in CustomerUpdateInput) (*models.Customer, error) {
	customer, err := s.customers.GetByID(ctx, customerID)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}

	if in.CountryID != "" {
		countryID, err := uuid.Parse(in.CountryID)
		if err != nil {
			return nil, ErrInvalidCountry
		}
		if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
			if errors.Is(err, repository.ErrCountryNotFound) {
				return nil, ErrInvalidCountry
			}
			return nil, err
		}
		customer.CountryID = countryID
	}
	if in.Phone != nil {
		customer.Phone = in.Phone
	}
	if in.DisplayName != nil {
		customer.DisplayName = in.DisplayName
	}
	if strings.TrimSpace(in.CustomerType) != "" {
		customer.CustomerType = strings.TrimSpace(in.CustomerType)
	}
	if in.DateOfBirth != "" {
		dob, err := repository.ParseDate(in.DateOfBirth)
		if err != nil {
			return nil, ErrInvalidInput
		}
		customer.DateOfBirth = dob
	}
	if strings.TrimSpace(in.Status) != "" {
		customer.Status = strings.TrimSpace(in.Status)
	}

	if err := s.customers.Update(ctx, customer); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}
	return customer, nil
}

func (s *CustomerService) Delete(ctx context.Context, customerID string) error {
	err := s.customers.SoftDelete(ctx, customerID)
	if errors.Is(err, repository.ErrCustomerNotFound) {
		return ErrCustomerNotFound
	}
	return err
}

func (s *CustomerService) AddAddress(ctx context.Context, customerID string, in AddressInput) (*models.CustomerAddress, error) {
	if _, err := s.customers.GetByID(ctx, customerID); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}

	cid, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrCustomerNotFound
	}
	addr, err := s.buildAddress(cid, in)
	if err != nil {
		return nil, err
	}
	if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry
		}
		return nil, err
	}
	if err := s.customers.CreateAddress(ctx, addr); err != nil {
		return nil, err
	}
	return addr, nil
}

func (s *CustomerService) DeleteAddress(ctx context.Context, customerID, addressID string) error {
	err := s.customers.DeleteAddress(ctx, customerID, addressID)
	if errors.Is(err, repository.ErrAddressNotFound) {
		return ErrAddressNotFound
	}
	return err
}

func (s *CustomerService) buildAddress(customerID uuid.UUID, in AddressInput) (*models.CustomerAddress, error) {
	in.Line1 = strings.TrimSpace(in.Line1)
	in.City = strings.TrimSpace(in.City)
	in.AddressType = strings.TrimSpace(in.AddressType)
	if in.AddressType == "" {
		in.AddressType = "shipping"
	}
	if in.Line1 == "" || in.City == "" || in.CountryID == "" {
		return nil, ErrInvalidAddress
	}
	countryID, err := uuid.Parse(in.CountryID)
	if err != nil {
		return nil, ErrInvalidCountry
	}
	return &models.CustomerAddress{
		CustomerID:  customerID,
		CountryID:   countryID,
		Label:       in.Label,
		AddressType: in.AddressType,
		Line1:       in.Line1,
		Line2:       in.Line2,
		City:        in.City,
		Region:      in.Region,
		PostalCode:  in.PostalCode,
		Latitude:    in.Latitude,
		Longitude:   in.Longitude,
		IsDefault:   in.IsDefault,
	}, nil
}
