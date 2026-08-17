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
	ErrCustomerNotFound   = errors.New("customer not found")      // error for the service
	ErrCustomerConflict   = errors.New("customer already exists") // error for the service
	ErrAddressNotFound    = errors.New("address not found")       // error for the service
	ErrInvalidCountry     = errors.New("invalid country")         // error for the service
	ErrInvalidAddress     = errors.New("invalid address")         // error for the service
	ErrSavedGiftNotFound  = errors.New("saved gift not found")
	ErrSavedGiftConflict  = errors.New("product already saved")
	ErrSavedGiftProduct   = errors.New("product not found")
) // error for the service

type CustomerService struct {
	customers *repository.CustomerRepository // repository for the service
	countries *repository.CountryRepository  // repository for the service
	products  *repository.ProductRepository  // for validating product_id on saved gifts
	jwtSecret string                         // secret for the JWT
	jwtExpiry time.Duration                  // expiry for the JWT
}

func NewCustomerService(
	customers *repository.CustomerRepository,
	countries *repository.CountryRepository,
	products *repository.ProductRepository,
	jwtSecret string,
	jwtExpiry time.Duration,
) *CustomerService { // NewCustomerService is a function that creates a new CustomerService
	return &CustomerService{
		customers: customers,
		countries: countries,
		products:  products,
		jwtSecret: jwtSecret,
		jwtExpiry: jwtExpiry,
	}
}

type CustomerRegisterInput struct {
	CountryID    string // Country ID for the customer
	Email        string // Email for the customer
	Password     string
	Phone        *string        // Phone for the customer
	DisplayName  *string        // Display name for the customer
	CustomerType string         // Customer type for the customer
	DateOfBirth  string         // Date of birth for the customer
	Addresses    []AddressInput // Addresses for the customer
	ImageURL     *string
}

type CustomerUpdateInput struct {
	CountryID    string  // Country ID for the customer
	Phone        *string // Phone for the customer
	DisplayName  *string // Display name for the customer
	CustomerType string  // Customer type for the customer
	DateOfBirth  string  // Date of birth for the customer
	Status       string  // Status for the customer
	ImageURL     *string
}

type AddressInput struct {
	CountryID   string   `json:"country_id"`   // Country ID for the address
	Label       *string  `json:"label"`        // Label for the address
	AddressType string   `json:"address_type"` // Address type for the address
	Line1       string   `json:"line1"`        // Line 1 for the address
	Line2       *string  `json:"line2"`        // Line 2 for the address
	City        string   `json:"city"`         // City for the address
	Region      *string  `json:"region"`       // Region for the address
	PostalCode  *string  `json:"postal_code"`  // Postal code for the address
	Latitude    *float64 `json:"latitude"`     // Latitude for the address
	Longitude   *float64 `json:"longitude"`    // Longitude for the address
	IsDefault   bool     `json:"is_default"`   // Is default for the address
}

type CustomerLoginResult struct {
	Token string `json:"token"`
} // CustomerLoginResult is the result of the customer login operation

