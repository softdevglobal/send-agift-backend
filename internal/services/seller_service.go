package services

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

var (
	ErrSellerNotFound = errors.New("seller not found")	// return an error if the seller is not found			
	ErrSellerConflict = errors.New("seller already exists")	// return an error if the seller already exists
	ErrShopNotFound   = errors.New("shop not found")	// return an error if the shop is not found
	ErrShopConflict   = errors.New("shop already exists")	// return an error if the shop already exists
	ErrSellerAddrNotFound = errors.New("seller address not found")	// return an error if the seller address is not found
	ErrInvalidShop    = errors.New("invalid shop")	// return an error if the shop is invalid
)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)	// return a regular expression that matches any non-alphanumeric character

type SellerService struct {
	sellers   *repository.SellerRepository	// repository for the seller
	countries *repository.CountryRepository	// repository for the country
	jwtSecret string	// secret for the JWT
	jwtExpiry time.Duration	// expiry for the JWT
}

func NewSellerService(
	sellers *repository.SellerRepository,	// repository for the seller
	countries *repository.CountryRepository,	// repository for the country
	jwtSecret string,
	jwtExpiry time.Duration,	// expiry for the JWT
) *SellerService {
	return &SellerService{sellers: sellers, countries: countries, jwtSecret: jwtSecret, jwtExpiry: jwtExpiry}	// return a new SellerService
}

type SellerRegisterInput struct {	// SellerRegisterInput is a struct that contains the input for the seller register
	CountryID   string	// country ID for the seller
	SellerType  string
	LegalName   string	// legal name for the seller
	TradingName *string	// trading name for the seller
	Email       string	// email for the seller
	Password    string	// password for the seller
	Phone       *string	// phone for the seller	
	ImageURL    *string
	Addresses   []SellerAddressInput	// addresses for the seller
	Shop        *ShopInput // nil / omitted = blank (no shop)
}

type SellerUpdateInput struct {	// SellerUpdateInput is a struct that contains the input for the seller update		
	CountryID   string	// country ID for the seller
	SellerType  string	// seller type for the seller
	LegalName   string	// legal name for the seller
	TradingName *string	// trading name for the seller
	Phone       *string	// phone for the seller
	ImageURL    *string
}

type SellerAddressInput struct {
	CountryID   string   `json:"country_id"`	// country ID for the seller address
	Label       *string  `json:"label"`	// label for the seller address
	AddressType string   `json:"address_type"`	// address type for the seller address
	Line1       string   `json:"line1"`	// line1 for the seller address
	Line2       *string  `json:"line2"`	// line2 for the seller address
	City        string   `json:"city"`	// city for the seller address
	Region      *string  `json:"region"`	// region for the seller address
	PostalCode  *string  `json:"postal_code"`	// postal code for the seller address
	Latitude    *float64 `json:"latitude"`	// latitude for the seller address
	Longitude   *float64 `json:"longitude"`	// longitude for the seller address
	IsDefault   bool     `json:"is_default"`	// is default for the seller address
}

type ShopInput struct {
	Name                    string  `json:"name"`	// name for the shop		
	Slug                    string  `json:"slug"`	// slug for the shop
	Description             *string `json:"description"`	// description for the shop
	CustomerVisibleLocation *string `json:"customer_visible_location"`	// customer visible location for the shop
	Status                  string  `json:"status"`	// status for the shop
	AddressID               *string `json:"address_id"`
	ReturnAddressID         *string `json:"return_address_id"`
	ImageURL                *string `json:"image_url"`
}	// ShopInput is a struct that contains the input for the shop

type SellerLoginResult struct {
	Token string `json:"token"`	// token for the seller login	
}

