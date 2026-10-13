package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

// ErrPackageNotFound is a point package that does not exist.
var ErrPackageNotFound = errors.New("point package not found")

const packageSelect = `
	select id, name, description, points, bonus_points, price_cents, currency, badge,
	       is_active, sort_order, created_at, updated_at
	from points.point_packages
`

func scanPackage(row scanner) (*models.PointPackage, error) {
	p := &models.PointPackage{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Points, &p.BonusPoints, &p.PriceCents,
		&p.Currency, &p.Badge, &p.IsActive, &p.SortOrder, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPackageNotFound
	}
	return p, err
}

// ListPackages lists point packages in display order: the active ones only,
// or every one for the admin.
func (r *PointsRepository) ListPackages(ctx context.Context, activeOnly bool) ([]models.PointPackage, error) {
	rows, err := r.db.Query(ctx, packageSelect+`
		where ($1 = false or is_active)
		order by sort_order, price_cents, created_at`, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.PointPackage{}
	for rows.Next() {
		p, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPackage reads one package.
func (r *PointsRepository) GetPackage(ctx context.Context, id uuid.UUID) (*models.PointPackage, error) {
	return scanPackage(r.db.QueryRow(ctx, packageSelect+` where id = $1`, id))
}

// CountPackages is how many packages exist, active or not.
func (r *PointsRepository) CountPackages(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `select count(*) from points.point_packages`).Scan(&n)
	return n, err
}

// SavePackage creates a package (p.ID is uuid.Nil) or updates one, and
// writes the audit entry with it when one is given.
func (r *PointsRepository) SavePackage(ctx context.Context, p *models.PointPackage, adminID *uuid.UUID, audit *models.AuditEntry) (*models.PointPackage, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	id := p.ID
	if id == uuid.Nil {
		err = tx.QueryRow(ctx, `
			insert into points.point_packages
				(name, description, points, bonus_points, price_cents, currency, badge,
				 is_active, sort_order, created_by_admin_id, updated_by_admin_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
			returning id`,
			p.Name, p.Description, p.Points, p.BonusPoints, p.PriceCents, p.Currency, p.Badge,
			p.IsActive, p.SortOrder, adminID).Scan(&id)
	} else {
		var tag interface{ RowsAffected() int64 }
		tag, err = tx.Exec(ctx, `
			update points.point_packages
			set name = $2, description = $3, points = $4, bonus_points = $5, price_cents = $6,
			    currency = $7, badge = $8, is_active = $9, sort_order = $10,
			    updated_by_admin_id = $11, updated_at = now()
			where id = $1`,
			id, p.Name, p.Description, p.Points, p.BonusPoints, p.PriceCents, p.Currency, p.Badge,
			p.IsActive, p.SortOrder, adminID)
		if err == nil && tag.RowsAffected() == 0 {
			return nil, ErrPackageNotFound
		}
	}
	if err != nil {
		return nil, err
	}
	if audit != nil {
		audit.EntityID = &id
		audit.After = map[string]any{
			"name": p.Name, "points": p.Points, "bonus_points": p.BonusPoints,
			"price_cents": p.PriceCents, "currency": p.Currency, "is_active": p.IsActive,
		}
		if err := insertAudit(ctx, tx, *audit); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetPackage(ctx, id)
}
