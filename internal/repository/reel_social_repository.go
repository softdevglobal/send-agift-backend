package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrReelLikeNotFound    = errors.New("reel like not found")
	ErrReelCommentNotFound = errors.New("reel comment not found")
	ErrReelNotPublic       = errors.New("reel not public")
)

// SocialIdentity is either a logged-in customer or a guest token (never both).
type SocialIdentity struct {
	CustomerID *uuid.UUID
	GuestToken *string
}

// ReelSocialRepository persists likes and comments under schema social.
type ReelSocialRepository struct {
	db *pgxpool.Pool
}

func NewReelSocialRepository(db *pgxpool.Pool) *ReelSocialRepository {
	return &ReelSocialRepository{db: db}
}

// PublicCounts is like_count + comment_count for a published public reel.
type PublicCounts struct {
	LikeCount    int64
	CommentCount int64
}

// RecentLiker is a public preview of who liked (no ids / tokens).
type RecentLiker struct {
	Type        string // customer | guest
	DisplayName string
}

// EnsurePublicReel checks the reel is published + public on an active shop.
func (r *ReelSocialRepository) EnsurePublicReel(ctx context.Context, reelID string) error {
	_, err := r.PublicCounts(ctx, reelID)
	return err
}

// PublicLikeCount returns like_count when the reel is public; otherwise ErrReelNotPublic.
func (r *ReelSocialRepository) PublicLikeCount(ctx context.Context, reelID string) (int64, error) {
	c, err := r.PublicCounts(ctx, reelID)
	if err != nil {
		return 0, err
	}
	return c.LikeCount, nil
}

