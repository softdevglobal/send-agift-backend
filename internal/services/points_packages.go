package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// ErrPackageNotFound is a point package that does not exist, or that is not
// on sale.
var ErrPackageNotFound = errors.New("point package not found")

// Limits on one package.
const (
	maxPackagePoints = 10_000_000
	maxBonusPercent  = 100 // a bonus may at most double the points
)

// defaultPackageCents are the bundles sellers were offered before Super
// Admins could set their own ($5, $10, $25, $100), seeded once at the
// configured rate when there are no packages at all.
var defaultPackageCents = []struct {
	cents int64
	name  string
	badge string
}{
	{500, "Starter", ""},
	{1000, "Regular", ""},
	{2500, "Popular", "Most popular"},
	{10000, "Pro", "Best value"},
}

// EnsureDefaultPackages creates the starting packages when none exist yet,
// so sellers always have something to pick.
func (s *SellerPointsService) EnsureDefaultPackages(ctx context.Context) error {
	n, err := s.points.CountPackages(ctx)
	if err != nil || n > 0 {
		return err
	}
	rate := int64(s.rate.CentsPerPoint)
	for i, d := range defaultPackageCents {
		var badge *string
		if d.badge != "" {
			badge = &d.badge
		}
		if _, err := s.points.SavePackage(ctx, &models.PointPackage{
			Name: d.name, Points: d.cents / rate, PriceCents: d.cents, Currency: s.rate.Currency,
			Badge: badge, IsActive: true, SortOrder: i,
		}, nil, nil); err != nil {
			return err
		}
	}
	log.Printf("points: created %d starting point packages", len(defaultPackageCents))
	return nil
}

// SellerPackages are the packages on sale, in display order. Only those in
// the configured currency can be bought.
func (s *SellerPointsService) SellerPackages(ctx context.Context) ([]models.PointPackage, error) {
	all, err := s.points.ListPackages(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]models.PointPackage, 0, len(all))
	for _, p := range all {
		if strings.EqualFold(p.Currency, s.rate.Currency) {
			out = append(out, p)
		}
	}
	return out, nil
}

// packageToBuy is a package a seller may buy now.
func (s *SellerPointsService) packageToBuy(ctx context.Context, rawID string) (*models.PointPackage, error) {
	id, err := uuid.Parse(strings.TrimSpace(rawID))
	if err != nil {
		return nil, ErrPackageNotFound
	}
	p, err := s.points.GetPackage(ctx, id)
	if errors.Is(err, repository.ErrPackageNotFound) {
		return nil, ErrPackageNotFound
	}
	if err != nil {
		return nil, err
	}
	if !p.IsActive || !strings.EqualFold(p.Currency, s.rate.Currency) {
		return nil, ErrPackageNotFound
	}
	return p, nil
}

// AdminPackages is every package, on sale or not.
func (s *SellerPointsService) AdminPackages(ctx context.Context) ([]models.PointPackage, error) {
	return s.points.ListPackages(ctx, false)
}

// PackageInput is an admin setting up a package.
type PackageInput struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Points      int64   `json:"points"`
	BonusPoints int64   `json:"bonus_points"`
	PriceCents  int64   `json:"price_cents"`
	Badge       *string `json:"badge"`
	IsActive    *bool   `json:"is_active"`
	SortOrder   int     `json:"sort_order"`
}

func optionalText(v *string) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimSpace(*v)
	if t == "" {
		return nil
	}
	return &t
}

// validatePackage checks a package and turns it into the stored shape. Its
// price is in the configured currency.
func (s *SellerPointsService) validatePackage(in PackageInput) (*models.PointPackage, error) {
	name := strings.TrimSpace(in.Name)
	desc, badge := optionalText(in.Description), optionalText(in.Badge)
	switch {
	case name == "" || len([]rune(name)) > 60:
		return nil, fmt.Errorf("%w: name must be 1 to 60 characters", ErrInvalidPurchase)
	case desc != nil && len([]rune(*desc)) > 200:
		return nil, fmt.Errorf("%w: description must be at most 200 characters", ErrInvalidPurchase)
	case badge != nil && len([]rune(*badge)) > 24:
		return nil, fmt.Errorf("%w: badge must be at most 24 characters", ErrInvalidPurchase)
	case in.Points <= 0 || in.Points > maxPackagePoints:
		return nil, fmt.Errorf("%w: points must be 1 to %d", ErrInvalidPurchase, maxPackagePoints)
	case in.BonusPoints < 0 || in.BonusPoints*100 > in.Points*maxBonusPercent:
		return nil, fmt.Errorf("%w: bonus points must be 0 to %d%% of the points", ErrInvalidPurchase, maxBonusPercent)
	case in.PriceCents < minPurchaseCents || in.PriceCents > maxPurchaseCents:
		return nil, fmt.Errorf("%w: price must be %d to %d cents", ErrInvalidPurchase, minPurchaseCents, maxPurchaseCents)
	}
	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}
	return &models.PointPackage{
		Name: name, Description: desc, Points: in.Points, BonusPoints: in.BonusPoints,
		PriceCents: in.PriceCents, Currency: s.rate.Currency, Badge: badge,
		IsActive: active, SortOrder: in.SortOrder,
	}, nil
}

// AdminSavePackage creates a package (id is uuid.Nil) or updates one,
// audited. A package already bought keeps its history: purchases store the
// name and points they were bought at.
func (s *SellerPointsService) AdminSavePackage(ctx context.Context, admin AdminActor, id uuid.UUID, in PackageInput) (*models.PointPackage, error) {
	p, err := s.validatePackage(in)
	if err != nil {
		return nil, err
	}
	p.ID = id
	action := "points.package_created"
	if id != uuid.Nil {
		action = "points.package_updated"
	}
	audit := admin.audit(action, id, nil)
	audit.EntityType = "point_package"
	adminID := admin.ID
	saved, err := s.points.SavePackage(ctx, p, &adminID, &audit)
	if errors.Is(err, repository.ErrPackageNotFound) {
		return nil, ErrPackageNotFound
	}
	return saved, err
}
