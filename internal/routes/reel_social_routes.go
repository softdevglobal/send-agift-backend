package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterReelSocialRoutes mounts likes and comments under /reels/{id}/...
//
// Same URLs for guests and logged-in customers:
//   - Customer: Authorization: Bearer <jwt> (role=customer)
//   - Guest:    X-Guest-Token: <uuid>  (no register)
//
// Likes (separate from comments):
//
//	GET    /reels/{id}/likes                 — PUBLIC (like_count + recent_likers)
//	POST   /reels/{id}/likes                 — like (JWT or guest)
//	DELETE /reels/{id}/likes                 — unlike
//	GET    /reels/{id}/likes/me              — did I like? (JWT or guest)
//
// Comments:
//
//	GET    /reels/{id}/comments              — public list
//	POST   /reels/{id}/comments              — create (JWT or guest)
//	PUT    /reels/{id}/comments/{commentId}  — edit own
//	DELETE /reels/{id}/comments/{commentId}  — delete own
func RegisterReelSocialRoutes(r chi.Router, social *handlers.ReelSocialHandler, jwtSecret string) {
	// Fully public — no auth required (recent_likers + like_count).
	r.Get("/reels/{id}/likes", social.GetLikes)
	r.Get("/reels/{id}/comments", social.ListComments)

	// Writes + liked_by_me need customer JWT or guest token (same paths).
	r.Group(func(r chi.Router) {
		r.Use(middleware.OptionalCustomerOrGuest(jwtSecret, true))

		r.Post("/reels/{id}/likes", social.Like)
		r.Delete("/reels/{id}/likes", social.Unlike)
		r.Get("/reels/{id}/likes/me", social.LikedByMe)

		r.Post("/reels/{id}/comments", social.CreateComment)
		r.Put("/reels/{id}/comments/{commentId}", social.UpdateComment)
		r.Delete("/reels/{id}/comments/{commentId}", social.DeleteComment)
	})
}
