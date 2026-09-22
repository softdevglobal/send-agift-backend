package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

// ErrScoreNotReviewable means the score is not in a state an admin can
// change — a log that failed to replay stays rejected.
var ErrScoreNotReviewable = errors.New("score cannot be reviewed")

// adminPlayer shapes a player for the admin console: customers by name (or
// email), guests by a short tag of their device token.
//
// Guests are shown rather than hidden. They are most of the real activity on
// a game, and an operator judging whether a board looks healthy needs to see
// it. The Kind field is what keeps the distinction honest: only a customer
// can be contacted, age-verified or paid a prize, so the console labels who
// is who rather than quietly dropping half the scores.
func adminPlayer(customerID *uuid.UUID, guest, name, email, country *string) models.AdminPlayer {
	if customerID != nil {
		p := models.AdminPlayer{Kind: "customer", CustomerID: customerID, Email: email, CountryName: country}
		switch {
		case name != nil && strings.TrimSpace(*name) != "":
			p.Name = strings.TrimSpace(*name)
		case email != nil && strings.TrimSpace(*email) != "":
			p.Name = strings.TrimSpace(*email)
		default:
			p.Name = "Customer"
		}
		return p
	}
	tag := ""
	if guest != nil {
		tag = strings.ToUpper(strings.ReplaceAll(*guest, "-", ""))
		if len(tag) > 6 {
			tag = tag[:6]
		}
	}
	return models.AdminPlayer{Kind: "guest", Name: strings.TrimSpace("Guest " + tag)}
}

