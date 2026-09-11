package services

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrSocialIdentityRequired = errors.New("social identity required")
	ErrInvalidComment         = errors.New("invalid comment")
	ErrCommentNotFound        = errors.New("comment not found")
	ErrReelLikeNotFound       = errors.New("like not found")
	ErrReelNotPublicSocial    = errors.New("reel not public")
)

const (
	defaultCommentLimit = 20
	maxCommentLimit     = 50
	maxCommentBodyLen   = 1000
	defaultAnonName     = "Anonymous"
)

// ReelSocialService owns public likes and comments on reels.
type ReelSocialService struct {
	social *repository.ReelSocialRepository
}

func NewReelSocialService(social *repository.ReelSocialRepository) *ReelSocialService {
	return &ReelSocialService{social: social}
}

// SocialActor is resolved from JWT customer or X-Guest-Token (same URLs for both).
type SocialActor struct {
	CustomerID string // set when logged-in customer
	GuestToken string // set when guest (no register)
}

func (a SocialActor) toIdentity() (repository.SocialIdentity, error) {
	if a.CustomerID != "" {
		id, err := uuid.Parse(a.CustomerID)
		if err != nil {
			return repository.SocialIdentity{}, ErrSocialIdentityRequired
		}
		return repository.SocialIdentity{CustomerID: &id}, nil
	}
	if a.GuestToken != "" {
		tok := a.GuestToken
		return repository.SocialIdentity{GuestToken: &tok}, nil
	}
	return repository.SocialIdentity{}, ErrSocialIdentityRequired
}

// LikeResult is returned after like/unlike.
type LikeResult struct {
	Liked     bool  `json:"liked"`
	LikeCount int64 `json:"like_count"`
}

// RecentLikerView is a public liker preview (no customer_id / guest_token).
type RecentLikerView struct {
	Type        string `json:"type"` // customer | guest
	DisplayName string `json:"display_name"`
}

// ReelLikesResult matches GET /reels/{id}/likes public contract.
type ReelLikesResult struct {
	ReelID           string            `json:"reel_id"`
	LikeCount        int64             `json:"like_count"`
	LikedByRequester bool              `json:"liked_by_requester"`
	RecentLikers     []RecentLikerView `json:"recent_likers"`
}

// GetLikes returns like_count, optional liked_by_requester, and recent likers.
// Auth is optional: JWT or X-Guest-Token only needed for liked_by_requester=true.
func (s *ReelSocialService) GetLikes(ctx context.Context, reelID string, actor SocialActor, limit int) (*ReelLikesResult, error) {
	counts, err := s.social.PublicCounts(ctx, reelID)
	if err != nil {
		if errors.Is(err, repository.ErrReelNotPublic) {
			return nil, ErrReelNotPublicSocial
		}
		return nil, err
	}

	if limit <= 0 {
		limit = 3 // default: show 3 recent likers publicly
	}
	if limit > 50 {
		limit = 50
	}

	likers, err := s.social.ListRecentLikers(ctx, reelID, limit)
	if err != nil {
		return nil, err
	}
	recent := make([]RecentLikerView, 0, len(likers))
	for _, l := range likers {
		recent = append(recent, RecentLikerView{Type: l.Type, DisplayName: l.DisplayName})
	}

	out := &ReelLikesResult{
		ReelID:           reelID,
		LikeCount:        counts.LikeCount,
		LikedByRequester: false,
		RecentLikers:     recent,
	}

	// Optional identity → single indexed lookup for "did I like this".
	if id, err := actor.toIdentity(); err == nil {
		ok, err := s.social.HasLiked(ctx, reelID, id)
		if err != nil {
			return nil, err
		}
		out.LikedByRequester = ok
	}

	return out, nil
}

// Like adds a like for the actor on a public reel (idempotent).
func (s *ReelSocialService) Like(ctx context.Context, reelID string, actor SocialActor) (*LikeResult, error) {
	id, err := actor.toIdentity()
	if err != nil {
		return nil, err
	}
	if err := s.social.EnsurePublicReel(ctx, reelID); err != nil {
		if errors.Is(err, repository.ErrReelNotPublic) {
			return nil, ErrReelNotPublicSocial
		}
		return nil, err
	}
	_, count, err := s.social.Like(ctx, reelID, id)
	if err != nil {
		return nil, err
	}
	// After a successful POST the caller has a like (new or already present).
	return &LikeResult{Liked: true, LikeCount: count}, nil
}

// Unlike removes the actor's like.
func (s *ReelSocialService) Unlike(ctx context.Context, reelID string, actor SocialActor) (*LikeResult, error) {
	id, err := actor.toIdentity()
	if err != nil {
		return nil, err
	}
	if err := s.social.EnsurePublicReel(ctx, reelID); err != nil {
		if errors.Is(err, repository.ErrReelNotPublic) {
			return nil, ErrReelNotPublicSocial
		}
		return nil, err
	}
	count, err := s.social.Unlike(ctx, reelID, id)
	if err != nil {
		if errors.Is(err, repository.ErrReelLikeNotFound) {
			return nil, ErrReelLikeNotFound
		}
		return nil, err
	}
	return &LikeResult{Liked: false, LikeCount: count}, nil
}

// LikedByMe reports whether this actor already liked the reel, plus current like_count.
func (s *ReelSocialService) LikedByMe(ctx context.Context, reelID string, actor SocialActor) (*LikeResult, error) {
	count, err := s.social.PublicLikeCount(ctx, reelID)
	if err != nil {
		if errors.Is(err, repository.ErrReelNotPublic) {
			return nil, ErrReelNotPublicSocial
		}
		return nil, err
	}
	id, err := actor.toIdentity()
	if err != nil {
		return nil, err
	}
	ok, err := s.social.HasLiked(ctx, reelID, id)
	if err != nil {
		return nil, err
	}
	return &LikeResult{Liked: ok, LikeCount: count}, nil
}

