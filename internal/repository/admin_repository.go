package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var ErrAdminNotFound = errors.New("admin not found")

type AdminRepository struct {
	db *pgxpool.Pool
}

func NewAdminRepository(db *pgxpool.Pool) *AdminRepository {
	return &AdminRepository{db: db}
}

// CountAdmins tells us whether any admin exists yet — the bootstrap
// endpoint uses this to only allow creating the FIRST superadmin for free.
func (r *AdminRepository) CountAdmins(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `select count(*) from admin.admin_users`).Scan(&count)
	return count, err
}

// CreateAdmin inserts a new admin row and fills in the generated fields.
func (r *AdminRepository) CreateAdmin(ctx context.Context, a *models.Admin) error {
	query := `
		insert into admin.admin_users (email, password_hash, display_name, role, mfa_required, status, image_url)
		values ($1, $2, $3, $4, $5, 'active', $6)
		returning id, role, status, created_at, updated_at, image_url`
	role := a.Role
	if role == "" {
		role = "superadmin"
	}
	return r.db.QueryRow(ctx, query, a.Email, a.PasswordHash, a.DisplayName, role, a.MFARequired, a.ImageURL).
		Scan(&a.ID, &a.Role, &a.Status, &a.CreatedAt, &a.UpdatedAt, &a.ImageURL)
}

// GetByEmail fetches an active admin by email, for login.
func (r *AdminRepository) GetByEmail(ctx context.Context, email string) (*models.Admin, error) {
	query := `
		select id, email, password_hash, display_name, role, status, mfa_required, created_at, updated_at, image_url
		from admin.admin_users
		where email = $1
		  and status = 'active'`
	a := &models.Admin{}
	err := r.db.QueryRow(ctx, query, email).Scan(
		&a.ID, &a.Email, &a.PasswordHash, &a.DisplayName, &a.Role, &a.Status, &a.MFARequired, &a.CreatedAt, &a.UpdatedAt,
		&a.ImageURL,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdminNotFound
	}
	return a, err
}

// GetByID fetches an active admin by id.
func (r *AdminRepository) GetByID(ctx context.Context, id string) (*models.Admin, error) {
	query := `
		select id, email, password_hash, display_name, role, status, mfa_required, created_at, updated_at, image_url
		from admin.admin_users
		where id = $1
		  and status = 'active'`
	a := &models.Admin{}
	err := r.db.QueryRow(ctx, query, id).Scan(
		&a.ID, &a.Email, &a.PasswordHash, &a.DisplayName, &a.Role, &a.Status, &a.MFARequired, &a.CreatedAt, &a.UpdatedAt,
		&a.ImageURL,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdminNotFound
	}
	return a, err
}

// Update updates mutable admin profile fields.
func (r *AdminRepository) Update(ctx context.Context, a *models.Admin) error {
	err := r.db.QueryRow(ctx, `
		update admin.admin_users
		set display_name = $2,
		    image_url = $3,
		    updated_at = now()
		where id = $1 and status = 'active'
		returning updated_at`,
		a.ID, a.DisplayName, a.ImageURL,
	).Scan(&a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAdminNotFound
	}
	return err
}