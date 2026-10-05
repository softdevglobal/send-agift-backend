package services

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

var (
	// ErrSocialNotConfigured is a provider whose keys are not set.
	ErrSocialNotConfigured = errors.New("This sign-in option is not available right now.")
	// ErrSocialInvalidToken is a token the provider did not vouch for.
	ErrSocialInvalidToken = errors.New("We could not verify your sign-in. Please try again.")
	// ErrSocialNoEmail is a provider account that shares no verified email.
	ErrSocialNoEmail = errors.New("Your account did not share a verified email address. Please sign up with your email instead.")
	// ErrSocialEmailInUse is an email that signs in to a seller or admin account.
	ErrSocialEmailInUse = errors.New("This email is already used by a seller or admin account. Please sign in with your email and password instead.")
	// ErrSocialSignupExpired is a profile-completion token that is invalid or too old.
	ErrSocialSignupExpired = errors.New("Your sign-up session expired. Please choose Google or Facebook again.")
)

const (
	ProviderGoogle   = "google"
	ProviderFacebook = "facebook"

	// socialSignupTTL is how long a new customer has to finish their profile.
	socialSignupTTL = 30 * time.Minute
	socialSignupUse = "social_signup"
)

// SocialProfile is what a provider confirmed about the person signing in.
type SocialProfile struct {
	Provider string `json:"provider"`
	Subject  string `json:"sub"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Picture  string `json:"picture"`
}

// SocialSignInResult signs an existing customer in, or asks a new one to
// finish their profile (country and phone) before the account is made.
type SocialSignInResult struct {
	Status string `json:"status"` // signed_in | needs_profile
	Token  string `json:"token,omitempty"`
	Role   string `json:"role,omitempty"`

	SignupToken string `json:"signup_token,omitempty"`
	Email       string `json:"email,omitempty"`
	Name        string `json:"name,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
}

// SocialAuthService signs customers in with Google and Facebook.
type SocialAuthService struct {
	customers    *repository.CustomerRepository
	countries    *repository.CountryRepository
	capabilities *CountryCapabilityService
	email        *EmailService
	http         *http.Client

	googleClientIDs   []string
	facebookAppID     string
	facebookAppSecret string
	jwtSecret         string
	jwtExpiry         time.Duration

	// Provider endpoints; tests point these at a fake server.
	googleTokenInfoURL string
	googleUserInfoURL  string
	facebookGraphURL   string
}

func NewSocialAuthService(
	customers *repository.CustomerRepository,
	countries *repository.CountryRepository,
	capabilities *CountryCapabilityService,
	email *EmailService,
	googleClientIDs []string,
	facebookAppID, facebookAppSecret string,
	jwtSecret string,
	jwtExpiry time.Duration,
) *SocialAuthService {
	return &SocialAuthService{
		customers: customers, countries: countries, capabilities: capabilities, email: email,
		http:               &http.Client{Timeout: 10 * time.Second},
		googleClientIDs:    googleClientIDs,
		facebookAppID:      facebookAppID,
		facebookAppSecret:  facebookAppSecret,
		jwtSecret:          jwtSecret,
		jwtExpiry:          jwtExpiry,
		googleTokenInfoURL: "https://oauth2.googleapis.com/tokeninfo",
		googleUserInfoURL:  "https://www.googleapis.com/oauth2/v3/userinfo",
		facebookGraphURL:   "https://graph.facebook.com/v21.0",
	}
}

// SocialSignInInput is a token from the provider's own sign-in. Google sends
// an ID token (web One Tap, mobile) or an access token (web button); Facebook
// always sends an access token.
type SocialSignInInput struct {
	Provider  string `json:"provider"`
	Token     string `json:"token"`
	TokenType string `json:"token_type"` // id_token | access_token
}

// SignIn verifies the token with its provider, then signs the customer in:
// the account already linked to this provider account, or the customer with
// the same verified email (which links it). Anyone else is asked to finish
// their profile.
func (s *SocialAuthService) SignIn(ctx context.Context, in SocialSignInInput) (*SocialSignInResult, error) {
	profile, err := s.verify(ctx, in)
	if err != nil {
		return nil, err
	}

	if id, err := s.customers.FindSocialIdentity(ctx, profile.Provider, profile.Subject); err == nil {
		customer, err := s.customers.GetByID(ctx, id.String())
		if err == nil && customer.Status == "active" {
			return s.signedIn(customer)
		}
	} else if !errors.Is(err, repository.ErrSocialIdentityNotFound) {
		return nil, err
	}

	if profile.Email == "" {
		return nil, ErrSocialNoEmail
	}
	existing, err := s.customers.GetByEmail(ctx, profile.Email)
	if err == nil {
		if err := s.customers.LinkSocialIdentity(ctx, existing.ID, profile.Provider, profile.Subject, profile.Email); err != nil {
			return nil, err
		}
		// An account made for a gift recipient, still on its emailed default
		// password: the provider has just proved who owns the email, so that
		// guessable password stops working.
		if existing.PasswordChangeRequired {
			if err := s.retirePassword(ctx, existing.ID.String()); err != nil {
				return nil, err
			}
		}
		return s.signedIn(existing)
	}
	if !errors.Is(err, repository.ErrCustomerNotFound) {
		return nil, err
	}
	if used, err := s.customers.EmailUsedByStaffOrSeller(ctx, profile.Email); err != nil {
		return nil, err
	} else if used {
		return nil, ErrSocialEmailInUse
	}

	signup, err := s.signupToken(profile)
	if err != nil {
		return nil, err
	}
	return &SocialSignInResult{
		Status: "needs_profile", SignupToken: signup,
		Email: profile.Email, Name: profile.Name, ImageURL: profile.Picture,
	}, nil
}

