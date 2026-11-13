package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"myapp/internal/models"
)

// States of a country, a customer's own state, and the switches that turn
// games and game entries on for a country, a state and a game.

var (
	ErrStateNotFound = errors.New("state not found")
	// ErrStateDuplicate is a second state with the same code in a country.
	ErrStateDuplicate = errors.New("that country already has a state with this code")
	// ErrStateInUse is deleting a state customers or competitions refer to.
	ErrStateInUse           = errors.New("this state is in use; make it inactive instead")
	ErrJurisdictionNotFound = errors.New("no games settings for that place")
	// ErrStateWrongCountry is a state that is not in the customer's country.
	ErrStateWrongCountry = errors.New("that state is not in your country")
)

// StateChangeBlockedError is a customer changing state too soon.
type StateChangeBlockedError struct{ Until time.Time }

func (e *StateChangeBlockedError) Error() string {
	return "your state can be changed again on " + e.Until.Format("2 January 2006")
}

func scanStates(rows pgx.Rows) ([]models.State, error) {
	defer rows.Close()
	out := []models.State{}
	for rows.Next() {
		var s models.State
		if err := rows.Scan(&s.ID, &s.CountryID, &s.Code, &s.Name, &s.IsActive); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// States lists a country's states by name; activeOnly hides retired ones.
func (r *GameEntriesRepository) States(ctx context.Context, countryID uuid.UUID, activeOnly bool) ([]models.State, error) {
	rows, err := r.db.Query(ctx, `
		select id, country_id, code, name, is_active from core.states
		where country_id = $1 and (is_active or not $2)
		order by name`, countryID, activeOnly)
	if err != nil {
		return nil, err
	}
	return scanStates(rows)
}

// CountryHasStates is true when a country has states a customer must pick from.
func (r *GameEntriesRepository) CountryHasStates(ctx context.Context, countryID uuid.UUID) (bool, error) {
	var has bool
	err := r.db.QueryRow(ctx, `
		select exists(select 1 from core.states where country_id = $1 and is_active)`, countryID).Scan(&has)
	return has, err
}

func mapStateErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return ErrStateDuplicate
		case "23503":
			if strings.Contains(pg.Message, "delete") {
				return ErrStateInUse
			}
			return ErrStateNotFound
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStateNotFound
	}
	return err
}

// CountryISO is a country's ISO 3166-1 alpha-2 code, e.g. "AU".
func (r *GameEntriesRepository) CountryISO(ctx context.Context, countryID uuid.UUID) (string, error) {
	var iso string
	err := r.db.QueryRow(ctx, `select iso_code from core.countries where id = $1`, countryID).Scan(&iso)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrStateNotFound
	}
	return iso, err
}

// FindOrCreateState returns the country's state called [name], adding it
// with [code] when it is new. A code already used by a different state
// gets a number on the end (WP, WP2, ...). Two customers picking the same
// new state at once end up with the one row.
func (r *GameEntriesRepository) FindOrCreateState(ctx context.Context, countryID uuid.UUID, code, name string) (*models.State, error) {
	byName := func() (*models.State, error) {
		st := &models.State{}
		err := r.db.QueryRow(ctx, `
			select id, country_id, code, name, is_active from core.states
			where country_id = $1 and lower(name) = lower($2)
			order by is_active desc, created_at limit 1`, countryID, name).
			Scan(&st.ID, &st.CountryID, &st.Code, &st.Name, &st.IsActive)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return st, err
	}
	for attempt := 1; attempt <= 9; attempt++ {
		if st, err := byName(); err != nil || st != nil {
			return st, err
		}
		try := code
		if attempt > 1 {
			suffix := fmt.Sprint(attempt)
			if len(try)+len(suffix) > 10 {
				try = try[:10-len(suffix)]
			}
			try += suffix
		}
		st := &models.State{}
		err := r.db.QueryRow(ctx, `
			insert into core.states (country_id, code, name, is_active) values ($1, $2, $3, true)
			on conflict (country_id, code) do nothing
			returning id, country_id, code, name, is_active`, countryID, try, name).
			Scan(&st.ID, &st.CountryID, &st.Code, &st.Name, &st.IsActive)
		if err == nil {
			return st, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, mapStateErr(err)
		}
	}
	return nil, ErrStateDuplicate
}

