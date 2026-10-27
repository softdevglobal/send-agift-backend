package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrApprovalNotFound     = errors.New("approval not found")
	ErrApprovalSamePerson   = errors.New("the admin who requested an approval cannot approve it")
	ErrApprovalNotPending   = errors.New("only a pending approval can be approved or rejected")
	ErrStopNotFound         = errors.New("emergency stop not found")
	ErrStopAlreadyLifted    = errors.New("that emergency stop was already lifted")
	ErrVersionNotFound      = errors.New("version not found")
	ErrVersionNotDraft      = errors.New("only a draft can be edited or submitted")
	ErrVersionNotAwaiting   = errors.New("only a version awaiting approval can be approved")
	ErrVersionSamePerson    = errors.New("the admin who submitted a version cannot approve it")
	ErrVersionBadTransition = errors.New("that status change is not allowed")
)

// CompetitionPointsRepository is competition points, memberships, the
// reward engine, entries and the approval records that gate them.
type CompetitionPointsRepository struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func NewCompetitionPointsRepository(db *pgxpool.Pool) *CompetitionPointsRepository {
	return &CompetitionPointsRepository{db: db, now: time.Now}
}

func (r *CompetitionPointsRepository) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const approvalColumns = `a.id, a.operation, a.country_id, co.name, a.channel, a.provider, a.point_source,
	a.min_age, a.evidence_ref, a.notes, a.status, a.effective_from, a.expires_at,
	a.requested_by_admin_id, a.approved_by_admin_id, a.approved_at, a.revoked_at, a.revoke_reason,
	a.created_at, (a.status = 'active' and a.effective_from <= now() and a.expires_at > now())`

func scanApproval(row scanner) (*models.CapabilityApproval, error) {
	var a models.CapabilityApproval
	err := row.Scan(&a.ID, &a.Operation, &a.CountryID, &a.CountryName, &a.Channel, &a.Provider,
		&a.PointSource, &a.MinAge, &a.EvidenceRef, &a.Notes, &a.Status, &a.EffectiveFrom, &a.ExpiresAt,
		&a.RequestedByAdminID, &a.ApprovedByAdminID, &a.ApprovedAt, &a.RevokedAt, &a.RevokeReason,
		&a.CreatedAt, &a.Current)
	return &a, err
}

// ApprovalQuery finds the approvals that permit one operation now.
type ApprovalQuery struct {
	Operation string
	CountryID uuid.UUID
	// Channel is matched exactly or by an approval for 'any'.
	Channel string
	// Provider, when set, must be the approval's own provider: a connected
	// account does not by itself approve every operation on it.
	Provider *string
}

// CurrentApprovals lists the active, in-window approvals matching q.
func (r *CompetitionPointsRepository) CurrentApprovals(ctx context.Context, q ApprovalQuery) ([]models.CapabilityApproval, error) {
	return currentApprovals(ctx, r.db, q)
}

