package services

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ProviderApple is Sign in with Apple. The app sends the identity token Apple
// gave it; the token is a JWT signed by Apple, so it is checked against
// Apple's published keys rather than by calling Apple for each sign-in.
const ProviderApple = "apple"

const (
	appleIssuer  = "https://appleid.apple.com"
	appleKeysURL = "https://appleid.apple.com/auth/keys"

	// appleKeysTTL is how long Apple's keys are reused. They change rarely,
	// and an unknown key id forces a refresh sooner.
	appleKeysTTL = time.Hour
	// appleKeysMinRefetch stops a stream of bad tokens becoming a stream of
	// requests to Apple.
	appleKeysMinRefetch = time.Minute
)

// appleKeyCache holds Apple's signing keys by key id.
type appleKeyCache struct {
	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// EnableApple turns on Sign in with Apple. clientIDs are the identifiers
// Apple issues tokens for: the iPhone app's bundle id, and a Services ID if a
// website button is added later. Without any, Apple sign-in answers 503, as
// Google and Facebook do when unconfigured.
func (s *SocialAuthService) EnableApple(clientIDs []string) *SocialAuthService {
	s.appleClientIDs = clientIDs
	return s
}

func (s *SocialAuthService) verifyApple(ctx context.Context, in SocialSignInInput) (*SocialProfile, error) {
	if len(s.appleClientIDs) == 0 {
		return nil, ErrSocialNotConfigured
	}
	type appleClaims struct {
		jwt.RegisteredClaims
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		Nonce         string `json:"nonce"`
	}
	claims := &appleClaims{}
	_, err := jwt.ParseWithClaims(strings.TrimSpace(in.Token), claims,
		func(token *jwt.Token) (any, error) {
			kid, _ := token.Header["kid"].(string)
			if kid == "" {
				return nil, errors.New("no key id")
			}
			return s.appleKey(ctx, kid)
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(appleIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(time.Minute),
	)
	if err != nil {
		return nil, ErrSocialInvalidToken
	}
	audience, _ := claims.GetAudience()
	issuedToUs := false
	for _, aud := range audience {
		if slices.Contains(s.appleClientIDs, aud) {
			issuedToUs = true
		}
	}
	if !issuedToUs || claims.Subject == "" {
		return nil, ErrSocialInvalidToken
	}

	// A nonce ties the token to the sign-in that asked for it, so a token
	// captured elsewhere cannot be replayed. The app sends the raw value and
	// Apple's token carries its SHA-256.
	if claims.Nonce != "" || strings.TrimSpace(in.Nonce) != "" {
		sum := sha256.Sum256([]byte(strings.TrimSpace(in.Nonce)))
		want := hex.EncodeToString(sum[:])
		if strings.TrimSpace(in.Nonce) == "" ||
			subtle.ConstantTimeCompare([]byte(want), []byte(claims.Nonce)) != 1 {
			return nil, ErrSocialInvalidToken
		}
	}

	// Only an email Apple has verified is used; a private relay address
	// counts, since mail to it reaches the person.
	email := ""
	switch v := claims.EmailVerified.(type) {
	case bool:
		if v {
			email = normalizeEmail(claims.Email)
		}
	case string:
		if v == "true" {
			email = normalizeEmail(claims.Email)
		}
	}
	// Apple gives the name to the app, once, on the first sign-in, not in
	// the token. The app passes it along; it is only a suggestion for the
	// profile form.
	return &SocialProfile{
		Provider: ProviderApple, Subject: claims.Subject, Email: email,
		Name: strings.TrimSpace(in.Name),
	}, nil
}

// appleKey returns Apple's public key for a key id, from the cache while it
// is fresh, and from Apple when it is stale or the id is new.
func (s *SocialAuthService) appleKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c := &s.appleKeys
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	fresh := c.keys != nil && now.Sub(c.fetchedAt) < appleKeysTTL
	if key, ok := c.keys[kid]; ok && fresh {
		return key, nil
	}
	// Unknown id, or stale: fetch, but not more than once a minute.
	if c.keys == nil || now.Sub(c.fetchedAt) >= appleKeysMinRefetch {
		keys, err := s.fetchAppleKeys(ctx)
		if err != nil {
			if key, ok := c.keys[kid]; ok {
				return key, nil // Apple unreachable: the last known key still verifies
			}
			return nil, err
		}
		c.keys, c.fetchedAt = keys, now
	}
	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("apple key %q not found", kid)
}

func (s *SocialAuthService) fetchAppleKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	url := s.appleKeysURL
	if url == "" {
		url = appleKeysURL
	}
	if err := s.getJSON(ctx, url, "", &set); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil || len(e) == 0 {
			continue
		}
		exp := 0
		for _, b := range e {
			exp = exp<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}
	}
	if len(keys) == 0 {
		return nil, errors.New("apple returned no signing keys")
	}
	return keys, nil
}
