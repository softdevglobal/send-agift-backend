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

// Define custom errors for the authentication failures
var (
	ErrBootstrapForbidden = errors.New("bootstrap already completed") // if the bootstrap is already completed
	ErrInvalidInput       = errors.New("invalid input") // if the input is invalid
	ErrInvalidCredentials = errors.New("invalid email or password") // if the email or password is invalid
)
// Authservice handles authentication for admins, customers and sellers
type AuthService struct {
	admins          *repository.AdminRepository // repository for admin operations
	customers       *repository.CustomerRepository // repository for customer operations
	sellers         *repository.SellerRepository // repository for seller operations
	jwtSecret       string // secret for the JWT
	jwtExpiry       time.Duration // expiry for the JWT
	bootstrapSecret string // secret for the bootstrap
}

// NewAuthService is a simple constructor for the AuthService
func NewAuthService( 
	admins *repository.AdminRepository, // repository for admin operations
	customers *repository.CustomerRepository, // repository for customer operations
	sellers *repository.SellerRepository, // repository for seller operations
	jwtSecret, bootstrapSecret string, // secret for the JWT and bootstrap
	jwtExpiry time.Duration, // expiry for the JWT
) *AuthService {
	return &AuthService{ // returns a new AuthService
		admins:          admins, // repository for admin operations
		customers:       customers, // repository for customer operations		
		sellers:         sellers, // repository for seller operations
		jwtSecret:       jwtSecret, // secret for the JWT
		jwtExpiry:       jwtExpiry, // expiry for the JWT
		bootstrapSecret: bootstrapSecret, // secret for the bootstrap
	}
}
// BootstrapInput is the input for the bootstrap operation
type BootstrapInput struct {
	Email           string
	Password        string
	DisplayName     string
	BootstrapSecret string
}

// LoginInput is the input for the login operation
type LoginInput struct {
	Email    string
	Password string
}

// LoginResult is the result of the login operation
type LoginResult struct {
	Token string `json:"token"`
	Role  string `json:"role"`
}

// Bootstrap creates the first superadmin when none exist, or when the bootstrap secret matches.
func (s *AuthService) Bootstrap(ctx context.Context, in BootstrapInput) (*models.Admin, error) {
	count, err := s.admins.CountAdmins(ctx) // count the number of admins
	if err != nil {
		return nil, err // return an error if the count of admins is not found
	}

	if count > 0 && (s.bootstrapSecret == "" || in.BootstrapSecret != s.bootstrapSecret) {
		return nil, ErrBootstrapForbidden // return an error if the bootstrap is forbidden
	}

	if in.Email == "" || len(in.Password) < 8 {
		return nil, ErrInvalidInput // return an error if the input is invalid
	}

	hash, err := utils.HashPassword(in.Password)
	if err != nil {
		return nil, err // return an error if the password is not hashed
	}

	admin := &models.Admin{
		Email:        in.Email,
		PasswordHash: hash,
		DisplayName:  in.DisplayName,
		Role:         "superadmin",
		MFARequired:  true,
	}
	if err := s.admins.CreateAdmin(ctx, admin); err != nil {
		return nil, err // return an error if the admin is not created
	}

	return admin, nil // return the admin	
}

// Login checks admin, then customer, then seller with the same email + password.
func (s *AuthService) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email)) // trim the email and convert it to lowercase
	if email == "" || in.Password == "" {
		return nil, ErrInvalidCredentials // return an error if the email or password is invalid
	}
	// check if the admin exists
	if admin, err := s.admins.GetByEmail(ctx, email); err == nil {
		if utils.CheckPassword(in.Password, admin.PasswordHash) {
			role := admin.Role // get the role of the admin
			if role == "" {
				role = "admin"
			}
			return s.token(admin.ID.String(), admin.Email, role) // return the token and role
		}
	} else if !errors.Is(err, repository.ErrAdminNotFound) {
		return nil, err // return an error if the admin is not found
	}
	// check if the customer exists
	if customer, err := s.customers.GetByEmail(ctx, email); err == nil {
		if utils.CheckPassword(in.Password, customer.PasswordHash) {
			return s.token(customer.ID.String(), customer.Email, "customer") // return the token and role
		}
	} else if !errors.Is(err, repository.ErrCustomerNotFound) {
		return nil, err // return an error if the customer is not found
	}
	// check if the seller exists
	if seller, err := s.sellers.GetByEmail(ctx, email); err == nil {
		if utils.CheckPassword(in.Password, seller.PasswordHash) {
			return s.token(seller.ID.String(), seller.Email, "seller") // return the token and role
		}
	} else if !errors.Is(err, repository.ErrSellerNotFound) {
		return nil, err // return an error if the seller is not found
	}

	return nil, ErrInvalidCredentials // return an error if the email or password is invalid
}

// token generates a JWT token for the user
func (s *AuthService) token(id, email, role string) (*LoginResult, error) {
	token, err := utils.GenerateJWT(id, email, role, s.jwtSecret, s.jwtExpiry) // generate a JWT token
	if err != nil {
		return nil, err // return an error if the token is not generated
	}
	return &LoginResult{Token: token, Role: role}, nil // return the token and role
}