func currentApprovals(ctx context.Context, db querier, q ApprovalQuery) ([]models.CapabilityApproval, error) {
	rows, err := db.Query(ctx, `
		select `+approvalColumns+`
		from core.capability_approvals a
		inner join core.countries co on co.id = a.country_id
		where a.status = 'active' and a.effective_from <= now() and a.expires_at > now()
		  and a.operation = $1 and a.country_id = $2
		  and (a.channel = $3 or a.channel = 'any')
		  and ($4::text is null or a.provider = $4)
		order by a.created_at`, q.Operation, q.CountryID, q.Channel, q.Provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.CapabilityApproval, 0)
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ListApprovals is every approval record, newest first.
func (r *CompetitionPointsRepository) ListApprovals(ctx context.Context) ([]models.CapabilityApproval, error) {
	rows, err := r.db.Query(ctx, `
		select `+approvalColumns+`
		from core.capability_approvals a
		inner join core.countries co on co.id = a.country_id
		order by a.created_at desc
		limit 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.CapabilityApproval, 0)
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *CompetitionPointsRepository) getApproval(ctx context.Context, q querier, id uuid.UUID, lock bool) (*models.CapabilityApproval, error) {
	sql := `select ` + approvalColumns + `
		from core.capability_approvals a
		inner join core.countries co on co.id = a.country_id
		where a.id = $1`
	if lock {
		sql += ` for update of a`
	}
	a, err := scanApproval(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrApprovalNotFound
	}
	return a, err
}

// RequestApproval records evidence for one operation, pending a second
// person's approval.
func (r *CompetitionPointsRepository) RequestApproval(ctx context.Context, a *models.CapabilityApproval, audit models.AuditEntry) (*models.CapabilityApproval, error) {
	var out *models.CapabilityApproval
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			insert into core.capability_approvals
				(operation, country_id, channel, provider, point_source, min_age, evidence_ref, notes,
				 effective_from, expires_at, requested_by_admin_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			returning id`,
			a.Operation, a.CountryID, a.Channel, a.Provider, a.PointSource, a.MinAge, a.EvidenceRef,
			a.Notes, a.EffectiveFrom, a.ExpiresAt, a.RequestedByAdminID).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrCountryNotFound
		}
		if err != nil {
			return err
		}
		audit.EntityID = &id
		audit.After = map[string]any{"operation": a.Operation, "country_id": a.CountryID, "channel": a.Channel,
			"provider": a.Provider, "evidence_ref": a.EvidenceRef, "expires_at": a.ExpiresAt}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = r.getApproval(ctx, tx, id, false)
		return err
	})
	return out, err
}

// DecideApproval approves or rejects a pending approval. The approver must
// be a different admin from the one who requested it.
func (r *CompetitionPointsRepository) DecideApproval(ctx context.Context, id, adminID uuid.UUID, approve bool, audit models.AuditEntry) (*models.CapabilityApproval, error) {
	var out *models.CapabilityApproval
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		a, err := r.getApproval(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if a.Status != "pending" {
			return ErrApprovalNotPending
		}
		if a.RequestedByAdminID == adminID {
			return ErrApprovalSamePerson
		}
		status := "rejected"
		if approve {
			status = "active"
		}
		if _, err := tx.Exec(ctx, `
			update core.capability_approvals
			set status = $2, approved_by_admin_id = $3, approved_at = now()
			where id = $1`, id, status, adminID); err != nil {
			return err
		}
		audit.EntityID = &id
		audit.Before = map[string]any{"status": a.Status}
		audit.After = map[string]any{"status": status}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = r.getApproval(ctx, tx, id, false)
		return err
	})
	return out, err
}

// RevokeApproval withdraws an approval at once. New affected operations
// stop; what customers already hold is untouched.
func (r *CompetitionPointsRepository) RevokeApproval(ctx context.Context, id, adminID uuid.UUID, reason string, audit models.AuditEntry) (*models.CapabilityApproval, error) {
	var out *models.CapabilityApproval
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		a, err := r.getApproval(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if a.Status == "revoked" || a.Status == "rejected" {
			out = a
			return nil
		}
		if _, err := tx.Exec(ctx, `
			update core.capability_approvals
			set status = 'revoked', revoked_by_admin_id = $2, revoked_at = now(), revoke_reason = $3
			where id = $1`, id, adminID, reason); err != nil {
			return err
		}
		audit.EntityID = &id
		audit.Before = map[string]any{"status": a.Status}
		audit.After = map[string]any{"status": "revoked", "reason": reason}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = r.getApproval(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ─── Emergency stops ─────────────────────────────────────────────────────

const stopColumns = `s.id, s.scope, s.country_id, co.name, s.reason, s.activated_by_admin_id,
	s.activated_at, s.lifted_at, s.lift_reason`

func scanStop(row scanner) (*models.EmergencyStop, error) {
	var s models.EmergencyStop
	err := row.Scan(&s.ID, &s.Scope, &s.CountryID, &s.CountryName, &s.Reason, &s.ActivatedByAdminID,
		&s.ActivatedAt, &s.LiftedAt, &s.LiftReason)
	return &s, err
}

// StopActive is whether a stop covers scope in this country now.
func (r *CompetitionPointsRepository) StopActive(ctx context.Context, scope string, countryID uuid.UUID) (bool, error) {
	return stopActive(ctx, r.db, scope, countryID)
}

func stopActive(ctx context.Context, q querier, scope string, countryID uuid.UUID) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `
		select exists (select 1 from core.emergency_stops
		               where scope = $1 and lifted_at is null
		                 and (country_id is null or country_id = $2))`, scope, countryID).Scan(&on)
	return on, err
}

// ListStops lists stops, active ones first.
func (r *CompetitionPointsRepository) ListStops(ctx context.Context) ([]models.EmergencyStop, error) {
	rows, err := r.db.Query(ctx, `
		select `+stopColumns+`
		from core.emergency_stops s
		left join core.countries co on co.id = s.country_id
		order by (s.lifted_at is null) desc, s.activated_at desc
		limit 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.EmergencyStop, 0)
	for rows.Next() {
		s, err := scanStop(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ActivateStop starts a stop; it takes effect for the next request.
func (r *CompetitionPointsRepository) ActivateStop(ctx context.Context, scope string, countryID *uuid.UUID, reason string, adminID uuid.UUID, audit models.AuditEntry) (*models.EmergencyStop, error) {
	var out *models.EmergencyStop
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			insert into core.emergency_stops (scope, country_id, reason, activated_by_admin_id)
			values ($1, $2, $3, $4) returning id`, scope, countryID, reason, adminID).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrCountryNotFound
		}
		if err != nil {
			return err
		}
		audit.EntityID = &id
		audit.After = map[string]any{"scope": scope, "country_id": countryID, "reason": reason}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = scanStop(tx.QueryRow(ctx, `select `+stopColumns+`
			from core.emergency_stops s left join core.countries co on co.id = s.country_id
			where s.id = $1`, id))
		return err
	})
	return out, err
}

