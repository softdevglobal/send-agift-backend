package middleware

import (
	"net/http"
	"strings"

	"myapp/internal/utils"
)

// RequireRole allows the request only when the JWT role is one of the allowed values.
// Use after RequireAuth. Treats "superadmin" as admin-capable.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, role := range allowed {
		allowedSet[strings.ToLower(role)] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, _ := r.Context().Value(RoleContextKey).(string)
			role = strings.ToLower(role)

			if _, ok := allowedSet[role]; !ok {
				// superadmin can access admin-only routes
				if _, adminOK := allowedSet["admin"]; adminOK && role == "superadmin" {
					next.ServeHTTP(w, r)
					return
				}
				utils.Error(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