// SaveState creates (id nil) or updates a state, audited.
func (r *GameEntriesRepository) SaveState(ctx context.Context, id *uuid.UUID, s *models.State, audit models.AuditEntry) (*models.State, error) {
	out := &models.State{}
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var row pgx.Row
		if id == nil {
			row = tx.QueryRow(ctx, `
				insert into core.states (country_id, code, name, is_active) values ($1, $2, $3, $4)
				returning id, country_id, code, name, is_active`, s.CountryID, s.Code, s.Name, s.IsActive)
		} else {
			row = tx.QueryRow(ctx, `
				update core.states set code = $2, name = $3, is_active = $4, updated_at = now()
				where id = $1
				returning id, country_id, code, name, is_active`, *id, s.Code, s.Name, s.IsActive)
		}
		if err := row.Scan(&out.ID, &out.CountryID, &out.Code, &out.Name, &out.IsActive); err != nil {
			return mapStateErr(err)
		}
		audit.EntityID = &out.ID
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

// DeleteState removes a state nothing refers to.
func (r *GameEntriesRepository) DeleteState(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from core.states where id = $1`, id)
		if err != nil {
			return mapStateErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrStateNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}

// CustomerState is a customer's state and whether they still owe us one.
type CustomerState struct {
	CountryID     uuid.UUID     `json:"country_id"`
	State         *models.State `json:"state"`
	StateSetAt    *time.Time    `json:"state_set_at"`
	StateSource   *string       `json:"state_source"`
	StateRequired bool          `json:"state_required"`
	// CanChangeAt is when a state already given can next be changed.
	CanChangeAt   *time.Time `json:"can_change_at"`
	PhoneVerified bool       `json:"phone_verified"`
}

// CustomerState reads a customer's state. StateRequired is true when their
// country has states and they have not given one.
func (r *GameEntriesRepository) CustomerState(ctx context.Context, customerID uuid.UUID, cooldown time.Duration) (*CustomerState, error) {
	out := &CustomerState{}
	var (
		sid        *uuid.UUID
		code, name *string
		active     *bool
		hasStates  bool
	)
	err := r.db.QueryRow(ctx, `
		select c.country_id, c.state_id, s.code, s.name, s.is_active, c.state_set_at, c.state_source,
		       exists(select 1 from core.states x where x.country_id = c.country_id and x.is_active),
		       c.phone_verified_at is not null
		from customer.customers c
		left join core.states s on s.id = c.state_id
		where c.id = $1`, customerID).
		Scan(&out.CountryID, &sid, &code, &name, &active, &out.StateSetAt, &out.StateSource,
			&hasStates, &out.PhoneVerified)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntriesCustomerNotFound
	}
	if err != nil {
		return nil, err
	}
	if sid != nil {
		out.State = &models.State{ID: *sid, CountryID: out.CountryID, Code: *code, Name: *name, IsActive: *active}
		if out.StateSetAt != nil {
			t := out.StateSetAt.Add(cooldown)
			out.CanChangeAt = &t
		}
	}
	out.StateRequired = hasStates && sid == nil
	return out, nil
}

// SetCustomerState records the state a customer says they live in. The
// first answer is taken as given. Changing it later is limited so that
// nobody moves state to enter a competition: only after the cooldown, and
// only with a verified phone number. The state must be an active one in
// the customer's own country. It is marked as backed by an address when
// one of their saved addresses is in that state.
func (r *GameEntriesRepository) SetCustomerState(ctx context.Context, customerID, stateID uuid.UUID, cooldown time.Duration) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		var (
			countryID     uuid.UUID
			current       *uuid.UUID
			setAt         *time.Time
			phoneVerified bool
		)
		err := tx.QueryRow(ctx, `
			select country_id, state_id, state_set_at, phone_verified_at is not null
			from customer.customers where id = $1 and deleted_at is null for update`, customerID).
			Scan(&countryID, &current, &setAt, &phoneVerified)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrEntriesCustomerNotFound
		}
		if err != nil {
			return err
		}
		if current != nil && *current == stateID {
			return nil
		}
		var (
			stateCountry uuid.UUID
			code, name   string
			active       bool
		)
		err = tx.QueryRow(ctx, `
			select country_id, code, name, is_active from core.states where id = $1`, stateID).
			Scan(&stateCountry, &code, &name, &active)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
			return ErrStateNotFound
		}
		if err != nil {
			return err
		}
		if stateCountry != countryID {
			return ErrStateWrongCountry
		}
		if current != nil {
			if setAt != nil && time.Now().Before(setAt.Add(cooldown)) {
				return &StateChangeBlockedError{Until: setAt.Add(cooldown)}
			}
			if !phoneVerified {
				return ErrStateNeedsPhone
			}
		}
		var byAddress bool
		if err := tx.QueryRow(ctx, `
			select exists(
				select 1 from customer.customer_addresses a
				where a.customer_id = $1 and a.country_id = $2
				  and lower(trim(coalesce(a.region, ''))) in (lower($3), lower($4)))`,
			customerID, countryID, code, name).Scan(&byAddress); err != nil {
			return err
		}
		source := "declared"
		if byAddress {
			source = "address"
		}
		_, err = tx.Exec(ctx, `
			update customer.customers
			set state_id = $2, state_set_at = now(), state_source = $3, updated_at = now()
			where id = $1`, customerID, stateID, source)
		return err
	})
}

// ErrStateNeedsPhone is changing state without a verified phone number.
var ErrStateNeedsPhone = errors.New("verify your phone number before changing your state")

// StateOfCountry checks a state is an active one of a country, for sign-up.
func (r *GameEntriesRepository) StateOfCountry(ctx context.Context, stateID, countryID uuid.UUID) error {
	var ok bool
	if err := r.db.QueryRow(ctx, `
		select exists(select 1 from core.states where id = $1 and country_id = $2 and is_active)`,
		stateID, countryID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrStateWrongCountry
	}
	return nil
}

// ─── Programme and jurisdictions ──────────────────────────────────────────

func switchState(on bool, stopped *time.Time) string {
	switch {
	case stopped != nil:
		return "STOPPED"
	case on:
		return "ON"
	}
	return "OFF"
}

// Program is the global switch.
func (r *GameEntriesRepository) Program(ctx context.Context) (*models.EntryProgram, error) {
	p := &models.EntryProgram{}
	if err := r.db.QueryRow(ctx, `
		select enabled, stopped_at, stopped_reason from entries.program`).
		Scan(&p.Enabled, &p.StoppedAt, &p.StoppedReason); err != nil {
		return nil, err
	}
	p.State = switchState(p.Enabled, p.StoppedAt)
	return p, nil
}

// SetProgram turns the whole programme on or off. Turning it on also lifts
// a stop.
func (r *GameEntriesRepository) SetProgram(ctx context.Context, enabled bool, adminID uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			update entries.program
			set enabled = $1, stopped_at = null, stopped_reason = null,
			    updated_by_admin_id = $2, updated_at = now()`, enabled, adminID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// StopProgram is the global emergency stop: earning and new plays end at
// once, everywhere. Refunds and what is already owed carry on.
func (r *GameEntriesRepository) StopProgram(ctx context.Context, reason string, adminID uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			update entries.program
			set stopped_at = now(), stopped_reason = $1, updated_by_admin_id = $2, updated_at = now()`,
			reason, adminID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

const jurisdictionCols = `j.id, j.country_id, co.name, j.state_id, s.name, j.earning_enabled, j.play_enabled,
	j.chance_games_enabled, j.min_age, j.official_rules, j.customer_daily_cap, j.product_daily_cap,
	j.free_entries_per_day, j.stopped_at, j.stopped_reason, j.updated_at,
	coalesce((select array_agg(jg.game_id) from entries.jurisdiction_games jg
	           where jg.jurisdiction_id = j.id and jg.enabled), '{}')
	from entries.jurisdictions j
	inner join core.countries co on co.id = j.country_id
	left join core.states s on s.id = j.state_id`

func scanJurisdiction(row scanner) (*models.Jurisdiction, error) {
	j := &models.Jurisdiction{}
	err := row.Scan(&j.ID, &j.CountryID, &j.CountryName, &j.StateID, &j.StateName, &j.EarningEnabled,
		&j.PlayEnabled, &j.ChanceGamesEnabled, &j.MinAge, &j.OfficialRules, &j.CustomerDailyCap,
		&j.ProductDailyCap, &j.FreeEntriesPerDay, &j.StoppedAt, &j.StoppedReason, &j.UpdatedAt, &j.GameIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrJurisdictionNotFound
	}
	if err != nil {
		return nil, err
	}
	j.State = switchState(j.EarningEnabled || j.PlayEnabled, j.StoppedAt)
	return j, nil
}

// Jurisdictions lists every place that has games settings.
func (r *GameEntriesRepository) Jurisdictions(ctx context.Context) ([]models.Jurisdiction, error) {
	rows, err := r.db.Query(ctx, `select `+jurisdictionCols+` order by co.name, s.name nulls first`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Jurisdiction{}
	for rows.Next() {
		j, err := scanJurisdiction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// SaveJurisdiction writes the settings of a country or state and the games
// switched on there, replacing what was set. Saving lifts nothing: a
// stopped place stays stopped until LiftJurisdictionStop.
func (r *GameEntriesRepository) SaveJurisdiction(ctx context.Context, j *models.Jurisdiction, adminID uuid.UUID, audit models.AuditEntry) (*models.Jurisdiction, error) {
	var out *models.Jurisdiction
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		if j.StateID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `
				select exists(select 1 from core.states where id = $1 and country_id = $2)`,
				*j.StateID, j.CountryID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return ErrStateWrongCountry
			}
		}
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			select id from entries.jurisdictions
			where country_id = $1 and state_id is not distinct from $2 for update`, j.CountryID, j.StateID).Scan(&id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			err = tx.QueryRow(ctx, `
				insert into entries.jurisdictions (country_id, state_id, earning_enabled, play_enabled,
					chance_games_enabled, min_age, official_rules, customer_daily_cap, product_daily_cap,
					free_entries_per_day, updated_by_admin_id)
				values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
				returning id`,
				j.CountryID, j.StateID, j.EarningEnabled, j.PlayEnabled, j.ChanceGamesEnabled, j.MinAge,
				j.OfficialRules, j.CustomerDailyCap, j.ProductDailyCap, j.FreeEntriesPerDay, adminID).Scan(&id)
		case err == nil:
			_, err = tx.Exec(ctx, `
				update entries.jurisdictions
				set earning_enabled = $2, play_enabled = $3, chance_games_enabled = $4, min_age = $5,
				    official_rules = $6, customer_daily_cap = $7, product_daily_cap = $8,
				    free_entries_per_day = $9, updated_by_admin_id = $10, updated_at = now()
				where id = $1`,
				id, j.EarningEnabled, j.PlayEnabled, j.ChanceGamesEnabled, j.MinAge,
				j.OfficialRules, j.CustomerDailyCap, j.ProductDailyCap, j.FreeEntriesPerDay, adminID)
		}
		if err != nil {
			return mapStateErr(err)
		}
		if _, err := tx.Exec(ctx, `delete from entries.jurisdiction_games where jurisdiction_id = $1`, id); err != nil {
			return err
		}
		for _, g := range j.GameIDs {
			if _, err := tx.Exec(ctx, `
				insert into entries.jurisdiction_games (jurisdiction_id, game_id, enabled) values ($1, $2, true)
				on conflict do nothing`, id, g); err != nil {
				return mapStateErr(err)
			}
		}
		audit.EntityID = &id
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = scanJurisdiction(tx.QueryRow(ctx, `select `+jurisdictionCols+` where j.id = $1`, id))
		return err
	})
	return out, err
}

// StopJurisdiction is the emergency stop for one place; lift undoes it.
func (r *GameEntriesRepository) StopJurisdiction(ctx context.Context, id uuid.UUID, stop bool, reason string, adminID uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		sql := `update entries.jurisdictions
			set stopped_at = null, stopped_reason = null, updated_by_admin_id = $2, updated_at = now()
			where id = $1`
		args := []any{id, adminID}
		if stop {
			sql = `update entries.jurisdictions
				set stopped_at = now(), stopped_reason = $3, updated_by_admin_id = $2, updated_at = now()
				where id = $1`
			args = append(args, reason)
		}
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrJurisdictionNotFound
		}
		return insertAudit(ctx, tx, audit)
	})
}