// LiftStop ends a stop.
func (r *CompetitionPointsRepository) LiftStop(ctx context.Context, id, adminID uuid.UUID, reason string, audit models.AuditEntry) (*models.EmergencyStop, error) {
	var out *models.EmergencyStop
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update core.emergency_stops
			set lifted_at = now(), lifted_by_admin_id = $2, lift_reason = $3
			where id = $1 and lifted_at is null`, id, adminID, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from core.emergency_stops where id = $1)`, id).
				Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrStopNotFound
			}
			return ErrStopAlreadyLifted
		}
		audit.EntityID = &id
		audit.After = map[string]any{"lifted": true, "reason": reason}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = scanStop(tx.QueryRow(ctx, `select `+stopColumns+`
			from core.emergency_stops s left join core.countries co on co.id = s.country_id
			where s.id = $1`, id))
		return err
	})
	return out, err
}

// ─── Customer evidence ───────────────────────────────────────────────────

// CustomerEvidence is what the eligibility engine may rely on. An account
// country alone is never enough: residence also needs a verified phone
// number from that country.
type CustomerEvidence struct {
	CustomerID      uuid.UUID
	Status          string
	Deleted         bool
	CountryID       uuid.UUID
	CountryISO      string
	CountryName     string
	Currency        string
	PhoneE164       *string
	PhoneVerifiedAt *time.Time
	DateOfBirth     *time.Time
	EmailVerifiedAt *time.Time
	Email           string
}

// Evidence loads a customer's eligibility evidence.
func (r *CompetitionPointsRepository) Evidence(ctx context.Context, customerID uuid.UUID) (*CustomerEvidence, error) {
	return loadEvidence(ctx, r.db, customerID)
}

func loadEvidence(ctx context.Context, q querier, customerID uuid.UUID) (*CustomerEvidence, error) {
	var e CustomerEvidence
	err := q.QueryRow(ctx, `
		select c.id, c.status, c.deleted_at is not null, c.country_id, co.iso_code, co.name,
		       co.default_currency, c.phone_e164, c.phone_verified_at, c.date_of_birth,
		       c.email_verified_at, coalesce(c.email::text, '')
		from customer.customers c
		inner join core.countries co on co.id = c.country_id
		where c.id = $1`, customerID).
		Scan(&e.CustomerID, &e.Status, &e.Deleted, &e.CountryID, &e.CountryISO, &e.CountryName,
			&e.Currency, &e.PhoneE164, &e.PhoneVerifiedAt, &e.DateOfBirth, &e.EmailVerifiedAt, &e.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCustomerNotFound
	}
	return &e, err
}
