package services

import (
	"context"
	"crypto/subtle"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Facebook Limited Login. On an iPhone where the person has not allowed
// tracking, Facebook's SDK signs in without a Graph access token and hands
// the app an OpenID Connect ID token instead. Like Apple's, it is a JWT
// signed by Facebook and is checked against Facebook's published keys.
const (
	facebookLimitedIssuer  = "https://www.facebook.com"
	facebookLimitedKeysURL = "https://limited.facebook.com/.well-known/oauth/openid/jwks/"
)

func (s *SocialAuthService) verifyFacebookLimited(ctx context.Context, in SocialSignInInput) (*SocialProfile, error) {
	type limitedClaims struct {
		jwt.RegisteredClaims
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
		Nonce   string `json:"nonce"`
	}
	claims := &limitedClaims{}
	_, err := jwt.ParseWithClaims(strings.TrimSpace(in.Token), claims,
		func(token *jwt.Token) (any, error) {
			kid, _ := token.Header["kid"].(string)
			if kid == "" {
				return nil, errors.New("no key id")
			}
			return s.signingKey(ctx, &s.facebookKeys, s.facebookKeysURL, facebookLimitedKeysURL, kid)
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(facebookLimitedIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(time.Minute),
	)
	if err != nil {
		return nil, ErrSocialInvalidToken
	}
	audience, _ := claims.GetAudience()
	if !slices.Contains(audience, s.facebookAppID) || claims.Subject == "" {
		return nil, ErrSocialInvalidToken
	}

	// The app starts the sign-in with a one-time value and Facebook puts it in
	// the token unchanged, so a token captured elsewhere cannot be replayed.
	nonce := strings.TrimSpace(in.Nonce)
	if nonce == "" || subtle.ConstantTimeCompare([]byte(nonce), []byte(claims.Nonce)) != 1 {
		return nil, ErrSocialInvalidToken
	}

	// Facebook only puts an email it has confirmed in the token.
	return &SocialProfile{
		Provider: ProviderFacebook, Subject: claims.Subject,
		Email: normalizeEmail(claims.Email), Name: strings.TrimSpace(claims.Name),
		Picture: claims.Picture,
	}, nil
}
