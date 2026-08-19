package services

import (
	"context"
	"errors"
	"strings"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var ErrUnauthorized = errors.New("unauthorized")
var ErrAdminNotFound = errors.New("admin not found")

type AdminService struct {
	admins *repository.AdminRepository
}

func NewAdminService(admins *repository.AdminRepository) *AdminService {
	return &AdminService{admins: admins}
}

type AdminUpdateInput struct {
	DisplayName *string `json:"display_name"`
	ImageURL    *string `json:"image_url"`
}

// GetByID returns an admin profile for the authenticated admin id.
func (s *AdminService) GetByID(ctx context.Context, id string) (*models.Admin, error) {
	if id == "" {
		return nil, ErrUnauthorized
	}
	admin, err := s.admins.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrAdminNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	return admin, nil
}

// Update updates the authenticated admin profile.
func (s *AdminService) Update(ctx context.Context, id string, in AdminUpdateInput) (*models.Admin, error) {
	if id == "" {
		return nil, ErrUnauthorized
	}
	admin, err := s.admins.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrAdminNotFound) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}
	if in.DisplayName != nil {
		admin.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.ImageURL != nil {
		admin.ImageURL = in.ImageURL
	}
	if err := s.admins.Update(ctx, admin); err != nil {
		if errors.Is(err, repository.ErrAdminNotFound) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}
	return admin, nil
}