// SocialCompleteInput finishes a social sign-up with what a customer
// account needs that the provider doesn't give.
type SocialCompleteInput struct {
	SignupToken  string `json:"signup_token"`
	CountryID    string `json:"country_id"`
	Phone        string `json:"phone"`
	CustomerType string `json:"customer_type"`
	DisplayName  string `json:"display_name"`
}

// Complete creates the customer account for a verified social sign-up and
// signs them in.
func (s *SocialAuthService) Complete(ctx context.Context, in SocialCompleteInput) (*SocialSignInResult, error) {
	profile, err := s.readSignupToken(in.SignupToken)
	if err != nil {
		return nil, err
	}
	phone := strings.TrimSpace(in.Phone)
	if phone == "" {
		return nil, ErrInvalidInput
	}
	countryID, err := uuid.Parse(strings.TrimSpace(in.CountryID))
	if err != nil {
		return nil, ErrInvalidCountry
	}
	if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry
		}
		return nil, err
	}
	if err := s.capabilities.EnsureCustomerRegistrationAllowed(ctx, countryID.String()); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		name = profile.Name
	}
	if name == "" {
		name = strings.Split(profile.Email, "@")[0]
	}
	customerType := strings.TrimSpace(in.CustomerType)
	if customerType == "" {
		customerType = "individual"
	}
	hash, err := randomPasswordHash()
	if err != nil {
		return nil, err
	}
	var image *string
	if profile.Picture != "" {
		image = &profile.Picture
	}
	customer := &models.Customer{
		CountryID:    countryID,
		Email:        profile.Email,
		Phone:        &phone,
		PasswordHash: hash,
		DisplayName:  &name,
		CustomerType: customerType,
		Status:       "active",
		ImageURL:     image,
	}
	if err := s.customers.Create(ctx, customer); err != nil {
		if !errors.Is(err, repository.ErrCustomerDuplicate) {
			return nil, err
		}
		// Made meanwhile (a second tab, or a gift for them): sign in to it.
		existing, getErr := s.customers.GetByEmail(ctx, profile.Email)
		if getErr != nil {
			return nil, ErrCustomerConflict
		}
		customer = existing
	} else if err := s.email.SendCustomerWelcome(ctx, customer); err != nil {
		log.Printf("customer %s welcome email: %v", customer.ID, err)
	}
	if err := s.customers.LinkSocialIdentity(ctx, customer.ID, profile.Provider, profile.Subject, profile.Email); err != nil {
		return nil, err
	}
	return s.signedIn(customer)
}

func (s *SocialAuthService) signedIn(c *models.Customer) (*SocialSignInResult, error) {
	token, err := utils.GenerateJWT(c.ID.String(), c.Email, "customer", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &SocialSignInResult{Status: "signed_in", Token: token, Role: "customer"}, nil
}

// retirePassword replaces a password with one nobody knows.
func (s *SocialAuthService) retirePassword(ctx context.Context, customerID string) error {
	hash, err := randomPasswordHash()
	if err != nil {
		return err
	}
	return s.customers.UpdatePassword(ctx, customerID, hash)
}

func randomPasswordHash() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return utils.HashPassword(hex.EncodeToString(b))
}

// ── Profile-completion token ─────────────────────────────────────────────

type socialSignupClaims struct {
	Use     string        `json:"use"`
	Profile SocialProfile `json:"profile"`
	jwt.RegisteredClaims
}

func (s *SocialAuthService) signupToken(p *SocialProfile) (string, error) {
	claims := socialSignupClaims{
		Use: socialSignupUse, Profile: *p,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(socialSignupTTL))},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.jwtSecret))
}

func (s *SocialAuthService) readSignupToken(raw string) (*SocialProfile, error) {
	claims := &socialSignupClaims{}
	_, err := jwt.ParseWithClaims(strings.TrimSpace(raw), claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(s.jwtSecret), nil
	})
	if err != nil || claims.Use != socialSignupUse || claims.Profile.Email == "" || claims.Profile.Subject == "" {
		return nil, ErrSocialSignupExpired
	}
	return &claims.Profile, nil
}

// ── Providers ────────────────────────────────────────────────────────────

