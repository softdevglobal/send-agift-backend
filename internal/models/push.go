package models

import (
	"time"

	"github.com/google/uuid"
)

// PushMessage is one push notification: what the phone shows, plus data the
// app reads when it is tapped (for example which competition to open).
type PushMessage struct {
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Data  map[string]string `json:"data"`
}

// InboxNotification is one entry in a customer's notification inbox.
type InboxNotification struct {
	ID        uuid.UUID         `json:"id"`
	Kind      string            `json:"kind"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	Data      map[string]string `json:"data"`
	CreatedAt time.Time         `json:"created_at"`
	ReadAt    *time.Time        `json:"read_at,omitempty"`
}

// NotificationInbox is a page of the inbox with the unread count.
type NotificationInbox struct {
	Items  []InboxNotification `json:"items"`
	Unread int                 `json:"unread"`
}
