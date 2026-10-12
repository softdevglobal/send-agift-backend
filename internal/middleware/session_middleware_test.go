package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"myapp/internal/utils"
)

type fakeSessions struct {
	after time.Time
	err   error
}

func (f fakeSessions) SessionsValidAfter(context.Context, string, string) (time.Time, error) {
	return f.after, f.err
}

func TestRequireAuthEndsSessionsBeforeTheCutoff(t *testing.T) {
	const secret = "s3cret"
	t.Cleanup(func() { UseSessionChecker(nil) })

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := RequireAuth(secret)(ok)
	call := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	customer, _ := utils.GenerateJWT("c-1", "a@x", "customer", secret, time.Hour)
	seller, _ := utils.GenerateJWT("s-1", "s@x", "seller", secret, time.Hour)
	admin, _ := utils.GenerateJWT("a-1", "a@x", "superadmin", secret, time.Hour)

	UseSessionChecker(fakeSessions{})
	if got := call(customer); got != http.StatusNoContent {
		t.Fatalf("no cut-off: want 204, got %d", got)
	}

	UseSessionChecker(fakeSessions{after: time.Now().Add(time.Minute)})
	if got := call(customer); got != http.StatusUnauthorized {
		t.Fatalf("token older than the cut-off: want 401, got %d", got)
	}
	if got := call(seller); got != http.StatusUnauthorized {
		t.Fatalf("a seller token older than the cut-off: want 401, got %d", got)
	}
	if got := call(admin); got != http.StatusNoContent {
		t.Fatalf("an admin token is never cut off: want 204, got %d", got)
	}

	UseSessionChecker(fakeSessions{after: time.Now().Add(-time.Minute)})
	if got := call(customer); got != http.StatusNoContent {
		t.Fatalf("token newer than the cut-off: want 204, got %d", got)
	}

	UseSessionChecker(fakeSessions{err: errors.New("db down")})
	if got := call(customer); got != http.StatusInternalServerError {
		t.Fatalf("checker error must not let the token through: want 500, got %d", got)
	}
}
