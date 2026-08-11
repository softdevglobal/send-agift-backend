package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

var (
	ErrBootstrapForbidden = errors.New("bootstrap already completed")
	ErrInvalidInput       = errors.New("invalid input")
	ErrInvalidCredentials = errors.New("invalid email or password")
)

type AuthService struct {
	admins          *repository.AdminRepository
	customers       *repository.CustomerRepository
	sellers         *repository.SellerRepository
	jwtSecret       string
	jwtExpiry       time.Duration
	bootstrapSecret string
}

func NewAuthService(
	admins *repository.AdminRepository,
	customers *repository.CustomerRepository,
	sellers *repository.SellerRepository,
	jwtSecret, bootstrapSecret string,
	jwtExpiry time.Duration,
) *AuthService {
	return &AuthService{
		admins:          admins,
		customers:       customers,
		sellers:         sellers,
		jwtSecret:       jwtSecret,
		jwtExpiry:       jwtExpiry,
		bootstrapSecret: bootstrapSecret,
	}
}

type BootstrapInput struct {
	Email           string
	Password        string
	DisplayName     string
	BootstrapSecret string
}

type LoginInput struct {
	Email    string
	Password string
}

type LoginResult struct {
	Token string `json:"token"`
	Role  string `json:"role"`
}

// Bootstrap creates the first superadmin when none exist, or when the bootstrap secret matches.
func (s *AuthService) Bootstrap(ctx context.Context, in BootstrapInput) (*models.Admin, error) {
	count, err := s.admins.CountAdmins(ctx)
	if err != nil {
		return nil, err
	}

	if count > 0 && (s.bootstrapSecret == "" || in.BootstrapSecret != s.bootstrapSecret) {
		return nil, ErrBootstrapForbidden
	}

	if in.Email == "" || len(in.Password) < 8 {
		return nil, ErrInvalidInput
	}

	hash, err := utils.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	admin := &models.Admin{
		Email:        in.Email,
		PasswordHash: hash,
		DisplayName:  in.DisplayName,
		Role:         "superadmin",
		MFARequired:  true,
	}
	if err := s.admins.CreateAdmin(ctx, admin); err != nil {
		return nil, err
	}

	return admin, nil
}

// Login checks admin, then customer, then seller with the same email + password.
func (s *AuthService) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if email == "" || in.Password == "" {
		return nil, ErrInvalidCredentials
	}

	if admin, err := s.admins.GetByEmail(ctx, email); err == nil {
		if utils.CheckPassword(in.Password, admin.PasswordHash) {
			role := admin.Role
			if role == "" {
				role = "admin"
			}
			return s.token(admin.ID.String(), admin.Email, role)
		}
	} else if !errors.Is(err, repository.ErrAdminNotFound) {
		return nil, err
	}

	if customer, err := s.customers.GetByEmail(ctx, email); err == nil {
		if utils.CheckPassword(in.Password, customer.PasswordHash) {
			return s.token(customer.ID.String(), customer.Email, "customer")
		}
	} else if !errors.Is(err, repository.ErrCustomerNotFound) {
		return nil, err
	}

	if seller, err := s.sellers.GetByEmail(ctx, email); err == nil {
		if utils.CheckPassword(in.Password, seller.PasswordHash) {
			return s.token(seller.ID.String(), seller.Email, "seller")
		}
	} else if !errors.Is(err, repository.ErrSellerNotFound) {
		return nil, err
	}

	return nil, ErrInvalidCredentials
}

func (s *AuthService) token(id, email, role string) (*LoginResult, error) {
	token, err := utils.GenerateJWT(id, email, role, s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, Role: role}, nil
}