func (s *CustomerService) Register(ctx context.Context, in CustomerRegisterInput) (*models.CustomerDetails, error) { // Register is a function that registers a new customer
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	in.CustomerType = strings.TrimSpace(in.CustomerType)
	if in.CustomerType == "" {
		in.CustomerType = "individual"
	}
	if in.Email == "" || len(in.Password) < 8 {
		return nil, ErrInvalidInput // return an error if the input is invalid
	}

	countryID, err := uuid.Parse(in.CountryID)
	if err != nil {
		return nil, ErrInvalidCountry // return an error if the country is invalid
	}
	if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry // return an error if the country is not found
		}
		return nil, err // return an error if the country is not found
	}

	dob, err := repository.ParseDate(in.DateOfBirth)
	if err != nil {
		return nil, ErrInvalidInput // return an error if the date of birth is invalid
	}

	hash, err := utils.HashPassword(in.Password)
	if err != nil {
		return nil, err // return an error if the password is not hashed
	}

	displayName := in.DisplayName
	if displayName == nil || strings.TrimSpace(*displayName) == "" {
		fallback := strings.Split(in.Email, "@")[0]
		displayName = &fallback
	}

	customer := &models.Customer{ // create a new customer
		CountryID:    countryID,
		Email:        in.Email,
		Phone:        in.Phone,
		PasswordHash: hash,
		DisplayName:  displayName,
		CustomerType: in.CustomerType,
		DateOfBirth:  dob,
		Status:       "active",
		ImageURL:     in.ImageURL,
	}
	if err := s.customers.Create(ctx, customer); err != nil {
		if errors.Is(err, repository.ErrCustomerDuplicate) {
			return nil, ErrCustomerConflict // return an error if the customer already exists
		}
		return nil, err // return an error if the customer is not created
	}

	addresses := make([]models.CustomerAddress, 0, len(in.Addresses)) // create a new slice of customer addresses
	for i, addrIn := range in.Addresses {
		if i == 0 && !addrIn.IsDefault && len(in.Addresses) == 1 {
			addrIn.IsDefault = true // set the address to default if it is the first address and the default is not set
		}
		addr, err := s.buildAddress(customer.ID, addrIn)
		if err != nil {
			return nil, err // return an error if the address is not built
		}
		if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
			if errors.Is(err, repository.ErrCountryNotFound) {
				return nil, ErrInvalidCountry // return an error if the country is not found
			}
			return nil, err // return an error if the country is not found
		}
		if err := s.customers.CreateAddress(ctx, addr); err != nil {
			return nil, err // return an error if the address is not created
		}
		addresses = append(addresses, *addr) // append the address to the slice of customer addresses
	}

	return &models.CustomerDetails{Customer: *customer, Addresses: addresses}, nil // return the customer details
}

func (s *CustomerService) Login(ctx context.Context, email, password string) (*CustomerLoginResult, error) { // Login is a function that logs in a customer
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil, ErrInvalidCredentials // return an error if the email or password is invalid
	}

	customer, err := s.customers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrInvalidCredentials // return an error if the customer is not found
		}
		return nil, err // return an error if the customer is not found
	}
	if !utils.CheckPassword(password, customer.PasswordHash) {
		return nil, ErrInvalidCredentials // return an error if the password is invalid
	}

	token, err := utils.GenerateJWT(customer.ID.String(), customer.Email, "customer", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err // return an error if the token is not generated
	}
	return &CustomerLoginResult{Token: token}, nil // return the customer login result
}

func (s *CustomerService) GetDetails(ctx context.Context, customerID string) (*models.CustomerDetails, error) { // GetDetails is a function that gets the details of a customer
	customer, err := s.customers.GetByID(ctx, customerID)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound // return an error if the customer is not found
		}
		return nil, err // return an error if the customer is not found
	}
	addresses, err := s.customers.ListAddresses(ctx, customerID)
	if err != nil {
		return nil, err // return an error if the addresses are not found
	}
	return &models.CustomerDetails{Customer: *customer, Addresses: addresses}, nil // return the customer details
}

func (s *CustomerService) Update(ctx context.Context, customerID string, in CustomerUpdateInput) (*models.Customer, error) { // Update is a function that updates a customer
	customer, err := s.customers.GetByID(ctx, customerID)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound // return an error if the customer is not found
		}
		return nil, err // return an error if the customer is not found
	}

	if in.CountryID != "" {
		countryID, err := uuid.Parse(in.CountryID)
		if err != nil {
			return nil, ErrInvalidCountry // return an error if the country is invalid
		}
		if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
			if errors.Is(err, repository.ErrCountryNotFound) {
				return nil, ErrInvalidCountry // return an error if the country is not found
			}
			return nil, err // return an error if the country is not found
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
	if in.ImageURL != nil {
		customer.ImageURL = in.ImageURL
	}

	if err := s.customers.Update(ctx, customer); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound // return an error if the customer is not found
		}
		return nil, err // return an error if the customer is not updated
	}
	return customer, nil // return the customer
}

