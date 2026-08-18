package middleware

import (
	"context" // context is used to manage the lifecycle of a request
	"net/http" // net/http is used to create and manage HTTP servers and clients
	"strings" // strings is used to manipulate strings

	"myapp/internal/utils" // utils is used to manage the utilities of the application
)

type contextKey string // contextKey is a type that represents a key for a context

const (
	AdminIDContextKey contextKey = "admin_id" // AdminIDContextKey is a key for the admin ID in the context
	UserIDContextKey  contextKey = "user_id" // UserIDContextKey is a key for the user ID in the context
	RoleContextKey    contextKey = "role" // RoleContextKey is a key for the role in the context
)

// RequireAuth reads "Authorization: Bearer <token>", validates the JWT,
// and stores subject + role on the request context.
func RequireAuth(secret string) func(http.Handler) http.Handler {
	// return the middleware function that wraps the next handler 
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { 
			header := r.Header.Get("Authorization") // get the authorization header from the request
			if !strings.HasPrefix(header, "Bearer ") { // if the authorization header does not have the bearer token, return an error
				utils.Error(w, http.StatusUnauthorized, "missing bearer token")
				return // return the error
			}
			tokenStr := strings.TrimPrefix(header, "Bearer ") // trim the bearer token from the authorization header

			claims, err := utils.ParseJWT(tokenStr, secret)
			if err != nil { // if the token is invalid or expired, return an error
				utils.Error(w, http.StatusUnauthorized, "invalid or expired token")
				return // return the error
			}

			ctx := context.WithValue(r.Context(), AdminIDContextKey, claims.Subject) // add the admin ID to the context
			ctx = context.WithValue(ctx, UserIDContextKey, claims.Subject) // add the user ID to the context
			ctx = context.WithValue(ctx, RoleContextKey, claims.Role) // add the role to the context			
			next.ServeHTTP(w, r.WithContext(ctx)) // serve the request with the context	
		}) // return the next handler
	} // return the middleware function
}