// CommentInput is the POST/PUT body for a reel comment.
type CommentInput struct {
	Body        string  `json:"body"`
	DisplayName *string `json:"display_name"` // nickname when anonymous / guest
	IsAnonymous *bool   `json:"is_anonymous"` // customers may hide their name
}

// CreateComment posts a comment as customer or guest on a public reel.
func (s *ReelSocialService) CreateComment(ctx context.Context, reelID string, actor SocialActor, in CommentInput) (*models.ReelCommentView, error) {
	id, err := actor.toIdentity()
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(in.Body)
	if body == "" || len(body) > maxCommentBodyLen {
		return nil, ErrInvalidComment
	}
	if err := s.social.EnsurePublicReel(ctx, reelID); err != nil {
		if errors.Is(err, repository.ErrReelNotPublic) {
			return nil, ErrReelNotPublicSocial
		}
		return nil, err
	}

	reelUUID, err := uuid.Parse(reelID)
	if err != nil {
		return nil, ErrReelNotPublicSocial
	}

	c := &models.ReelComment{
		ReelID:     reelUUID,
		CustomerID: id.CustomerID,
		GuestToken: id.GuestToken,
		Body:       body,
	}

	if id.GuestToken != nil {
		// Guests are always anonymous.
		c.IsAnonymous = true
		c.DisplayName = anonDisplayName(in.DisplayName)
	} else {
		// Customer: default show name unless is_anonymous=true.
		anon := in.IsAnonymous != nil && *in.IsAnonymous
		c.IsAnonymous = anon
		if anon {
			c.DisplayName = anonDisplayName(in.DisplayName)
		}
	}

	if err := s.social.CreateComment(ctx, c); err != nil {
		return nil, err
	}
	// Enrich customer display name for named comments.
	if c.CustomerID != nil && !c.IsAnonymous {
		if loaded, err := s.social.GetCommentByID(ctx, c.ID.String()); err == nil {
			c.CustomerDisplayName = loaded.CustomerDisplayName
		}
	}
	view := models.ToCommentView(*c)
	return &view, nil
}

// UpdateComment edits the body; only the original author (same JWT or guest token).
func (s *ReelSocialService) UpdateComment(ctx context.Context, reelID, commentID string, actor SocialActor, in CommentInput) (*models.ReelCommentView, error) {
	id, err := actor.toIdentity()
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(in.Body)
	if body == "" || len(body) > maxCommentBodyLen {
		return nil, ErrInvalidComment
	}

	existing, err := s.social.GetCommentByID(ctx, commentID)
	if err != nil {
		if errors.Is(err, repository.ErrReelCommentNotFound) {
			return nil, ErrCommentNotFound
		}
		return nil, err
	}
	if existing.ReelID.String() != reelID || existing.Status != "visible" {
		return nil, ErrCommentNotFound
	}

	updated, err := s.social.UpdateCommentBody(ctx, commentID, id, body)
	if err != nil {
		if errors.Is(err, repository.ErrReelCommentNotFound) {
			return nil, ErrCommentNotFound
		}
		return nil, err
	}
	updated.CustomerDisplayName = existing.CustomerDisplayName
	view := models.ToCommentView(*updated)
	return &view, nil
}

// DeleteComment soft-deletes; only the original author.
func (s *ReelSocialService) DeleteComment(ctx context.Context, reelID, commentID string, actor SocialActor) error {
	id, err := actor.toIdentity()
	if err != nil {
		return err
	}
	existing, err := s.social.GetCommentByID(ctx, commentID)
	if err != nil {
		if errors.Is(err, repository.ErrReelCommentNotFound) {
			return ErrCommentNotFound
		}
		return err
	}
	if existing.ReelID.String() != reelID {
		return ErrCommentNotFound
	}
	if err := s.social.SoftDeleteComment(ctx, commentID, id); err != nil {
		if errors.Is(err, repository.ErrReelCommentNotFound) {
			return ErrCommentNotFound
		}
		return err
	}
	return nil
}

// ListComments returns a public page of comments (no auth required).
func (s *ReelSocialService) ListComments(ctx context.Context, reelID, cursor string, limit int) (*models.ReelCommentList, error) {
	if err := s.social.EnsurePublicReel(ctx, reelID); err != nil {
		if errors.Is(err, repository.ErrReelNotPublic) {
			return nil, ErrReelNotPublicSocial
		}
		return nil, err
	}
	if limit <= 0 {
		limit = defaultCommentLimit
	}
	if limit > maxCommentLimit {
		limit = maxCommentLimit
	}
	cur, err := repository.DecodeCommentCursor(cursor)
	if err != nil {
		return nil, ErrInvalidCursor
	}

	rows, err := s.social.ListComments(ctx, reelID, limit+1, cur)
	if err != nil {
		return nil, err
	}

	list := &models.ReelCommentList{Items: make([]models.ReelCommentView, 0, len(rows))}
	for i, row := range rows {
		if i >= limit {
			enc := repository.EncodeCommentCursor(repository.CommentCursor{
				CreatedAt: rows[limit-1].CreatedAt,
				ID:        rows[limit-1].ID,
			})
			list.NextCursor = &enc
			break
		}
		list.Items = append(list.Items, models.ToCommentView(row))
	}
	return list, nil
}

func anonDisplayName(in *string) *string {
	name := defaultAnonName
	if in != nil {
		trimmed := strings.TrimSpace(*in)
		if trimmed != "" {
			if len(trimmed) > 40 {
				trimmed = trimmed[:40]
			}
			name = trimmed
		}
	}
	return &name
}