func (s *SocialAuthService) verify(ctx context.Context, in SocialSignInInput) (*SocialProfile, error) {
	token := strings.TrimSpace(in.Token)
	if token == "" {
		return nil, ErrSocialInvalidToken
	}
	switch strings.ToLower(strings.TrimSpace(in.Provider)) {
	case ProviderGoogle:
		if len(s.googleClientIDs) == 0 {
			return nil, ErrSocialNotConfigured
		}
		if in.TokenType == "access_token" {
			return s.verifyGoogleAccessToken(ctx, token)
		}
		return s.verifyGoogleIDToken(ctx, token)
	case ProviderFacebook:
		if s.facebookAppID == "" || s.facebookAppSecret == "" {
			return nil, ErrSocialNotConfigured
		}
		return s.verifyFacebook(ctx, token)
	default:
		return nil, ErrInvalidInput
	}
}

// googleTokenInfo is Google's own reading of a token: Google checks the
// signature and expiry, and we check it was issued to one of our clients.
type googleTokenInfo struct {
	Aud           string `json:"aud"`
	Azp           string `json:"azp"`
	Sub           string `json:"sub"`
	Iss           string `json:"iss"`
	Email         string `json:"email"`
	EmailVerified string `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

func (s *SocialAuthService) verifyGoogleIDToken(ctx context.Context, token string) (*SocialProfile, error) {
	var info googleTokenInfo
	if err := s.getJSON(ctx, s.googleTokenInfoURL+"?"+url.Values{"id_token": {token}}.Encode(), "", &info); err != nil {
		return nil, err
	}
	if !slices.Contains(s.googleClientIDs, info.Aud) || info.Sub == "" ||
		(info.Iss != "accounts.google.com" && info.Iss != "https://accounts.google.com") {
		return nil, ErrSocialInvalidToken
	}
	return &SocialProfile{
		Provider: ProviderGoogle, Subject: info.Sub,
		Email: verifiedEmail(info.Email, info.EmailVerified), Name: info.Name, Picture: info.Picture,
	}, nil
}

func (s *SocialAuthService) verifyGoogleAccessToken(ctx context.Context, token string) (*SocialProfile, error) {
	var info googleTokenInfo
	if err := s.getJSON(ctx, s.googleTokenInfoURL+"?"+url.Values{"access_token": {token}}.Encode(), "", &info); err != nil {
		return nil, err
	}
	if !slices.Contains(s.googleClientIDs, info.Aud) && !slices.Contains(s.googleClientIDs, info.Azp) {
		return nil, ErrSocialInvalidToken
	}
	var user struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := s.getJSON(ctx, s.googleUserInfoURL, token, &user); err != nil {
		return nil, err
	}
	if user.Sub == "" || (info.Sub != "" && user.Sub != info.Sub) {
		return nil, ErrSocialInvalidToken
	}
	email := ""
	if user.EmailVerified {
		email = normalizeEmail(user.Email)
	}
	return &SocialProfile{Provider: ProviderGoogle, Subject: user.Sub, Email: email, Name: user.Name, Picture: user.Picture}, nil
}

func (s *SocialAuthService) verifyFacebook(ctx context.Context, token string) (*SocialProfile, error) {
	// Ask Facebook whether the token was issued to our app.
	var debug struct {
		Data struct {
			AppID   string `json:"app_id"`
			IsValid bool   `json:"is_valid"`
			UserID  string `json:"user_id"`
		} `json:"data"`
	}
	debugURL := s.facebookGraphURL + "/debug_token?" + url.Values{
		"input_token":  {token},
		"access_token": {s.facebookAppID + "|" + s.facebookAppSecret},
	}.Encode()
	if err := s.getJSON(ctx, debugURL, "", &debug); err != nil {
		return nil, err
	}
	if !debug.Data.IsValid || debug.Data.AppID != s.facebookAppID || debug.Data.UserID == "" {
		return nil, ErrSocialInvalidToken
	}

	mac := hmac.New(sha256.New, []byte(s.facebookAppSecret))
	mac.Write([]byte(token))
	var me struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Picture struct {
			Data struct {
				URL          string `json:"url"`
				IsSilhouette bool   `json:"is_silhouette"`
			} `json:"data"`
		} `json:"picture"`
	}
	meURL := s.facebookGraphURL + "/me?" + url.Values{
		"fields":          {"id,name,email,picture.type(large)"},
		"access_token":    {token},
		"appsecret_proof": {hex.EncodeToString(mac.Sum(nil))},
	}.Encode()
	if err := s.getJSON(ctx, meURL, "", &me); err != nil {
		return nil, err
	}
	if me.ID == "" || me.ID != debug.Data.UserID {
		return nil, ErrSocialInvalidToken
	}
	picture := me.Picture.Data.URL
	if me.Picture.Data.IsSilhouette {
		picture = ""
	}
	// Facebook only returns an email it has confirmed.
	return &SocialProfile{Provider: ProviderFacebook, Subject: me.ID, Email: normalizeEmail(me.Email), Name: me.Name, Picture: picture}, nil
}

func verifiedEmail(email, verified string) string {
	if verified != "true" {
		return ""
	}
	return normalizeEmail(email)
}

// getJSON fetches a provider endpoint. A 4xx means the provider rejected the
// token; anything else unexpected is a failure on our side or theirs.
func (s *SocialAuthService) getJSON(ctx context.Context, endpoint, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("sign-in provider: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return ErrSocialInvalidToken
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sign-in provider: %s", resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("sign-in provider: %w", err)
	}
	return nil
}
