package models

import (
	"time"

	"github.com/google/uuid"
)

// Conversation maps to messaging.conversations — one chat thread (the "room").
//
// Type decides which optional FKs are filled:
//   - product_inquiry → product_id + shop_id
//   - order           → product_id + shop_id + order_id + order_item_id
//   - support         → none of those (see support.cases instead)
//
// created_by_user_id is polymorphic: customer UUID, seller UUID, or admin UUID
// depending on who opened the thread (no single "users" table to FK to).
type Conversation struct {
	ID              uuid.UUID  `json:"id"`
	Type            string     `json:"type"`   // product_inquiry | order | support
	Status          string     `json:"status"` // open | closed
	ProductID       *uuid.UUID `json:"product_id,omitempty"`
	ShopID          *uuid.UUID `json:"shop_id,omitempty"`
	OrderID         *uuid.UUID `json:"order_id,omitempty"`
	OrderItemID     *uuid.UUID `json:"order_item_id,omitempty"`
	CreatedByUserID uuid.UUID  `json:"created_by_user_id"`          // who opened the thread
	LastMessageAt   *time.Time `json:"last_message_at,omitempty"`   // bumped on every send; nil until first message
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ConversationParticipant maps to messaging.conversation_participants.
//
// This is how we know who is the customer vs seller vs admin in a thread:
//   - user_id = that person's UUID (customer.customers.id OR seller.sellers.id OR admin.admin_users.id)
//   - role    = "customer" | "seller" | "admin"
//
// Messages only store sender_user_id; join here to learn the sender's role for the UI.
// last_read_at drives unread counts (messages from others after this timestamp = unread).
type ConversationParticipant struct {
	ID             uuid.UUID  `json:"id"`
	ConversationID uuid.UUID  `json:"conversation_id"`
	UserID         uuid.UUID  `json:"user_id"`                   // polymorphic: customer / seller / admin id
	Role           string     `json:"role"`                      // customer | seller | admin
	LastReadAt     *time.Time `json:"last_read_at,omitempty"`    // null = never opened / never marked read
	JoinedAt       time.Time  `json:"joined_at"`

	// Read-only display fields, resolved from the table the role points at so a
	// chat can show who is writing (e.g. a seller sees the customer's name).
	// Email is deliberately not exposed.
	DisplayName *string `json:"display_name,omitempty"` // customer display_name | seller trading/legal name | admin display_name
	ImageURL    *string `json:"image_url,omitempty"`
}

// SupportCase maps to support.cases — admin help ticket metadata linked 1:1 to a conversation.
// Chat bubbles still live in messaging.messages; this row holds subject/status/priority
// and who the ticket is about (counterpart_*).
type SupportCase struct {
	ID                uuid.UUID `json:"id"`
	ConversationID    uuid.UUID `json:"conversation_id"`     // 1:1 with messaging.conversations
	OpenedByUserID    uuid.UUID `json:"opened_by_user_id"`  // who created the ticket
	OpenedByRole      string    `json:"opened_by_role"`     // admin | customer | seller
	CounterpartUserID uuid.UUID `json:"counterpart_user_id"` // the customer/seller the case is about
	CounterpartRole   string    `json:"counterpart_role"`    // customer | seller
	Subject           *string   `json:"subject,omitempty"`
	Status            string    `json:"status"`   // open | in_progress | closed
	Priority          string    `json:"priority"` // low | normal | high | urgent
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Message maps to messaging.messages — one chat bubble.
//
// There is NO role column here. To know if the sender was customer or seller:
// look up sender_user_id in conversation_participants for this conversation_id.
// Attachments (if any) come from messaging.message_attachments → media.media_assets.
type Message struct {
	ID             uuid.UUID           `json:"id"`
	ConversationID uuid.UUID           `json:"conversation_id"`
	SenderUserID   uuid.UUID           `json:"sender_user_id"` // JWT subject of whoever sent it
	Body           string              `json:"body"`
	Type           string              `json:"type"` // text (MVP); files ride on attachments
	CreatedAt      time.Time           `json:"created_at"`
	DeletedAt      *time.Time          `json:"deleted_at,omitempty"` // soft-delete; list queries skip non-null
	Attachments    []MessageAttachment `json:"attachments,omitempty"`
}

// MessageAttachment is a file on a message, joined with media.media_assets for the client.
type MessageAttachment struct {
	ID           uuid.UUID `json:"id"`
	MessageID    uuid.UUID `json:"message_id"`
	MediaID      uuid.UUID `json:"media_id"`
	AssetType    string    `json:"asset_type"` // image | document | ...
	ObjectPath   string    `json:"object_path"`
	CDNURL       *string   `json:"cdn_url,omitempty"`
	MimeType     string    `json:"mime_type"`
	SizeBytes    int64     `json:"size_bytes"`
	CreatedAt    time.Time `json:"created_at"`
}

// ConversationSummary is a thread for inbox lists (includes unread for the viewer).
// Embeds Conversation so all room fields are present, then adds inbox-only extras.
type ConversationSummary struct {
	Conversation
	UnreadCount  int                       `json:"unread_count"`            // messages from others after my last_read_at
	Participants []ConversationParticipant `json:"participants"`
	SupportCase  *SupportCase              `json:"support_case,omitempty"` // only when type=support
}

// ConversationDetails is one thread with participants (messages loaded separately via /messages).
type ConversationDetails struct {
	Conversation
	Participants []ConversationParticipant `json:"participants"`
	SupportCase  *SupportCase              `json:"support_case,omitempty"` // only when type=support
}