func (s *SellerService) Register(ctx context.Context, in SellerRegisterInput) (*models.SellerDetails, error) {
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	in.LegalName = strings.TrimSpace(in.LegalName)	// legal name for the seller
	in.SellerType = strings.TrimSpace(in.SellerType)	// seller type for the seller
	if in.SellerType == "" {
		in.SellerType = "individual"	// seller type for the seller
	}
	if in.Email == "" || len(in.Password) < 8 || in.LegalName == "" {
		return nil, ErrInvalidInput	// return an error if the input is invalid
	}

	countryID, err := uuid.Parse(in.CountryID)
	if err != nil {
		return nil, ErrInvalidCountry	// return an error if the country is invalid
	}
	if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry	// return an error if the country is not found
		}
		return nil, err	// return an error if the country is not found
	}

	hash, err := utils.HashPassword(in.Password)
	if err != nil {
		return nil, err	// return an error if the password is not hashed
	}

	seller := &models.Seller{
		CountryID:          countryID,
		SellerType:         in.SellerType,
		LegalName:          in.LegalName,
		TradingName:        in.TradingName,
		Email:              in.Email,
		Phone:              in.Phone,
		PasswordHash:       hash,
		VerificationStatus: "unverified",
		Status:             "active",
		ImageURL:           in.ImageURL,
	}
	if err := s.sellers.Create(ctx, seller); err != nil {
		if errors.Is(err, repository.ErrSellerDuplicate) {
			return nil, ErrSellerConflict	// return an error if the seller already exists
		}
		return nil, err	// return an error if the seller is not created
	}

	addresses := make([]models.SellerAddress, 0, len(in.Addresses))
	for i, addrIn := range in.Addresses {
		if i == 0 && !addrIn.IsDefault && len(in.Addresses) == 1 {
			addrIn.IsDefault = true
		}
		addr, err := s.buildAddress(seller.ID, addrIn)
		if err != nil {
			return nil, err	// return an error if the address is not built
		}
		if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
			if errors.Is(err, repository.ErrCountryNotFound) {
				return nil, ErrInvalidCountry	// return an error if the country is invalid					
			}
			return nil, err	// return an error if the country is not found
		}
		if err := s.sellers.CreateAddress(ctx, addr); err != nil {
			return nil, err	// return an error if the address is not created
		}
		addresses = append(addresses, *addr)
	}

	shops := []models.Shop{}
	if in.Shop != nil && strings.TrimSpace(in.Shop.Name) != "" {
		shop, err := s.createShopForSeller(ctx, seller.ID, *in.Shop)
		if err != nil {
			return nil, err	// return an error if the shop is not created			
		}
		shops = append(shops, *shop)
	}

	return &models.SellerDetails{Seller: *seller, Addresses: addresses, Shops: shops}, nil	// return the seller details
}

func (s *SellerService) Login(ctx context.Context, email, password string) (*SellerLoginResult, error) { // Login is a function that logs in a seller
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil, ErrInvalidCredentials	// return an error if the email or password is invalid	
	}
	seller, err := s.sellers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrInvalidCredentials	// return an error if the seller is not found
		}
		return nil, err	// return an error if the seller is not found
	}
	if !utils.CheckPassword(password, seller.PasswordHash) {
		return nil, ErrInvalidCredentials	// return an error if the password is invalid
	}
	token, err := utils.GenerateJWT(seller.ID.String(), seller.Email, "seller", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err	// return an error if the token is not generated
	}
	return &SellerLoginResult{Token: token}, nil	// return the seller login result
}

func (s *SellerService) GetDetails(ctx context.Context, sellerID string) (*models.SellerDetails, error) { // GetDetails is a function that gets the details of a seller
	seller, err := s.sellers.GetByID(ctx, sellerID)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound	// return an error if the seller is not found
		}
		return nil, err	// return an error if the seller is not found
	}
	addresses, err := s.sellers.ListAddresses(ctx, sellerID)
	if err != nil {
		return nil, err	// return an error if the addresses are not found
	}
	shops, err := s.sellers.ListShops(ctx, sellerID)
	if err != nil {
		return nil, err	// return an error if the shops are not found
	}
	return &models.SellerDetails{Seller: *seller, Addresses: addresses, Shops: shops}, nil	// return the seller details
}

