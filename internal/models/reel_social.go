package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
)
// ReelLike maps to social.reel_likes — one like per reel + identity.
type ReelLike struct {
	ID         uuid.UUID  `json:"id"`
	ReelID     uuid.UUID  `json:"reel_id"`
	CustomerID *uuid.UUID `json:"-"` // never expose; used for auth only
	GuestToken *string    `json:"-"` // never expose; used for auth only
	CreatedAt  time.Time  `json:"created_at"`
}

// ReelComment maps to social.reel_comments.
// Public JSON hides customer_id / guest_token; Author is built in the service.
type ReelComment struct {
	ID           uuid.UUID  `json:"id"`
	ReelID       uuid.UUID  `json:"reel_id"`
	CustomerID   *uuid.UUID `json:"-"`
	GuestToken   *string    `json:"-"`
	IsAnonymous  bool       `json:"is_anonymous"`
	DisplayName  *string    `json:"-"` // raw DB value; use Author.DisplayName in API
	Body         string     `json:"body"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// CustomerDisplayName is joined from customer.customers when not anonymous.
	CustomerDisplayName *string `json:"-"`
}

// CommentAuthor is what the client shows as the commenter name.
type CommentAuthor struct {
	Type        string `json:"type"` // customer | anonymous
	DisplayName string `json:"display_name"`
}

// ReelLikerPreview is a public liker name (no ids / tokens).
type ReelLikerPreview struct {
	Type        string `json:"type"` // customer | guest
	DisplayName string `json:"display_name"`
}

// ReelCommentView is the public comment payload (no tokens / customer ids).
type ReelCommentView struct {
	ID          uuid.UUID     `json:"id"`
	ReelID      uuid.UUID     `json:"reel_id"`
	Body        string        `json:"body"`
	IsAnonymous bool          `json:"is_anonymous"`
	Author      CommentAuthor `json:"author"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// ToCommentView builds the public comment JSON from a DB row.
func ToCommentView(c ReelComment) ReelCommentView {
	const defaultAnonName = "Anonymous"
	author := CommentAuthor{Type: "anonymous", DisplayName: defaultAnonName}
	if c.IsAnonymous {
		if c.DisplayName != nil && strings.TrimSpace(*c.DisplayName) != "" {
			author.DisplayName = strings.TrimSpace(*c.DisplayName)
		}
	} else {
		author.Type = "customer"
		author.DisplayName = defaultAnonName
		if c.CustomerDisplayName != nil && strings.TrimSpace(*c.CustomerDisplayName) != "" {
			author.DisplayName = strings.TrimSpace(*c.CustomerDisplayName)
		} else if c.DisplayName != nil && strings.TrimSpace(*c.DisplayName) != "" {
			author.DisplayName = strings.TrimSpace(*c.DisplayName)
		}
	}
	return ReelCommentView{
		ID:          c.ID,
		ReelID:      c.ReelID,
		Body:        c.Body,
		IsAnonymous: c.IsAnonymous,
		Author:      author,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

// ReelCommentList is a page of comments with an optional next cursor.
type ReelCommentList struct {
	Items      []ReelCommentView `json:"items"`
	NextCursor *string           `json:"next_cursor,omitempty"`
}