// PublicCounts returns denormalized counters for a public reel (no auth needed).
func (r *ReelSocialRepository) PublicCounts(ctx context.Context, reelID string) (*PublicCounts, error) {
	var c PublicCounts
	err := r.db.QueryRow(ctx, `
		select r.like_count, r.comment_count
		from seller.reels r
		inner join seller.shops s on s.id = r.shop_id
		where r.id = $1
		  and r.status = 'published'
		  and r.visibility = 'public'
		  and s.status = 'active'`, reelID).Scan(&c.LikeCount, &c.CommentCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReelNotPublic
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListRecentLikers returns newest likers (type + display_name only; no ids/tokens).
// Guests have no nickname on likes — shown as "Guest".
func (r *ReelSocialRepository) ListRecentLikers(ctx context.Context, reelID string, limit int) ([]RecentLiker, error) {
	m, err := r.ListRecentLikersForReels(ctx, []string{reelID}, limit)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(reelID)
	if err != nil {
		return nil, err
	}
	return m[id], nil
}

// ListRecentLikersForReels batch-loads up to limitPerReel newest likers per reel.
func (r *ReelSocialRepository) ListRecentLikersForReels(ctx context.Context, reelIDs []string, limitPerReel int) (map[uuid.UUID][]RecentLiker, error) {
	out := make(map[uuid.UUID][]RecentLiker, len(reelIDs))
	if len(reelIDs) == 0 {
		return out, nil
	}
	if limitPerReel <= 0 {
		limitPerReel = 3
	}
	rows, err := r.db.Query(ctx, `
		select reel_id, liker_type, display_name
		from (
			select
				l.reel_id,
				case when l.customer_id is not null then 'customer' else 'guest' end as liker_type,
				case
					when l.customer_id is not null then coalesce(nullif(trim(cu.display_name), ''), 'Customer')
					else 'Guest'
				end as display_name,
				row_number() over (partition by l.reel_id order by l.created_at desc) as rn
			from social.reel_likes l
			left join customer.customers cu on cu.id = l.customer_id
			where l.reel_id = any($1::uuid[])
		) ranked
		where rn <= $2
		order by reel_id, rn`, reelIDs, limitPerReel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var reelID uuid.UUID
		var item RecentLiker
		if err := rows.Scan(&reelID, &item.Type, &item.DisplayName); err != nil {
			return nil, err
		}
		out[reelID] = append(out[reelID], item)
	}
	return out, rows.Err()
}

// ListVisibleCommentsForReels returns all visible comments for the given reels (newest first).
func (r *ReelSocialRepository) ListVisibleCommentsForReels(ctx context.Context, reelIDs []string) (map[uuid.UUID][]models.ReelComment, error) {
	out := make(map[uuid.UUID][]models.ReelComment, len(reelIDs))
	if len(reelIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select c.id, c.reel_id, c.customer_id, c.guest_token, c.is_anonymous, c.display_name,
		       c.body, c.status, c.created_at, c.updated_at,
		       cu.display_name as customer_display_name
		from social.reel_comments c
		left join customer.customers cu on cu.id = c.customer_id
		where c.reel_id = any($1::uuid[])
		  and c.status = 'visible'
		order by c.created_at desc, c.id desc`, reelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var c models.ReelComment
		if err := rows.Scan(
			&c.ID, &c.ReelID, &c.CustomerID, &c.GuestToken, &c.IsAnonymous, &c.DisplayName,
			&c.Body, &c.Status, &c.CreatedAt, &c.UpdatedAt, &c.CustomerDisplayName,
		); err != nil {
			return nil, err
		}
		out[c.ReelID] = append(out[c.ReelID], c)
	}
	return out, rows.Err()
}

// Like inserts a like and bumps like_count. Idempotent if already liked.
func (r *ReelSocialRepository) Like(ctx context.Context, reelID string, id SocialIdentity) (liked bool, likeCount int64, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback(ctx)

	// Partial unique indexes (customer vs guest) — DO NOTHING if already liked.
	var inserted uuid.UUID
	err = tx.QueryRow(ctx, `
		insert into social.reel_likes (reel_id, customer_id, guest_token)
		values ($1, $2, $3)
		on conflict do nothing
		returning id`,
		reelID, id.CustomerID, id.GuestToken,
	).Scan(&inserted)

	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, 0, err
	}
	if err == nil {
		// New like — bump denormalized counter on seller.reels.
		if _, err := tx.Exec(ctx, `
			update seller.reels set like_count = like_count + 1, updated_at = now()
			where id = $1`, reelID); err != nil {
			return false, 0, err
		}
		liked = true
	}

	if err := tx.QueryRow(ctx, `select like_count from seller.reels where id = $1`, reelID).Scan(&likeCount); err != nil {
		return false, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, 0, err
	}
	return liked, likeCount, nil
}

// Unlike removes the caller's like and decrements like_count when a row was deleted.
func (r *ReelSocialRepository) Unlike(ctx context.Context, reelID string, id SocialIdentity) (likeCount int64, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		delete from social.reel_likes
		where reel_id = $1
		  and (
		    ($2::uuid is not null and customer_id = $2)
		    or ($3::text is not null and guest_token = $3)
		  )`, reelID, id.CustomerID, id.GuestToken)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrReelLikeNotFound
	}

	if _, err := tx.Exec(ctx, `
		update seller.reels
		set like_count = greatest(like_count - 1, 0), updated_at = now()
		where id = $1`, reelID); err != nil {
		return 0, err
	}

	if err := tx.QueryRow(ctx, `select like_count from seller.reels where id = $1`, reelID).Scan(&likeCount); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return likeCount, nil
}

// HasLiked reports whether this identity already liked the reel.
func (r *ReelSocialRepository) HasLiked(ctx context.Context, reelID string, id SocialIdentity) (bool, error) {
	var ok bool
	err := r.db.QueryRow(ctx, `
		select exists(
			select 1 from social.reel_likes
			where reel_id = $1
			  and (
			    ($2::uuid is not null and customer_id = $2)
			    or ($3::text is not null and guest_token = $3)
			  )
		)`, reelID, id.CustomerID, id.GuestToken).Scan(&ok)
	return ok, err
}

// CreateComment inserts a visible comment and bumps comment_count.
func (r *ReelSocialRepository) CreateComment(ctx context.Context, c *models.ReelComment) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		insert into social.reel_comments (
			reel_id, customer_id, guest_token, is_anonymous, display_name, body, status
		) values ($1, $2, $3, $4, $5, $6, 'visible')
		returning id, created_at, updated_at, status`,
		c.ReelID, c.CustomerID, c.GuestToken, c.IsAnonymous, c.DisplayName, c.Body,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt, &c.Status)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		update seller.reels set comment_count = comment_count + 1, updated_at = now()
		where id = $1`, c.ReelID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateCommentBody edits body if the caller owns the comment and it is still visible.
func (r *ReelSocialRepository) UpdateCommentBody(ctx context.Context, commentID string, id SocialIdentity, body string) (*models.ReelComment, error) {
	c := &models.ReelComment{}
	err := r.db.QueryRow(ctx, `
		update social.reel_comments
		set body = $1, updated_at = now()
		where id = $2
		  and status = 'visible'
		  and (
		    ($3::uuid is not null and customer_id = $3)
		    or ($4::text is not null and guest_token = $4)
		  )
		returning id, reel_id, customer_id, guest_token, is_anonymous, display_name,
		          body, status, created_at, updated_at`,
		body, commentID, id.CustomerID, id.GuestToken,
	).Scan(
		&c.ID, &c.ReelID, &c.CustomerID, &c.GuestToken, &c.IsAnonymous, &c.DisplayName,
		&c.Body, &c.Status, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReelCommentNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// SoftDeleteComment marks status=deleted and decrements comment_count once.
func (r *ReelSocialRepository) SoftDeleteComment(ctx context.Context, commentID string, id SocialIdentity) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var reelID uuid.UUID
	err = tx.QueryRow(ctx, `
		update social.reel_comments
		set status = 'deleted', updated_at = now()
		where id = $1
		  and status = 'visible'
		  and (
		    ($2::uuid is not null and customer_id = $2)
		    or ($3::text is not null and guest_token = $3)
		  )
		returning reel_id`,
		commentID, id.CustomerID, id.GuestToken,
	).Scan(&reelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReelCommentNotFound
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		update seller.reels
		set comment_count = greatest(comment_count - 1, 0), updated_at = now()
		where id = $1`, reelID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CommentCursor is keyset pagination on (created_at desc, id desc).
type CommentCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

// ListComments returns visible comments newest first, optionally joining customer display_name.
func (r *ReelSocialRepository) ListComments(ctx context.Context, reelID string, limit int, cursor *CommentCursor) ([]models.ReelComment, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := r.db.Query(ctx, `
		select c.id, c.reel_id, c.customer_id, c.guest_token, c.is_anonymous, c.display_name,
		       c.body, c.status, c.created_at, c.updated_at,
		       cu.display_name as customer_display_name
		from social.reel_comments c
		left join customer.customers cu on cu.id = c.customer_id
		where c.reel_id = $1
		  and c.status = 'visible'
		  and (
		    $2::timestamptz is null
		    or (c.created_at, c.id) < ($2::timestamptz, $3::uuid)
		  )
		order by c.created_at desc, c.id desc
		limit $4`,
		reelID,
		func() any {
			if cursor == nil {
				return nil
			}
			return cursor.CreatedAt
		}(),
		func() any {
			if cursor == nil {
				return nil
			}
			return cursor.ID
		}(),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.ReelComment
	for rows.Next() {
		var c models.ReelComment
		if err := rows.Scan(
			&c.ID, &c.ReelID, &c.CustomerID, &c.GuestToken, &c.IsAnonymous, &c.DisplayName,
			&c.Body, &c.Status, &c.CreatedAt, &c.UpdatedAt, &c.CustomerDisplayName,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EncodeCommentCursor packs created_at+id for the next page.
func EncodeCommentCursor(c CommentCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCommentCursor parses a next_cursor from the client.
func DecodeCommentCursor(raw string) (*CommentCursor, error) {
	if raw == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var c CommentCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCommentByID loads one comment (any status) for ownership checks / enrichment.
func (r *ReelSocialRepository) GetCommentByID(ctx context.Context, commentID string) (*models.ReelComment, error) {
	c := &models.ReelComment{}
	err := r.db.QueryRow(ctx, `
		select c.id, c.reel_id, c.customer_id, c.guest_token, c.is_anonymous, c.display_name,
		       c.body, c.status, c.created_at, c.updated_at, cu.display_name
		from social.reel_comments c
		left join customer.customers cu on cu.id = c.customer_id
		where c.id = $1`, commentID,
	).Scan(
		&c.ID, &c.ReelID, &c.CustomerID, &c.GuestToken, &c.IsAnonymous, &c.DisplayName,
		&c.Body, &c.Status, &c.CreatedAt, &c.UpdatedAt, &c.CustomerDisplayName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReelCommentNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}
