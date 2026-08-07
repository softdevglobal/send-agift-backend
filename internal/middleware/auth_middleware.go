package middleware

import (
	"context"
	"net/http"
	"strings"

	"myapp/internal/utils"
)

type contextKey string

const AdminIDContextKey contextKey = "admin_id"

// RequireAuth reads "Authorization: Bearer <token>", validates the JWT,
// and stores the admin id (the "sub" claim) on the request context so
// downstream handlers can read it via r.Context().Value(AdminIDContextKey).
func RequireAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				utils.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			tokenStr := strings.TrimPrefix(header, "Bearer ")

			claims, err := utils.ParseJWT(tokenStr, secret)
			if err != nil {
				utils.Error(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), AdminIDContextKey, claims.Subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}