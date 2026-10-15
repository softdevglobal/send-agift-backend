package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testAppleBundle = "com.sendagift.sendAgiftMobile"

// appleFixture stands in for Apple: a real RSA key, a key set served over
// HTTP, and a way to mint identity tokens signed with it.
type appleFixture struct {
	t       *testing.T
	key     *rsa.PrivateKey
	fetches atomic.Int32
	svc     *SocialAuthService
}

func newAppleFixture(t *testing.T) *appleFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &appleFixture{t: t, key: key}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		e := big.NewInt(int64(key.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "test-kid", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(e),
		}}})
	}))
	t.Cleanup(srv.Close)
	f.svc = NewSocialAuthService(nil, nil, nil, nil, nil, "", "", "test-secret", time.Hour).
		EnableApple([]string{testAppleBundle})
	f.svc.appleKeysURL = srv.URL
	return f
}

func (f *appleFixture) token(mutate func(jwt.MapClaims)) string {
	f.t.Helper()
	claims := jwt.MapClaims{
		"iss": appleIssuer, "aud": testAppleBundle, "sub": "001234.apple.user",
		"iat": time.Now().Unix(), "exp": time.Now().Add(10 * time.Minute).Unix(),
		"email": "Jane@Example.com", "email_verified": "true",
	}
	if mutate != nil {
		mutate(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "test-kid"
	signed, err := tok.SignedString(f.key)
	if err != nil {
		f.t.Fatal(err)
	}
	return signed
}

func (f *appleFixture) verify(token, nonce string) (*SocialProfile, error) {
	return f.svc.verify(context.Background(), SocialSignInInput{
		Provider: "apple", Token: token, Nonce: nonce, Name: "Jane Appleseed",
	})
}

func TestAppleIdentityToken(t *testing.T) {
	f := newAppleFixture(t)

	t.Run("a good token gives the person", func(t *testing.T) {
		p, err := f.verify(f.token(nil), "")
		if err != nil {
			t.Fatal(err)
		}
		if p.Provider != ProviderApple || p.Subject != "001234.apple.user" ||
			p.Email != "jane@example.com" || p.Name != "Jane Appleseed" {
			t.Fatalf("profile: %+v", p)
		}
	})

	t.Run("the email is only used when Apple verified it", func(t *testing.T) {
		for _, verified := range []any{"false", false, nil} {
			p, err := f.verify(f.token(func(c jwt.MapClaims) { c["email_verified"] = verified }), "")
			if err != nil || p.Email != "" {
				t.Fatalf("email_verified=%v: email %q err %v", verified, p.Email, err)
			}
		}
		// Apple also sends the flag as a real boolean.
		p, err := f.verify(f.token(func(c jwt.MapClaims) { c["email_verified"] = true }), "")
		if err != nil || p.Email != "jane@example.com" {
			t.Fatalf("boolean flag: %q %v", p.Email, err)
		}
	})

	t.Run("a token for another app is refused", func(t *testing.T) {
		_, err := f.verify(f.token(func(c jwt.MapClaims) { c["aud"] = "com.someone.else" }), "")
		if !errors.Is(err, ErrSocialInvalidToken) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("a token from another issuer is refused", func(t *testing.T) {
		_, err := f.verify(f.token(func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }), "")
		if !errors.Is(err, ErrSocialInvalidToken) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("an expired token is refused", func(t *testing.T) {
		_, err := f.verify(f.token(func(c jwt.MapClaims) {
			c["exp"] = time.Now().Add(-time.Hour).Unix()
		}), "")
		if !errors.Is(err, ErrSocialInvalidToken) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("a token signed by someone else is refused", func(t *testing.T) {
		other, _ := rsa.GenerateKey(rand.Reader, 2048)
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": appleIssuer, "aud": testAppleBundle, "sub": "x",
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = "test-kid"
		forged, _ := tok.SignedString(other)
		if _, err := f.verify(forged, ""); !errors.Is(err, ErrSocialInvalidToken) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("an unsigned token is refused", func(t *testing.T) {
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
			"iss": appleIssuer, "aud": testAppleBundle, "sub": "x",
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = "test-kid"
		unsigned, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if _, err := f.verify(unsigned, ""); !errors.Is(err, ErrSocialInvalidToken) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("the nonce must match what the sign-in started with", func(t *testing.T) {
		sum := sha256.Sum256([]byte("raw-nonce"))
		withNonce := f.token(func(c jwt.MapClaims) { c["nonce"] = hex.EncodeToString(sum[:]) })

		if _, err := f.verify(withNonce, "raw-nonce"); err != nil {
			t.Fatalf("matching nonce: %v", err)
		}
		if _, err := f.verify(withNonce, "different"); !errors.Is(err, ErrSocialInvalidToken) {
			t.Fatalf("wrong nonce: %v", err)
		}
		if _, err := f.verify(withNonce, ""); !errors.Is(err, ErrSocialInvalidToken) {
			t.Fatalf("missing nonce: %v", err)
		}
	})

	t.Run("Apple's keys are fetched once, not per sign-in", func(t *testing.T) {
		before := f.fetches.Load()
		for i := 0; i < 5; i++ {
			if _, err := f.verify(f.token(nil), ""); err != nil {
				t.Fatal(err)
			}
		}
		if got := f.fetches.Load() - before; got != 0 {
			t.Fatalf("keys fetched %d more times", got)
		}
	})
}

func TestAppleNotConfigured(t *testing.T) {
	s := NewSocialAuthService(nil, nil, nil, nil, nil, "", "", "x", time.Hour)
	_, err := s.verify(context.Background(), SocialSignInInput{Provider: "apple", Token: "t"})
	if !errors.Is(err, ErrSocialNotConfigured) {
		t.Fatalf("got %v", err)
	}
}