func (s *SellerService) Update(ctx context.Context, sellerID string, in SellerUpdateInput) (*models.Seller, error) { // Update is a function that updates a seller	
	seller, err := s.sellers.GetByID(ctx, sellerID)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound	// return an error if the seller is not found
		}
		return nil, err	// return an error if the seller is not found
	}
	if in.CountryID != "" {
		countryID, err := uuid.Parse(in.CountryID)
		if err != nil {
			return nil, ErrInvalidCountry	// return an error if the country is invalid
		}
		if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
			if errors.Is(err, repository.ErrCountryNotFound) {
				return nil, ErrInvalidCountry	// return an error if the country is not found
			}
			return nil, err	// return an error if the country is not found
		}
		seller.CountryID = countryID
	}
	if strings.TrimSpace(in.SellerType) != "" {
		seller.SellerType = strings.TrimSpace(in.SellerType)	// seller type for the seller
	}
	if strings.TrimSpace(in.LegalName) != "" {
		seller.LegalName = strings.TrimSpace(in.LegalName)	// legal name for the seller
	}
	if in.TradingName != nil {
		seller.TradingName = in.TradingName	// trading name for the seller
	}
	if in.Phone != nil {
		seller.Phone = in.Phone	// phone for the seller
	}
	if in.ImageURL != nil {
		seller.ImageURL = in.ImageURL
	}
	if err := s.sellers.Update(ctx, seller); err != nil {
		return nil, err	// return an error if the seller is not updated
	}
	return seller, nil	// return the seller
}

func (s *SellerService) Delete(ctx context.Context, sellerID string) error { // Delete is a function that deletes a seller
	err := s.sellers.SoftDeactivate(ctx, sellerID)
	if errors.Is(err, repository.ErrSellerNotFound) {
		return ErrSellerNotFound	// return an error if the seller is not found						
	}
	return err	// return an error if the seller is not deleted
}

func (s *SellerService) AddAddress(ctx context.Context, sellerID string, in SellerAddressInput) (*models.SellerAddress, error) { // AddAddress is a function that adds an address to a seller
	if _, err := s.sellers.GetByID(ctx, sellerID); err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound	// return an error if the seller is not found
		}
		return nil, err	// return an error if the seller is not found
	}
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrSellerNotFound	// return an error if the seller is not found
	}
	addr, err := s.buildAddress(sid, in)
	if err != nil {
		return nil, err	// return an error if the address is not built
	}
	if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry	// return an error if the country is invalid
		}
		return nil, err	// return an error if the country is not found
	}
	if err := s.sellers.CreateAddress(ctx, addr); err != nil {
		return nil, err	// return an error if the address is not created
	}
	return addr, nil	// return the address
}

func (s *SellerService) UpdateAddress(ctx context.Context, sellerID, addressID string, in SellerAddressInput) (*models.SellerAddress, error) {
	if _, err := s.sellers.GetByID(ctx, sellerID); err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		return nil, err
	}
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrSellerNotFound
	}
	aid, err := uuid.Parse(addressID)
	if err != nil {
		return nil, ErrSellerAddrNotFound
	}
	addr, err := s.buildAddress(sid, in)
	if err != nil {
		return nil, err
	}
	addr.ID = aid
	if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry
		}
		return nil, err
	}
	if err := s.sellers.UpdateAddress(ctx, addr); err != nil {
		if errors.Is(err, repository.ErrSellerAddrNotFound) {
			return nil, ErrSellerAddrNotFound
		}
		return nil, err
	}
	return addr, nil
}

func (s *SellerService) DeleteAddress(ctx context.Context, sellerID, addressID string) error { // DeleteAddress is a function that deletes an address from a seller
	err := s.sellers.DeleteAddress(ctx, sellerID, addressID)
	if errors.Is(err, repository.ErrSellerAddrNotFound) {
		return ErrSellerAddrNotFound	// return an error if the seller address is not found
	}
	return err	// return an error if the seller address is not deleted				
}

func (s *SellerService) CreateShop(ctx context.Context, sellerID string, in ShopInput) (*models.Shop, error) {
	if _, err := s.sellers.GetByID(ctx, sellerID); err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		return nil, err
	}
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrSellerNotFound
	}
	return s.createShopForSeller(ctx, sid, in)	// return the shop
}

