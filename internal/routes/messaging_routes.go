package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterMessagingRoutes mounts product/order chat and admin support chat under /api/v1.
//
// Auth stack for every route below:
//  1. RequireAuth     — valid JWT required; puts user_id + role on context
//  2. RequireRole     — customer | seller | admin (superadmin passes as admin)
//
// Product rules (AliExpress-like):
//   - product_inquiry / order: stay open by default; close is optional; order Start auto-reopens if closed
//   - support: close when resolved (manual); start again opens a new ticket if previous is closed
//   - no auto-close job
//
// Endpoints:
//
//	POST   /conversations                      — start (or reuse) a thread
//	GET    /conversations                      — inbox with unread counts
//	GET    /conversations/{id}                 — one thread + participants
//	GET    /conversations/{id}/messages        — messages (marks read)
//	POST   /conversations/{id}/messages        — send text and/or attachments
//	POST   /conversations/{id}/read            — mark read without fetching
//	POST   /conversations/{id}/close           — optional freeze (recommended for support resolve)
//	POST   /conversations/{id}/reopen          — optional reopen after close
//
// Conversation types (POST body "type"):
//   - product_inquiry — customer only; needs product_id
//   - order           — customer or seller; needs order_item_id
//   - support         — admin→user (counterpart_*) or user→admin help ticket
//
// Attachments (damage photos, PDFs):
//  1. POST /media/presign-upload with folder "chat-image" or "chat-document"
//  2. PUT file to upload_url
//  3. POST .../messages with attachments[{object_path, mime_type, size_bytes}]
func RegisterMessagingRoutes(r chi.Router, msg *handlers.MessagingHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		// All messaging routes share the same auth/role gate
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer", "seller", "admin"))

		r.Post("/conversations", msg.Start)                       // create / reuse thread
		r.Get("/conversations", msg.List)                         // caller's inbox
		r.Get("/conversations/{id}", msg.Get)                     // one thread
		r.Get("/conversations/{id}/messages", msg.ListMessages)   // bubbles (+ mark read)
		r.Post("/conversations/{id}/messages", msg.SendMessage)   // send text bubble
		r.Post("/conversations/{id}/read", msg.MarkRead)          // mark read only
		r.Post("/conversations/{id}/close", msg.Close)            // optional close
		r.Post("/conversations/{id}/reopen", msg.Reopen)          // optional reopen
	})
}
