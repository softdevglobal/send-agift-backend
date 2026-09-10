package handlers

import (
	"encoding/json" // decode JSON request bodies into service input structs
	"errors"        // errors.Is for mapping sentinel service errors → HTTP status
	"log"           // log unexpected (non-sentinel) errors before returning 500
	"net/http"      // ResponseWriter / Request types for every handler method
	"strconv"       // parse ?limit= query param for message pagination
	"time"          // parse ?before= RFC3339 cursor for older-message pages

	"github.com/go-chi/chi/v5" // URL path params ({id})

	"myapp/internal/middleware" // UserIDContextKey / RoleContextKey set by RequireAuth
	"myapp/internal/services"   // MessagingService + sentinel errors this handler maps
	"myapp/internal/utils"      // JSON() / Error() response helpers
)

// MessagingHandler is the HTTP adapter for chat.
// It does NOT contain business rules — it only:
//  1. reads the authenticated actor from the request context
//  2. parses path/query/body input
//  3. calls MessagingService
//  4. maps service errors to HTTP status codes
type MessagingHandler struct {
	msg *services.MessagingService // business logic for product/order/support chat
}

// NewMessagingHandler wires the service into a new handler (DI constructor).
func NewMessagingHandler(msg *services.MessagingService) *MessagingHandler {
	return &MessagingHandler{msg: msg}
}

// Start handles POST /conversations.
// Creates (or reuses) a product_inquiry, order, or support thread.
// Role rules live in the service (e.g. only customers start product_inquiry).
func (h *MessagingHandler) Start(w http.ResponseWriter, r *http.Request) {
	userID, role := h.actor(r) // JWT subject + role placed on context by middleware
	var req services.StartConversationInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	details, err := h.msg.Start(r.Context(), userID, role, req)
	if err != nil {
		h.writeError(w, err, "could not start conversation")
		return
	}
	// 201 Created — even when an existing thread was reused, the client asked to "start"
	utils.JSON(w, http.StatusCreated, details)
}

