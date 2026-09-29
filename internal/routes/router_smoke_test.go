package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The whole router must build: chi panics at startup on clashing patterns.
func TestRouterBuilds(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "secret")
	for _, path := range []string{
		"/api/v1/competitions/00000000-0000-0000-0000-000000000000/plays",
		"/api/v1/admin/competitions/00000000-0000-0000-0000-000000000000/prize-adjustments",
		"/api/v1/admin/customers/00000000-0000-0000-0000-000000000000/points/adjustments",
		"/api/v1/sellers/me/points/purchases",
		"/api/v1/admin/points/purchases/00000000-0000-0000-0000-000000000000/confirm",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: want 401 before auth, got %d", path, rec.Code)
		}
	}
}
