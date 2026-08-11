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
	ErrSellerNotFound = errors.New("seller not found")
	ErrSellerConflict = errors.New("seller already exists")
	ErrShopNotFound   = errors.New("shop not found")
	ErrShopConflict   = errors.New("shop already exists")
	ErrSellerAddrNotFound = errors.New("seller address not found")
	ErrInvalidShop    = errors.New("invalid shop")
)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

type SellerService struct {
	sellers   *repository.SellerRepository
	countries *repository.CountryRepository
	jwtSecret string
	jwtExpiry time.Duration
}

func NewSellerService(
	sellers *repository.SellerRepository,
	countries *repository.CountryRepository,
	jwtSecret string,
	jwtExpiry time.Duration,
) *SellerService {
	return &SellerService{sellers: sellers, countries: countries, jwtSecret: jwtSecret, jwtExpiry: jwtExpiry}
}

type SellerRegisterInput struct {
	CountryID   string
	SellerType  string
	LegalName   string
	TradingName *string
	Email       string
	Password    string
	Phone       *string
	Addresses   []SellerAddressInput
	Shop        *ShopInput // nil / omitted = blank (no shop)
}

type SellerUpdateInput struct {
	CountryID   string
	SellerType  string
	LegalName   string
	TradingName *string
	Phone       *string
}

type SellerAddressInput struct {
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

type ShopInput struct {
	Name                    string  `json:"name"`
	Slug                    string  `json:"slug"`
	Description             *string `json:"description"`
	ReturnAddressMode       string  `json:"return_address_mode"`
	CustomerVisibleLocation *string `json:"customer_visible_location"`
	Status                  string  `json:"status"`
	AddressID               *string `json:"address_id"`
}

type SellerLoginResult struct {
	Token string `json:"token"`
}

func (s *SellerService) Register(ctx context.Context, in SellerRegisterInput) (*models.SellerDetails, error) {
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	in.LegalName = strings.TrimSpace(in.LegalName)
	in.SellerType = strings.TrimSpace(in.SellerType)
	if in.SellerType == "" {
		in.SellerType = "individual"
	}
	if in.Email == "" || len(in.Password) < 8 || in.LegalName == "" {
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

	hash, err := utils.HashPassword(in.Password)
	if err != nil {
		return nil, err
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
	}
	if err := s.sellers.Create(ctx, seller); err != nil {
		if errors.Is(err, repository.ErrSellerDuplicate) {
			return nil, ErrSellerConflict
		}
		return nil, err
	}

	addresses := make([]models.SellerAddress, 0, len(in.Addresses))
	for i, addrIn := range in.Addresses {
		if i == 0 && !addrIn.IsDefault && len(in.Addresses) == 1 {
			addrIn.IsDefault = true
		}
		addr, err := s.buildAddress(seller.ID, addrIn)
		if err != nil {
			return nil, err
		}
		if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
			if errors.Is(err, repository.ErrCountryNotFound) {
				return nil, ErrInvalidCountry
			}
			return nil, err
		}
		if err := s.sellers.CreateAddress(ctx, addr); err != nil {
			return nil, err
		}
		addresses = append(addresses, *addr)
	}

	shops := []models.Shop{}
	if in.Shop != nil && strings.TrimSpace(in.Shop.Name) != "" {
		shop, err := s.createShopForSeller(ctx, seller.ID, *in.Shop)
		if err != nil {
			return nil, err
		}
		shops = append(shops, *shop)
	}

	return &models.SellerDetails{Seller: *seller, Addresses: addresses, Shops: shops}, nil
}

func (s *SellerService) Login(ctx context.Context, email, password string) (*SellerLoginResult, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	seller, err := s.sellers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !utils.CheckPassword(password, seller.PasswordHash) {
		return nil, ErrInvalidCredentials
	}
	token, err := utils.GenerateJWT(seller.ID.String(), seller.Email, "seller", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &SellerLoginResult{Token: token}, nil
}

func (s *SellerService) GetDetails(ctx context.Context, sellerID string) (*models.SellerDetails, error) {
	seller, err := s.sellers.GetByID(ctx, sellerID)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		return nil, err
	}
	addresses, err := s.sellers.ListAddresses(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	shops, err := s.sellers.ListShops(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	return &models.SellerDetails{Seller: *seller, Addresses: addresses, Shops: shops}, nil
}

func (s *SellerService) Update(ctx context.Context, sellerID string, in SellerUpdateInput) (*models.Seller, error) {
	seller, err := s.sellers.GetByID(ctx, sellerID)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
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
		seller.CountryID = countryID
	}
	if strings.TrimSpace(in.SellerType) != "" {
		seller.SellerType = strings.TrimSpace(in.SellerType)
	}
	if strings.TrimSpace(in.LegalName) != "" {
		seller.LegalName = strings.TrimSpace(in.LegalName)
	}
	if in.TradingName != nil {
		seller.TradingName = in.TradingName
	}
	if in.Phone != nil {
		seller.Phone = in.Phone
	}
	if err := s.sellers.Update(ctx, seller); err != nil {
		return nil, err
	}
	return seller, nil
}

func (s *SellerService) Delete(ctx context.Context, sellerID string) error {
	err := s.sellers.SoftDeactivate(ctx, sellerID)
	if errors.Is(err, repository.ErrSellerNotFound) {
		return ErrSellerNotFound
	}
	return err
}

func (s *SellerService) AddAddress(ctx context.Context, sellerID string, in SellerAddressInput) (*models.SellerAddress, error) {
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
	addr, err := s.buildAddress(sid, in)
	if err != nil {
		return nil, err
	}
	if _, err := s.countries.GetByID(ctx, addr.CountryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry
		}
		return nil, err
	}
	if err := s.sellers.CreateAddress(ctx, addr); err != nil {
		return nil, err
	}
	return addr, nil
}

func (s *SellerService) DeleteAddress(ctx context.Context, sellerID, addressID string) error {
	err := s.sellers.DeleteAddress(ctx, sellerID, addressID)
	if errors.Is(err, repository.ErrSellerAddrNotFound) {
		return ErrSellerAddrNotFound
	}
	return err
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
	return s.createShopForSeller(ctx, sid, in)
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
	if strings.TrimSpace(in.ReturnAddressMode) != "" {
		shop.ReturnAddressMode = strings.TrimSpace(in.ReturnAddressMode)
	}
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
	mode := strings.TrimSpace(in.ReturnAddressMode)
	if mode == "" {
		mode = "shop"
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
	shop := &models.Shop{
		SellerID:                sellerID,
		Name:                    in.Name,
		Slug:                    slugOrFromName(in.Slug, in.Name),
		Description:             in.Description,
		ReturnAddressMode:       mode,
		CustomerVisibleLocation: in.CustomerVisibleLocation,
		Status:                  status,
		AddressID:               addressID,
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
