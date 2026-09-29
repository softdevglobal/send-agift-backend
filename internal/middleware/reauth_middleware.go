package middleware

import (
	"net/http"
	"strings"

	"myapp/internal/utils"
)

// ReauthHeader carries the token from POST /admin/reauth.
const ReauthHeader = "X-Reauth-Token"

// RequireReauth guards high-risk admin actions — moving prize money or
// points, voiding, settling, cancelling, drawing — behind a password
// confirmed in the last few minutes (Progressive Prize spec §7). Use after
// RequireAuth and RequireRole. The token must belong to the same admin as
// the session making the request.
func RequireReauth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adminID, _ := r.Context().Value(AdminIDContextKey).(string)
			raw := strings.TrimSpace(r.Header.Get(ReauthHeader))
			if raw != "" && adminID != "" {
				if claims, err := utils.ParseJWT(raw, secret); err == nil &&
					claims.Role == "reauth" && claims.Subject == adminID {
					next.ServeHTTP(w, r)
					return
				}
			}
			utils.JSON(w, http.StatusForbidden, map[string]any{
				"error": "confirm your password to continue",
				"code":  "REAUTH_REQUIRED",
			})
		})
	}
}