func (s *SellerService) UpdateShop(ctx context.Context, sellerID, shopID string, in ShopInput) (*models.Shop, error) {
	shop, err := s.sellers.GetShopByID(ctx, sellerID, shopID)
	if err != nil {
		if errors.Is(err, repository.ErrShopNotFound) {
			return nil, ErrShopNotFound
		}
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, ErrInvalidShop
	}	
	shop.Name = strings.TrimSpace(in.Name)
	shop.Slug = slugOrFromName(in.Slug, in.Name)
	shop.Description = in.Description
	shop.CustomerVisibleLocation = in.CustomerVisibleLocation
	if strings.TrimSpace(in.Status) != "" {
		shop.Status = strings.TrimSpace(in.Status)
	}
	if in.AddressID != nil {
		if *in.AddressID == "" {
			shop.AddressID = nil
		} else {
			aid, err := uuid.Parse(*in.AddressID)
			if err != nil {
				return nil, ErrInvalidAddress
			}
			shop.AddressID = &aid
		}
	}
	if in.ReturnAddressID != nil {
		if *in.ReturnAddressID == "" {
			shop.ReturnAddressID = nil
		} else {
			rid, err := uuid.Parse(*in.ReturnAddressID)
			if err != nil {
				return nil, ErrInvalidAddress
			}
			shop.ReturnAddressID = &rid
		}
	}
	if in.ImageURL != nil {
		shop.ImageURL = in.ImageURL
	}
	if err := s.sellers.UpdateShop(ctx, shop); err != nil {
		if errors.Is(err, repository.ErrShopDuplicate) {
			return nil, ErrShopConflict
		}
		if errors.Is(err, repository.ErrShopNotFound) {
			return nil, ErrShopNotFound
		}
		return nil, err
	}
	return shop, nil
}

func (s *SellerService) DeleteShop(ctx context.Context, sellerID, shopID string) error {
	err := s.sellers.DeleteShop(ctx, sellerID, shopID)
	if errors.Is(err, repository.ErrShopNotFound) {
		return ErrShopNotFound
	}
	return err
}

func (s *SellerService) createShopForSeller(ctx context.Context, sellerID uuid.UUID, in ShopInput) (*models.Shop, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, ErrInvalidShop
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "active"
	}
	var addressID *uuid.UUID
	if in.AddressID != nil && *in.AddressID != "" {
		aid, err := uuid.Parse(*in.AddressID)
		if err != nil {
			return nil, ErrInvalidAddress
		}
		addressID = &aid
	}
	var returnAddressID *uuid.UUID
	if in.ReturnAddressID != nil && *in.ReturnAddressID != "" {
		rid, err := uuid.Parse(*in.ReturnAddressID)
		if err != nil {
			return nil, ErrInvalidAddress
		}
		returnAddressID = &rid
	}
	shop := &models.Shop{
		SellerID:                sellerID,
		Name:                    in.Name,
		Slug:                    slugOrFromName(in.Slug, in.Name),
		Description:             in.Description,
		CustomerVisibleLocation: in.CustomerVisibleLocation,
		Status:                  status,
		AddressID:               addressID,
		ReturnAddressID:         returnAddressID,
		ImageURL:                in.ImageURL,
	}
	if err := s.sellers.CreateShop(ctx, shop); err != nil {
		if errors.Is(err, repository.ErrShopDuplicate) {
			return nil, ErrShopConflict
		}
		return nil, err
	}
	return shop, nil
}

func (s *SellerService) buildAddress(sellerID uuid.UUID, in SellerAddressInput) (*models.SellerAddress, error) {
	in.Line1 = strings.TrimSpace(in.Line1)
	in.City = strings.TrimSpace(in.City)
	in.AddressType = strings.TrimSpace(in.AddressType)
	if in.AddressType == "" {
		in.AddressType = "both"
	}
	switch in.AddressType {
	case "pickup", "return", "both":
	default:
		return nil, ErrInvalidAddress
	}
	if in.Line1 == "" || in.City == "" || in.CountryID == "" {
		return nil, ErrInvalidAddress
	}
	countryID, err := uuid.Parse(in.CountryID)
	if err != nil {
		return nil, ErrInvalidCountry
	}
	return &models.SellerAddress{
		SellerID:    sellerID,
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

func slugOrFromName(slug, name string) string {
	slug = strings.TrimSpace(strings.ToLower(slug))
	if slug == "" {
		slug = strings.ToLower(strings.TrimSpace(name))
	}
	slug = nonSlugChars.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = uuid.NewString()
	}
	return slug
}
