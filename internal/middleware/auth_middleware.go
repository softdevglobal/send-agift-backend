package middleware

import (
	"context"
	"net/http"
	"strings"

	"myapp/internal/utils"
)

type contextKey string

const (
	AdminIDContextKey contextKey = "admin_id"
	UserIDContextKey  contextKey = "user_id"
	RoleContextKey    contextKey = "role"
)

// RequireAuth reads "Authorization: Bearer <token>", validates the JWT,
// and stores subject + role on the request context.
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
			ctx = context.WithValue(ctx, UserIDContextKey, claims.Subject)
			ctx = context.WithValue(ctx, RoleContextKey, claims.Role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
