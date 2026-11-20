package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testFacebookApp = "1745907156491235"

func TestFacebookLimitedLogin(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "fb-kid", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	svc := NewSocialAuthService(nil, nil, nil, nil, nil, testFacebookApp, "app-secret", "test-secret", time.Hour)
	svc.facebookKeysURL = srv.URL

	sign := func(signer *rsa.PrivateKey, mutate func(jwt.MapClaims)) string {
		claims := jwt.MapClaims{
			"iss": facebookLimitedIssuer, "aud": testFacebookApp, "sub": "10225",
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
			"nonce": "raw-nonce", "name": "Pawan D", "email": "Pawan@Example.com",
			"picture": "https://example.com/p.jpg",
		}
		if mutate != nil {
			mutate(claims)
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "fb-kid"
		s, err := tok.SignedString(signer)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	verify := func(token, nonce string) (*SocialProfile, error) {
		return svc.verify(context.Background(), SocialSignInInput{
			Provider: "facebook", Token: token, TokenType: "id_token", Nonce: nonce,
		})
	}

	t.Run("a good limited token gives the person", func(t *testing.T) {
		p, err := verify(sign(key, nil), "raw-nonce")
		if err != nil {
			t.Fatal(err)
		}
		if p.Provider != ProviderFacebook || p.Subject != "10225" || p.Email != "pawan@example.com" || p.Name != "Pawan D" {
			t.Fatalf("profile: %+v", p)
		}
	})

	refused := map[string]struct {
		token string
		nonce string
	}{
		"another app":      {sign(key, func(c jwt.MapClaims) { c["aud"] = "999" }), "raw-nonce"},
		"another issuer":   {sign(key, func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }), "raw-nonce"},
		"expired":          {sign(key, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }), "raw-nonce"},
		"wrong nonce":      {sign(key, nil), "different"},
		"missing nonce":    {sign(key, nil), ""},
		"no nonce in it":   {sign(key, func(c jwt.MapClaims) { delete(c, "nonce") }), "raw-nonce"},
		"signed elsewhere": {func() string { other, _ := rsa.GenerateKey(rand.Reader, 2048); return sign(other, nil) }(), "raw-nonce"},
	}
	for name, tc := range refused {
		t.Run("refused: "+name, func(t *testing.T) {
			if _, err := verify(tc.token, tc.nonce); !errors.Is(err, ErrSocialInvalidToken) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