func (s *CustomerService) Delete(ctx context.Context, customerID string) error {
	err := s.customers.SoftDelete(ctx, customerID)
	if errors.Is(err, repository.ErrCustomerNotFound) {
		return ErrCustomerNotFound
	}
	return err
}

// AddAddress is a function that adds an address to a customer
func (s *CustomerService) AddAddress(ctx context.Context, customerID string, in AddressInput) (*models.CustomerAddress, error) { // AddAddress is a function that adds an address to a customer
	if _, err := s.customers.GetByID(ctx, customerID); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound // return an error if the customer is not found
		}
		return nil, err // return an error if the customer is not found
	}

	cid, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrCustomerNotFound // return an error if the customer is not found
	}
	addr, err := s.buildAddress(cid, in)
	if err != nil {
		return nil, err // return an error if the address is not built
	}
	if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry // return an error if the country is not found
		}
		return nil, err // return an error if the country is not found
	}
	if err := s.customers.CreateAddress(ctx, addr); err != nil {
		return nil, err // return an error if the address is not created
	}
	return addr, nil // return the address
}

func (s *CustomerService) DeleteAddress(ctx context.Context, customerID, addressID string) error { // DeleteAddress is a function that deletes an address from a customer
	err := s.customers.DeleteAddress(ctx, customerID, addressID)
	if errors.Is(err, repository.ErrAddressNotFound) {
		return ErrAddressNotFound // return an error if the address is not found
	}
	return err // return an error if the address is not deleted
}

// AddSavedGift validates the customer and product, then inserts a wishlist row.
// Duplicate customer+product returns ErrSavedGiftConflict.
func (s *CustomerService) AddSavedGift(ctx context.Context, customerID, productID string) (*models.SavedGift, error) {
	// Ensure the logged-in customer still exists.
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
	pid, err := uuid.Parse(productID)
	if err != nil {
		return nil, ErrSavedGiftProduct
	}

	// Product must exist in seller.products before it can be saved.
	exists, err := s.products.ExistsByID(ctx, pid.String())
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrSavedGiftProduct
	}

	gift := &models.SavedGift{
		CustomerID: cid,
		ProductID:  pid,
	}
	if err := s.customers.CreateSavedGift(ctx, gift); err != nil {
		if errors.Is(err, repository.ErrSavedGiftDuplicate) {
			return nil, ErrSavedGiftConflict
		}
		return nil, err
	}
	return gift, nil
}

// ListSavedGifts returns all saved gifts for the logged-in customer, with product details.
func (s *CustomerService) ListSavedGifts(ctx context.Context, customerID string) ([]models.SavedGiftDetails, error) {
	if _, err := s.customers.GetByID(ctx, customerID); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}
	return s.customers.ListSavedGifts(ctx, customerID)
}

// DeleteSavedGift removes a saved gift only if it belongs to this customer.
func (s *CustomerService) DeleteSavedGift(ctx context.Context, customerID, savedGiftID string) error {
	if _, err := s.customers.GetByID(ctx, customerID); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return ErrCustomerNotFound
		}
		return err
	}
	err := s.customers.DeleteSavedGift(ctx, customerID, savedGiftID)
	if errors.Is(err, repository.ErrSavedGiftNotFound) {
		return ErrSavedGiftNotFound
	}
	return err
}

func (s *CustomerService) buildAddress(customerID uuid.UUID, in AddressInput) (*models.CustomerAddress, error) { // buildAddress is a function that builds an address
	in.Line1 = strings.TrimSpace(in.Line1)
	in.City = strings.TrimSpace(in.City)
	in.AddressType = strings.TrimSpace(in.AddressType)
	if in.AddressType == "" {
		in.AddressType = "shipping"
	}
	if in.Line1 == "" || in.City == "" || in.CountryID == "" {
		return nil, ErrInvalidAddress // return an error if the address is invalid
	}
	countryID, err := uuid.Parse(in.CountryID)
	if err != nil {
		return nil, ErrInvalidCountry // return an error if the country is invalid
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
	}, nil // return the address
} // buildAddress is a function that builds an address
