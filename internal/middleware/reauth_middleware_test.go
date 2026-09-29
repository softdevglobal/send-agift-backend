package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"myapp/internal/utils"
)

func TestRequireReauth(t *testing.T) {
	const secret = "s3cret"
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := RequireReauth(secret)(ok)

	call := func(adminID, header string) int {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req = req.WithContext(context.WithValue(req.Context(), AdminIDContextKey, adminID))
		if header != "" {
			req.Header.Set(ReauthHeader, header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	fresh, _ := utils.GenerateJWT("admin-1", "a@x", "reauth", secret, time.Minute)
	expired, _ := utils.GenerateJWT("admin-1", "a@x", "reauth", secret, -time.Minute)
	login, _ := utils.GenerateJWT("admin-1", "a@x", "superadmin", secret, time.Minute)
	other, _ := utils.GenerateJWT("admin-2", "b@x", "reauth", secret, time.Minute)

	for name, tc := range map[string]struct {
		header string
		want   int
	}{
		"fresh confirmation":       {fresh, http.StatusNoContent},
		"no confirmation":          {"", http.StatusForbidden},
		"expired confirmation":     {expired, http.StatusForbidden},
		"a login token is not one": {login, http.StatusForbidden},
		"another admin's token":    {other, http.StatusForbidden},
		"garbage":                  {"not-a-token", http.StatusForbidden},
	} {
		if got := call("admin-1", tc.header); got != tc.want {
			t.Errorf("%s: got %d, want %d", name, got, tc.want)
		}
	}
}

func TestDeviceID(t *testing.T) {
	for raw, want := range map[string]string{
		"abcd1234-EF":            "abcd1234-EF",
		"short":                  "",
		"has spaces in it here":  "",
		"<script>alert(1)</scr>": "",
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(DeviceIDHeader, raw)
		if got := DeviceID(req); got != want {
			t.Errorf("DeviceID(%q) = %q, want %q", raw, got, want)
		}
	}
}
