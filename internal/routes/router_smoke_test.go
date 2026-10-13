package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The whole router must build: chi panics at startup on clashing patterns.
func TestRouterBuilds(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "secret")
	for _, path := range []string{
		"/api/v1/competitions/00000000-0000-0000-0000-000000000000/plays",
		"/api/v1/admin/competitions/00000000-0000-0000-0000-000000000000/prize-adjustments",
		"/api/v1/admin/customers/00000000-0000-0000-0000-000000000000/points/adjustments",
		"/api/v1/sellers/me/points/purchases",
		"/api/v1/admin/points/purchases/00000000-0000-0000-0000-000000000000/confirm",
		"/api/v1/sellers/me/shops",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: want 401 before auth, got %d", path, rec.Code)
		}
	}

	// With the flag off, every free gift route says so before anything else.
	for _, route := range [][2]string{
		{http.MethodGet, "/api/v1/free-gifts/products"},
		{http.MethodGet, "/api/v1/customers/me/free-gift"},
		{http.MethodPost, "/api/v1/customers/me/free-gifts"},
		{http.MethodPut, "/api/v1/sellers/me/free-gift-program"},
		{http.MethodDelete, "/api/v1/sellers/me/free-gift-products/00000000-0000-0000-0000-000000000000"},
		{http.MethodPatch, "/api/v1/admin/free-gifts/sellers/00000000-0000-0000-0000-000000000000"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(route[0], route[1], nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "FREE_GIFT_DISABLED") {
			t.Fatalf("%s %s: want 404 FREE_GIFT_DISABLED, got %d %s", route[0], route[1], rec.Code, rec.Body.String())
		}
	}
}
