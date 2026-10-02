package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

// PushRepository stores the app installs that can receive push
// notifications, and the outbox of notifications waiting to be sent.
type PushRepository struct {
	db *pgxpool.Pool
}

func NewPushRepository(db *pgxpool.Pool) *PushRepository {
	return &PushRepository{db: db}
}

// UpsertDevice records an app install for a customer. A token already held
// by another customer moves to this one: the device is now signed in as them.
func (r *PushRepository) UpsertDevice(ctx context.Context, customerID uuid.UUID, token, platform string, appVersion *string) error {
	_, err := r.db.Exec(ctx, `
		insert into customer.push_devices (customer_id, token, platform, app_version)
		values ($1, $2, $3, $4)
		on conflict (token) do update
		set customer_id = excluded.customer_id, platform = excluded.platform,
		    app_version = excluded.app_version, last_seen_at = now()`,
		customerID, token, platform, appVersion)
	return err
}

// DeleteDevice forgets an install, on sign-out. Only the customer it belongs
// to can remove it.
func (r *PushRepository) DeleteDevice(ctx context.Context, customerID uuid.UUID, token string) error {
	_, err := r.db.Exec(ctx, `
		delete from customer.push_devices where customer_id = $1 and token = $2`, customerID, token)
	return err
}

// DeleteTokens drops tokens Firebase reported as no longer valid (the app
// was uninstalled, or the token was replaced).
func (r *PushRepository) DeleteTokens(ctx context.Context, tokens []string) error {
	if len(tokens) == 0 {
		return nil
	}
	_, err := r.db.Exec(ctx, `delete from customer.push_devices where token = any($1)`, tokens)
	return err
}

// queueCompetitionAnnouncement writes one notification for every active
// customer in the competition's countries. It runs inside the transaction
// that publishes the competition, so an announcement is queued exactly when
// the competition goes out, and never for one that failed to publish.
func queueCompetitionAnnouncement(ctx context.Context, q querier, competitionID uuid.UUID, msg models.PushMessage) (int64, error) {
	data, err := json.Marshal(msg.Data)
	if err != nil {
		return 0, err
	}
	tag, err := q.Exec(ctx, `
		insert into core.push_notifications (customer_id, kind, competition_id, title, body, data)
		select cu.id, 'competition_announced', $1, $2, $3, $4
		from customer.customers cu
		inner join competition.competition_countries cc
		        on cc.country_id = cu.country_id and cc.competition_id = $1
		where cu.status = 'active'
		on conflict (kind, competition_id, customer_id) do nothing`,
		competitionID, msg.Title, msg.Body, data)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PendingPush is one queued notification with the devices to send it to.
type PendingPush struct {
	ID       uuid.UUID
	Attempts int
	Message  models.PushMessage
	Tokens   []string
	// Stale is set when the notification is no longer worth sending: its
	// competition was cancelled or has already closed.
	Stale bool
}

// DuePushes returns up to limit notifications that are due, oldest first.
// Only one API server runs the sender at a time (an advisory lock), so the
// rows need no claiming.
func (r *PushRepository) DuePushes(ctx context.Context, limit int) ([]PendingPush, error) {
	rows, err := r.db.Query(ctx, `
		select n.id, n.attempts, n.title, n.body, n.data,
		       coalesce(array_agg(d.token) filter (where d.token is not null), '{}'),
		       coalesce(c.status not in ('scheduled', 'live') or c.ends_at <= now(), false)
		from core.push_notifications n
		left join customer.push_devices d on d.customer_id = n.customer_id
		left join competition.competitions c on c.id = n.competition_id
		where n.status = 'pending' and n.next_attempt_at <= now()
		group by n.id, c.status, c.ends_at
		order by n.next_attempt_at
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]PendingPush, 0)
	for rows.Next() {
		var p PendingPush
		var data []byte
		if err := rows.Scan(&p.ID, &p.Attempts, &p.Message.Title, &p.Message.Body, &data,
			&p.Tokens, &p.Stale); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &p.Message.Data); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkPush records how a send went: sent, skipped or failed, or — with a
// retry time — left pending for another try.
func (r *PushRepository) MarkPush(ctx context.Context, id uuid.UUID, status string, lastError *string, retryAt *time.Time) error {
	_, err := r.db.Exec(ctx, `
		update core.push_notifications
		set status = $2, last_error = $3, attempts = attempts + 1,
		    next_attempt_at = coalesce($4, next_attempt_at),
		    sent_at = case when $2 = 'sent' then now() else sent_at end
		where id = $1`, id, status, lastError, retryAt)
	return err
}

// AnnouncementStats counts how a competition's announcement went.
func AnnouncementStats(ctx context.Context, q querier, competitionID uuid.UUID) (models.AnnouncementStats, error) {
	var st models.AnnouncementStats
	err := q.QueryRow(ctx, `
		select count(*),
		       count(*) filter (where status = 'pending'),
		       count(*) filter (where status = 'sent'),
		       count(*) filter (where status = 'skipped'),
		       count(*) filter (where status = 'failed')
		from core.push_notifications
		where kind = 'competition_announced' and competition_id = $1`, competitionID).
		Scan(&st.Queued, &st.Pending, &st.Sent, &st.Skipped, &st.Failed)
	return st, err
}
