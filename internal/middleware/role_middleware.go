package middleware

import (
	"net/http"
	"strings"

	"myapp/internal/utils"
)

// RequireRole allows the request only when the JWT role is one of the allowed values.
// Use after RequireAuth.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	// allowedSet is a map of allowed roles
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, role := range allowed {
		allowedSet[strings.ToLower(role)] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, _ := r.Context().Value(RoleContextKey).(string)
			role = strings.ToLower(role)

			if _, ok := allowedSet[role]; !ok {
				utils.Error(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
