package services

import (
	"context"
	"errors"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var ErrUnauthorized = errors.New("unauthorized")

type AdminService struct {
	admins *repository.AdminRepository
}

func NewAdminService(admins *repository.AdminRepository) *AdminService {
	return &AdminService{admins: admins}
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
