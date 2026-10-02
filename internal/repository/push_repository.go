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

// RequeueMissed puts back in the queue the customer's unread notifications
// that never reached a phone — they had none registered, or only dead ones
// — while their competition is still open. Called when a device registers,
// so signing in delivers what arrived while signed out. Returns how many.
func (r *PushRepository) RequeueMissed(ctx context.Context, customerID uuid.UUID) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		update core.push_notifications n
		set status = 'pending', next_attempt_at = now(), last_error = null
		from competition.competitions c
		where n.customer_id = $1 and n.read_at is null and n.status = 'skipped'
		  and c.id = n.competition_id and c.status in ('scheduled', 'live') and c.ends_at > now()`,
		customerID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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
		on conflict (kind, competition_id, customer_id) where kind = 'competition_announced' do nothing`,
		competitionID, msg.Title, msg.Body, data)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// queueOvertaken tells every player whose best score the beater just passed
// that they have been beaten, so they come back and play again. It runs in
// the transaction that accepts the beater's score. "Passed" means the
// player's best was at least the beater's previous best and is now below the
// new score; players already behind the beater hear nothing. Someone who
// already has an unread "beaten" notice for this competition from the last
// 15 minutes is not sent another, so a run of plays is one notification.
func queueOvertaken(ctx context.Context, q querier, competitionID, beaterID, submissionID uuid.UUID, score int64) error {
	_, err := q.Exec(ctx, `
		with mine as (
			select coalesce(max(score) filter (where id <> $3), -1) as old_best
			from competition.score_submissions
			where competition_id = $1 and customer_id = $2 and validation_status = 'accepted'
		), others as (
			select customer_id, max(score) as best
			from competition.score_submissions
			where competition_id = $1 and customer_id <> $2 and validation_status = 'accepted'
			group by customer_id
		), beaten as (
			select o.customer_id, o.best
			from others o, mine m
			where o.best < $4 and o.best >= m.old_best
		), beater as (
			select coalesce(nullif(split_part(trim(coalesce(display_name, '')), ' ', 1), ''), 'Another player') as name
			from customer.customers where id = $2
		)
		insert into core.push_notifications (customer_id, kind, competition_id, title, body, data)
		select b.customer_id, 'competition_overtaken', $1,
		       format('%s beat your score!', bt.name),
		       format('%s scored %s in %s, beating your best of %s. Play again to take the lead!',
		              bt.name, $4::bigint, c.title, b.best),
		       jsonb_build_object('type', 'competition', 'competition_id', $1::text)
		from beaten b
		cross join beater bt
		inner join competition.competitions c on c.id = $1
		inner join customer.customers cu on cu.id = b.customer_id and cu.status = 'active'
		where not exists (
			select 1 from core.push_notifications p
			where p.customer_id = b.customer_id and p.kind = 'competition_overtaken'
			  and p.competition_id = $1 and p.read_at is null
			  and p.created_at > now() - interval '15 minutes')`,
		competitionID, beaterID, submissionID, score)
	return err
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
		if p.Message.Data == nil {
			p.Message.Data = map[string]string{}
		}
		// Lets the app tell a push apart from the inbox entry it matches.
		p.Message.Data["notification_id"] = p.ID.String()
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

// Inbox returns a customer's newest notifications and how many are unread.
// Announcements for competitions that were cancelled are left out.
func (r *PushRepository) Inbox(ctx context.Context, customerID uuid.UUID, limit int) (*models.NotificationInbox, error) {
	rows, err := r.db.Query(ctx, `
		select n.id, n.kind, n.title, n.body, n.data, n.created_at, n.read_at
		from core.push_notifications n
		left join competition.competitions c on c.id = n.competition_id
		where n.customer_id = $1 and coalesce(c.status, '') <> 'cancelled'
		order by n.created_at desc
		limit $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	inbox := &models.NotificationInbox{Items: make([]models.InboxNotification, 0)}
	for rows.Next() {
		var n models.InboxNotification
		var data []byte
		if err := rows.Scan(&n.ID, &n.Kind, &n.Title, &n.Body, &data, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &n.Data); err != nil {
			return nil, err
		}
		inbox.Items = append(inbox.Items, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	err = r.db.QueryRow(ctx, `
		select count(*)
		from core.push_notifications n
		left join competition.competitions c on c.id = n.competition_id
		where n.customer_id = $1 and n.read_at is null and coalesce(c.status, '') <> 'cancelled'`,
		customerID).Scan(&inbox.Unread)
	return inbox, err
}

// MarkRead marks the given notifications read, or all of them when ids is
// empty. Only the customer's own notifications are touched.
func (r *PushRepository) MarkRead(ctx context.Context, customerID uuid.UUID, ids []uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update core.push_notifications set read_at = now()
		where customer_id = $1 and read_at is null
		  and (cardinality($2::uuid[]) = 0 or id = any($2::uuid[]))`, customerID, ids)
	return err
}
