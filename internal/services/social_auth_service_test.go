package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/database"
	"myapp/internal/repository"
)

const testGoogleClient = "web-client.apps.googleusercontent.com"

// fakeProviders answers like Google's tokeninfo/userinfo and Facebook's Graph
// API for a handful of known tokens.
func fakeProviders(t *testing.T, email string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/tokeninfo" && q.Get("id_token") == "good-id":
			write(map[string]string{"aud": testGoogleClient, "sub": "g-123", "iss": "accounts.google.com",
				"email": email, "email_verified": "true", "name": "Gina Google"})
		case r.URL.Path == "/tokeninfo" && q.Get("id_token") == "other-app":
			write(map[string]string{"aud": "someone-else", "sub": "g-9", "iss": "accounts.google.com",
				"email": email, "email_verified": "true"})
		case r.URL.Path == "/tokeninfo" && q.Get("id_token") == "unverified":
			write(map[string]string{"aud": testGoogleClient, "sub": "g-7", "iss": "accounts.google.com",
				"email": email, "email_verified": "false"})
		case r.URL.Path == "/tokeninfo":
			w.WriteHeader(http.StatusBadRequest)
			write(map[string]string{"error": "invalid_token"})
		case r.URL.Path == "/debug_token":
			valid := q.Get("input_token") == "good-fb"
			appID := "fb-app"
			if q.Get("input_token") == "other-fb-app" {
				valid, appID = true, "not-ours"
			}
			write(map[string]any{"data": map[string]any{"app_id": appID, "is_valid": valid, "user_id": "fb-55"}})
		case r.URL.Path == "/me":
			if q.Get("appsecret_proof") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			write(map[string]any{"id": "fb-55", "name": "Finn Facebook", "email": email})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testSocialService(srvURL string) *SocialAuthService {
	s := NewSocialAuthService(nil, nil, nil, nil, []string{testGoogleClient}, "fb-app", "fb-secret", "test-secret", time.Hour)
	s.googleTokenInfoURL = srvURL + "/tokeninfo"
	s.googleUserInfoURL = srvURL + "/userinfo"
	s.facebookGraphURL = srvURL
	return s
}

func TestSocialVerifyChecksTheTokenWasIssuedToUs(t *testing.T) {
	srv := fakeProviders(t, "Person@Example.com")
	s := testSocialService(srv.URL)
	ctx := context.Background()

	p, err := s.verify(ctx, SocialSignInInput{Provider: "google", Token: "good-id"})
	if err != nil || p.Subject != "g-123" || p.Email != "person@example.com" {
		t.Fatalf("google id token: %+v %v", p, err)
	}
	if _, err := s.verify(ctx, SocialSignInInput{Provider: "google", Token: "other-app"}); !errors.Is(err, ErrSocialInvalidToken) {
		t.Fatalf("token for another client accepted: %v", err)
	}
	if _, err := s.verify(ctx, SocialSignInInput{Provider: "google", Token: "forged"}); !errors.Is(err, ErrSocialInvalidToken) {
		t.Fatalf("forged token: %v", err)
	}
	if p, _ := s.verify(ctx, SocialSignInInput{Provider: "google", Token: "unverified"}); p == nil || p.Email != "" {
		t.Fatalf("an unverified google email must not be trusted: %+v", p)
	}

	p, err = s.verify(ctx, SocialSignInInput{Provider: "facebook", Token: "good-fb"})
	if err != nil || p.Subject != "fb-55" || p.Email != "person@example.com" {
		t.Fatalf("facebook token: %+v %v", p, err)
	}
	if _, err := s.verify(ctx, SocialSignInInput{Provider: "facebook", Token: "other-fb-app"}); !errors.Is(err, ErrSocialInvalidToken) {
		t.Fatalf("facebook token for another app accepted: %v", err)
	}
	if _, err := s.verify(ctx, SocialSignInInput{Provider: "facebook", Token: "nope"}); !errors.Is(err, ErrSocialInvalidToken) {
		t.Fatalf("invalid facebook token: %v", err)
	}

	unconfigured := NewSocialAuthService(nil, nil, nil, nil, nil, "", "", "x", time.Hour)
	if _, err := unconfigured.verify(ctx, SocialSignInInput{Provider: "google", Token: "t"}); !errors.Is(err, ErrSocialNotConfigured) {
		t.Fatalf("unconfigured google: %v", err)
	}
}

func TestSocialSignupTokenRoundTrip(t *testing.T) {
	s := testSocialService("http://unused")
	raw, err := s.signupToken(&SocialProfile{Provider: "google", Subject: "g-1", Email: "a@b.co", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.readSignupToken(raw)
	if err != nil || p.Email != "a@b.co" || p.Subject != "g-1" {
		t.Fatalf("round trip: %+v %v", p, err)
	}
	other := testSocialService("http://unused")
	other.jwtSecret = "different"
	if _, err := other.readSignupToken(raw); !errors.Is(err, ErrSocialSignupExpired) {
		t.Fatal("a token signed with another secret was accepted")
	}
	if _, err := s.readSignupToken(raw[:len(raw)-2] + "xx"); !errors.Is(err, ErrSocialSignupExpired) {
		t.Fatal("a tampered token was accepted")
	}
}

// The whole flow against a database:
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/services -run SocialFlow -v
func TestSocialFlowCreatesThenSignsIn(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var country uuid.UUID
	if err := pool.QueryRow(ctx, `select id from core.countries order by created_at limit 1`).Scan(&country); err != nil {
		t.Skipf("no country: %v", err)
	}

	email := "social-" + uuid.NewString()[:8] + "@example.test"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from customer.customers where email = $1`, email)
	})

	srv := fakeProviders(t, email)
	customers := repository.NewCustomerRepository(pool)
	countries := repository.NewCountryRepository(pool)
	s := NewSocialAuthService(customers, countries,
		NewCountryCapabilityService(repository.NewCountryCapabilityRepository(pool), countries), nil,
		[]string{testGoogleClient}, "fb-app", "fb-secret", "test-secret", time.Hour)
	s.googleTokenInfoURL = srv.URL + "/tokeninfo"
	s.facebookGraphURL = srv.URL

	// A new person is asked to finish their profile; nothing is created yet.
	first, err := s.SignIn(ctx, SocialSignInInput{Provider: "google", Token: "good-id"})
	if err != nil || first.Status != "needs_profile" || first.SignupToken == "" || first.Name != "Gina Google" {
		t.Fatalf("first sign-in: %+v %v", first, err)
	}
	if _, err := customers.GetByEmail(ctx, email); err == nil {
		t.Fatal("account created before the profile was finished")
	}

	if _, err := s.Complete(ctx, SocialCompleteInput{SignupToken: first.SignupToken, CountryID: country.String()}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing phone must be refused: %v", err)
	}
	done, err := s.Complete(ctx, SocialCompleteInput{SignupToken: first.SignupToken, CountryID: country.String(), Phone: "+94 77 123 4567"})
	if err != nil || done.Status != "signed_in" || done.Token == "" {
		t.Fatalf("complete: %+v %v", done, err)
	}
	created, err := customers.GetByEmail(ctx, email)
	if err != nil || created.Phone == nil || *created.DisplayName != "Gina Google" {
		t.Fatalf("created customer: %+v %v", created, err)
	}

	// Next time, Google signs them straight in.
	again, err := s.SignIn(ctx, SocialSignInInput{Provider: "google", Token: "good-id"})
	if err != nil || again.Status != "signed_in" {
		t.Fatalf("returning google sign-in: %+v %v", again, err)
	}
	// Facebook with the same verified email links to the same account.
	fb, err := s.SignIn(ctx, SocialSignInInput{Provider: "facebook", Token: "good-fb"})
	if err != nil || fb.Status != "signed_in" {
		t.Fatalf("facebook with the same email: %+v %v", fb, err)
	}
	var links int
	_ = pool.QueryRow(ctx, `select count(*) from customer.social_identities where customer_id = $1`, created.ID).Scan(&links)
	if links != 2 {
		t.Fatalf("want google and facebook linked, got %d", links)
	}
	if strings.Contains(created.PasswordHash, "00001111") {
		t.Fatal("unexpected password")
	}
}