// AdminGameSummaries returns every game in the catalog with its practice
// activity and best scorer. Counts cover every version of a game.
//
// Guests are counted alongside customers: they are most of the activity on a
// game, and a board that hid them read as empty. Each row still says which it
// is, because only a customer can be paid a prize.
func (r *GameRepository) AdminGameSummaries(ctx context.Context) ([]models.AdminGameSummary, error) {
	rows, err := r.db.Query(ctx, `
		with g as (
			select g.id, g.slug, g.name, g.game_type, g.status, g.created_at,
			       (select v.version from competition.game_versions v
			         where v.game_id = g.id and v.status = 'approved'
			         order by v.created_at desc limit 1) as version
			from competition.games g
		), plays as (
			select v.game_id, count(*) as plays, max(s.started_at) as last_played
			from competition.game_sessions s
			join competition.game_versions v on v.id = s.game_version_id
			where s.mode = 'practice'
			group by v.game_id
		), scores as (
			select v.game_id,
			       count(*) as scores,
			       count(distinct coalesce(sc.customer_id::text, sc.guest_token)) as players,
			       count(*) filter (where sc.validation_status = 'manual_review') as review,
			       count(*) filter (where sc.validation_status = 'rejected') as rejected
			from competition.game_scores sc
			join competition.game_versions v on v.id = sc.game_version_id
			group by v.game_id
		), top as (
			select distinct on (v.game_id)
			       v.game_id, sc.score, sc.customer_id, sc.guest_token
			from competition.game_scores sc
			join competition.game_versions v on v.id = sc.game_version_id
			where sc.validation_status = 'accepted'
			order by v.game_id, sc.score desc, sc.duration_ms asc, sc.created_at asc
		), comps as (
			select v.game_id, count(*) as n
			from competition.competitions c
			join competition.game_versions v on v.id = c.game_version_id
			group by v.game_id
		)
		select g.slug, g.name, g.game_type, g.status, coalesce(g.version, ''),
		       coalesce(p.plays, 0), coalesce(s.scores, 0), coalesce(s.players, 0),
		       coalesce(s.review, 0), coalesce(s.rejected, 0), coalesce(cp.n, 0),
		       p.last_played, t.score, t.customer_id, t.guest_token,
		       c.display_name, c.email, co.name
		from g
		left join plays p on p.game_id = g.id
		left join scores s on s.game_id = g.id
		left join top t on t.game_id = g.id
		left join comps cp on cp.game_id = g.id
		left join customer.customers c on c.id = t.customer_id
		left join core.countries co on co.id = c.country_id
		order by g.created_at asc, g.name asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.AdminGameSummary, 0)
	for rows.Next() {
		var (
			s                    models.AdminGameSummary
			topScore             *int64
			topCustomer          *uuid.UUID
			topGuest             *string
			name, email, country *string
		)
		if err := rows.Scan(&s.Slug, &s.Name, &s.GameType, &s.Status, &s.Version,
			&s.Plays, &s.Scores, &s.Players, &s.UnderReview, &s.Rejected, &s.Competitions,
			&s.LastPlayedAt, &topScore, &topCustomer, &topGuest, &name, &email, &country); err != nil {
			return nil, err
		}
		if topScore != nil {
			s.TopScore = topScore
			p := adminPlayer(topCustomer, topGuest, name, email, country)
			s.TopPlayer = &p
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AdminLeaderboard ranks every player's best accepted score for a game,
// across all its versions, with how often they played.
//
// Guests rank alongside customers — a board that hid them showed nothing on a
// game most people play signed out. Each row still carries which it is.
func (r *GameRepository) AdminLeaderboard(ctx context.Context, slug string, limit int) ([]models.AdminLeaderboardRow, error) {
	rows, err := r.db.Query(ctx, `
		with sc as (
			-- One identity per player, whichever kind they are, so a guest's
			-- runs group together exactly as a customer's do.
			select sc.*, coalesce(sc.customer_id::text, sc.guest_token) as player
			from competition.game_scores sc
			join competition.game_versions v on v.id = sc.game_version_id
			join competition.games g on g.id = v.game_id
			where g.slug = $1
		), agg as (
			select player, count(*) as plays, max(created_at) as last_played
			from sc
			group by player
		), best as (
			select distinct on (player) player, customer_id, guest_token,
			       score, duration_ms, created_at
			from sc
			where validation_status = 'accepted'
			order by player, score desc, duration_ms asc, created_at asc
		)
		select rank() over (order by b.score desc, b.duration_ms asc) as rnk,
		       b.customer_id, b.guest_token, c.display_name, c.email, co.name,
		       b.score, b.duration_ms, a.plays, b.created_at, a.last_played
		from best b
		join agg a on a.player = b.player
		left join customer.customers c on c.id = b.customer_id
		left join core.countries co on co.id = c.country_id
		order by rnk asc, b.created_at asc
		limit $2`, slug, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.AdminLeaderboardRow, 0)
	for rows.Next() {
		var (
			row                  models.AdminLeaderboardRow
			customerID           *uuid.UUID
			guest                *string
			name, email, country *string
		)
		if err := rows.Scan(&row.Rank, &customerID, &guest, &name, &email, &country,
			&row.BestScore, &row.DurationMs, &row.Plays, &row.AchievedAt, &row.LastPlayedAt); err != nil {
			return nil, err
		}
		row.Player = adminPlayer(customerID, guest, name, email, country)
		out = append(out, row)
	}
	return out, rows.Err()
}

// AdminScores lists a game's most recent practice scores, optionally only
// those with one validation status (e.g. manual_review).
//
// Guest submissions are included: the review queue exists to catch impossible
// runs, and a run is no less suspect for having been played signed out.
func (r *GameRepository) AdminScores(ctx context.Context, slug, status string, limit int) ([]models.AdminGameScore, error) {
	rows, err := r.db.Query(ctx, `
		select sc.session_id, sc.customer_id, sc.guest_token, c.display_name, c.email, co.name,
		       sc.score, sc.client_score, sc.moves_count, sc.duration_ms,
		       sc.validation_status, sc.review_reason, sc.stats, sc.created_at
		from competition.game_scores sc
		join competition.game_versions v on v.id = sc.game_version_id
		join competition.games g on g.id = v.game_id
		left join customer.customers c on c.id = sc.customer_id
		left join core.countries co on co.id = c.country_id
		where g.slug = $1 and ($2 = '' or sc.validation_status = $2)
		order by sc.created_at desc
		limit $3`, slug, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.AdminGameScore, 0)
	for rows.Next() {
		var (
			s                    models.AdminGameScore
			customerID           *uuid.UUID
			guest                *string
			name, email, country *string
			stats                []byte
		)
		if err := rows.Scan(&s.SessionID, &customerID, &guest, &name, &email, &country,
			&s.Score, &s.ClientScore, &s.MovesCount, &s.DurationMs,
			&s.ValidationStatus, &s.ReviewReason, &stats, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.Player = adminPlayer(customerID, guest, name, email, country)
		s.Stats = map[string]int64{}
		if len(stats) > 0 {
			if err := json.Unmarshal(stats, &s.Stats); err != nil {
				return nil, err
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ReviewScore records a superadmin's verdict on a practice score, together
// with its audit-log row. Only held or accepted scores can change: a log that
// failed to replay has no valid score to restore.
func (r *GameRepository) ReviewScore(ctx context.Context, sessionID uuid.UUID, status string, reason *string, audit models.AuditEntry) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var before string
	err = tx.QueryRow(ctx, `
		select validation_status from competition.game_scores where session_id = $1 for update`,
		sessionID).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGameScoreNotFound
	}
	if err != nil {
		return err
	}
	if before != "manual_review" && before != "accepted" {
		return ErrScoreNotReviewable
	}

	if _, err := tx.Exec(ctx, `
		update competition.game_scores
		set validation_status = $2, review_reason = coalesce($3, review_reason)
		where session_id = $1`, sessionID, status, reason); err != nil {
		return err
	}
	audit.Before = map[string]any{"validation_status": before}
	audit.After = map[string]any{"validation_status": status, "reviewed_at": time.Now().UTC()}
	if err := insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
