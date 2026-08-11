package services

import (
	"context"
	"errors"
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
	jwtSecret       string
	jwtExpiry       time.Duration
	bootstrapSecret string
}

func NewAuthService(admins *repository.AdminRepository, jwtSecret, bootstrapSecret string, jwtExpiry time.Duration) *AuthService {
	return &AuthService{
		admins:          admins,
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

// Login verifies credentials and returns a signed JWT.
func (s *AuthService) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	if in.Email == "" || in.Password == "" {
		return nil, ErrInvalidCredentials
	}

	admin, err := s.admins.GetByEmail(ctx, in.Email)
	if err != nil {
		if errors.Is(err, repository.ErrAdminNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	if !utils.CheckPassword(in.Password, admin.PasswordHash) {
		return nil, ErrInvalidCredentials
	}

	role := admin.Role
	if role == "" {
		role = "admin"
	}

	token, err := utils.GenerateJWT(admin.ID.String(), admin.Email, role, s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}

	return &LoginResult{Token: token}, nil
}