// List handles GET /conversations.
// Returns only conversations for this JWT user+role (a seller never sees another seller's inbox).
func (h *MessagingHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, role := h.actor(r)
	items, err := h.msg.List(r.Context(), userID, role)
	if err != nil {
		h.writeError(w, err, "could not list conversations")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

// Get handles GET /conversations/{id}.
// Returns one thread + participants (+ support_case when type=support).
// Non-participants (including other sellers) get 404 so IDs can't be probed.
func (h *MessagingHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, role := h.actor(r)
	id := chi.URLParam(r, "id")
	details, err := h.msg.Get(r.Context(), userID, role, id)
	if err != nil {
		h.writeError(w, err, "could not get conversation")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// ListMessages handles GET /conversations/{id}/messages.
// Optional query params:
//   - limit  — page size (service/repo clamp to a safe max)
//   - before — RFC3339 timestamp; return messages strictly older than this (cursor pagination)
// Side effect: marks the conversation read for the viewer.
func (h *MessagingHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	userID, role := h.actor(r)
	id := chi.URLParam(r, "id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit")) // 0 / bad value → repo default

	// Optional "load older messages" cursor. Accept Nano first, then plain RFC3339.
	var before *time.Time
	if raw := r.URL.Query().Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			t, err = time.Parse(time.RFC3339, raw)
		}
		if err != nil {
			utils.Error(w, http.StatusBadRequest, "before must be an RFC3339 timestamp")
			return
		}
		before = &t
	}

	msgs, err := h.msg.ListMessages(r.Context(), userID, role, id, limit, before)
	if err != nil {
		h.writeError(w, err, "could not list messages")
		return
	}
	utils.JSON(w, http.StatusOK, msgs)
}

// SendMessage handles POST /conversations/{id}/messages.
// Body: { "body": "text here", "attachments": [{ "object_path", "mime_type", "size_bytes" }] }.
// Sender is always the JWT subject (never trusted from JSON).
// Attachments must already be uploaded via POST /media/presign-upload (folder chat-image|chat-document).
func (h *MessagingHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	userID, role := h.actor(r)
	id := chi.URLParam(r, "id")
	var req services.SendMessageInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	msg, err := h.msg.SendMessage(r.Context(), userID, role, id, req)
	if err != nil {
		h.writeError(w, err, "could not send message")
		return
	}
	utils.JSON(w, http.StatusCreated, msg)
}

// MarkRead handles POST /conversations/{id}/read.
// Updates conversation_participants.last_read_at without fetching messages
// (useful when the client already has the messages locally).
func (h *MessagingHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	userID, role := h.actor(r)
	id := chi.URLParam(r, "id")
	if err := h.msg.MarkRead(r.Context(), userID, role, id); err != nil {
		h.writeError(w, err, "could not mark conversation read")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Close handles POST /conversations/{id}/close.
// Optional freeze — not automatic. Recommended: use for support when resolved;
// product/order chats usually stay open (AliExpress-like) unless a party chooses to close.
func (h *MessagingHandler) Close(w http.ResponseWriter, r *http.Request) {
	userID, role := h.actor(r)
	id := chi.URLParam(r, "id")
	details, err := h.msg.Close(r.Context(), userID, role, id)
	if err != nil {
		h.writeError(w, err, "could not close conversation")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// Reopen handles POST /conversations/{id}/reopen.
// Optional — opens a closed thread again so messaging can continue.
func (h *MessagingHandler) Reopen(w http.ResponseWriter, r *http.Request) {
	userID, role := h.actor(r)
	id := chi.URLParam(r, "id")
	details, err := h.msg.Reopen(r.Context(), userID, role, id)
	if err != nil {
		h.writeError(w, err, "could not reopen conversation")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// actor pulls the authenticated user id and role from request context.
// RequireAuth middleware must have run first; otherwise these come back empty.
func (h *MessagingHandler) actor(r *http.Request) (userID, role string) {
	userID, _ = r.Context().Value(middleware.UserIDContextKey).(string)
	role, _ = r.Context().Value(middleware.RoleContextKey).(string)
	return userID, role
}

// writeError maps known MessagingService sentinel errors to HTTP responses.
// Unknown errors are logged and returned as a generic 500 with the caller's fallback message
// (so we don't leak internal details to clients).
func (h *MessagingHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidConversation):
		// Bad type / missing required fields / empty body with no attachments / closed thread / bad priority
		utils.Error(w, http.StatusBadRequest, "type must be product_inquiry|order|support; product_inquiry needs product_id; order needs order_item_id; admin support needs counterpart_role+counterpart_user_id; body or attachments required when sending")
	case errors.Is(err, services.ErrInvalidChatAttachment):
		utils.Error(w, http.StatusBadRequest, "attachments need object_path under public/chat/, image/* or application/* mime_type, size_bytes>=0; max 5 files")
	case errors.Is(err, services.ErrForbiddenConversation):
		// e.g. seller tried to start product_inquiry, or admin tried to start order chat
		utils.Error(w, http.StatusForbidden, "role cannot start this conversation type")
	case errors.Is(err, services.ErrProductNotFound):
		utils.Error(w, http.StatusNotFound, "product not found")
	case errors.Is(err, services.ErrOrderItemNotFound):
		utils.Error(w, http.StatusNotFound, "order item not found")
	case errors.Is(err, services.ErrSupportUserNotFound):
		// Counterpart customer/seller missing, or no active admin to assign a ticket to
		utils.Error(w, http.StatusNotFound, "customer, seller, or admin not found")
	case errors.Is(err, services.ErrConversationNotFound), errors.Is(err, services.ErrNotParticipant):
		// Hide "exists but you're not in it" vs "doesn't exist" — both look like 404
		utils.Error(w, http.StatusNotFound, "conversation not found")
	default:
		log.Printf("messaging handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
