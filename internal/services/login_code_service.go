package services

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

const (
	// two supported ways of a receiving a code: email or sms
	ChannelEmail = "email"
	ChannelSMS   = "sms"

	// two purposes for the code: login or verify phone
	loginPurpose       = "login"
	verifyPhonePurpose = "verify_phone"

	
	loginCodeTTL        = 5 * time.Minute  // Login otp is valid for 5 minutes
	loginCodeCooldown   = time.Minute // user must wait for 1 minute before requesting a new code
	loginCodeDailySends = 5 // maximum number of codes that can be sent by a user per day
	loginCodeDailyWrong = 10 // user can make 10 wrong attempts per day
	loginCodeWindow     = 24 * time.Hour // user can make 10 wrong attempts per day within a 24 hour window

	codeSignupTTL = 20 * time.Minute // signup token is valid for 20 minutes
	codeSignupUse = "code_signup" // signup token is used to sign up a new user
)

// customer error messages
var (
	ErrLoginChannel      = errors.New("choose phone or email")
	ErrLoginCodeWrong    = errors.New("that code is not right")
	ErrLoginCodeExpired  = errors.New("that code has expired. Send a new one")
	ErrLoginCodeLocked   = errors.New("too many wrong codes. Try again tomorrow or contact support")
	ErrLoginCodeWait     = errors.New("wait a minute before asking for another code")
	ErrLoginCodeTooMany  = errors.New("too many codes sent here today. Try again tomorrow")
	ErrPhoneTaken        = errors.New("this phone number is already verified on another account")
	ErrCodeSignupExpired = errors.New("your sign-up session expired. Ask for a new code")
)

// LoginCodeService signs customers in with a 6-digit code sent by SMS or
// email, and verifies a customer's phone the same way.
// LoginCodeService handles 
// 1. Login using an OTP
// 2. Email verification
// 3. Phone verification
// 4. Sending OTPs
// 5. Checking OTPs
// 6. Generating Jwt tokens 
type LoginCodeService struct {
	codes     *repository.LoginCodeRepository // repository for storing and retrieving OTP records
	customers *repository.CustomerRepository // repository for storing and retrieving customer records
	email     *EmailService // email service for sending emails
	sms       *SMSService // sms service for sending sms
	defaultCC string // default country code for phone numbers
	jwtSecret string // secret key for generating jwt tokens
	jwtExpiry time.Duration // expiration time for jwt tokens
}

func NewLoginCodeService(
	codes *repository.LoginCodeRepository, // OTP database repository
	customers *repository.CustomerRepository, // customer database repository
	email *EmailService, // email service for sending emails
	sms *SMSService, // sms service for sending sms
	defaultCountryCode, jwtSecret string, // default country code for phone numbers and secret key for generating jwt tokens		
	jwtExpiry time.Duration,
) *LoginCodeService {
	return &LoginCodeService{
		codes: codes, customers: customers, email: email, sms: sms,
		defaultCC: defaultCountryCode, jwtSecret: jwtSecret, jwtExpiry: jwtExpiry,
	}
}

// CodeLoginResult is a signed-in customer, or someone new who proved they
// hold the destination and may sign up with it.
type CodeLoginResult struct {
	// Possible values:
	// "signed_in"
	// "needs_signup"
	Status      string `json:"status"`
	Token       string `json:"token,omitempty"`
	Role        string `json:"role,omitempty"`
	SignupToken string `json:"signup_token,omitempty"`
	Channel     string `json:"channel,omitempty"`
	Destination string `json:"destination,omitempty"` // Email address or phone number
}

// normalize converts a channel and raw input into a normalized string
func (s *LoginCodeService) normalize(channel, raw string) (string, error) {
	switch channel {
	case ChannelEmail:
		e := normalizeEmail(raw)
		if e == "" || !strings.Contains(e, "@") {
			return "", ErrInvalidInput
		}
		return e, nil
	case ChannelSMS:
		return NormalizePhone(raw, s.defaultCC)
	}
	return "", ErrLoginChannel
}

// RequestLoginCode sends a code whether or not an account exists, so the
// answer never tells anyone which numbers or emails are registered.
func (s *LoginCodeService) RequestLoginCode(ctx context.Context, channel, raw string) error {
	dest, err := s.normalize(channel, raw)
	if err != nil {
		return err
	}
	return s.issue(ctx, channel, dest, loginPurpose, nil) // generate and send a new OTP to the user
}

// VerifyLoginCode checks the code and signs the customer in, or hands back
// a short-lived sign-up token when no account uses the destination.
func (s *LoginCodeService) VerifyLoginCode(ctx context.Context, channel, raw, code string) (*CodeLoginResult, error) {
	dest, err := s.normalize(channel, raw)
	if err != nil {
		return nil, ErrLoginCodeWrong
	}
	if err := s.check(ctx, channel, dest, loginPurpose, code, nil); err != nil { // check if the code is valid
		return nil, err
	}

	customer, err := s.findCustomer(ctx, channel, dest)
	if errors.Is(err, repository.ErrCustomerNotFound) {
		signup, err := s.signupToken(channel, dest)
		if err != nil {
			return nil, err
		}
		return &CodeLoginResult{Status: "needs_signup", SignupToken: signup, Channel: channel, Destination: dest}, nil
	}
	if err != nil {
		return nil, err
	}
	if channel == ChannelEmail {
		_ = s.customers.MarkEmailVerified(ctx, customer.ID.String())
	}
	if channel == ChannelSMS {
		if err := s.customers.SetVerifiedPhone(ctx, customer.ID.String(), dest, dest); err != nil {
			if !errors.Is(err, repository.ErrCustomerDuplicate) {
				log.Printf("verify phone on login %s: %v", customer.ID, err)
			} else if id, err2 := s.customers.CustomerIDByVerifiedPhone(ctx, dest); err2 == nil {
				customer, err = s.customers.GetByID(ctx, id)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	token, err := utils.GenerateJWT(customer.ID.String(), customer.Email, "customer", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &CodeLoginResult{Status: "signed_in", Token: token, Role: "customer"}, nil
}

// RequestPhoneVerification texts a code to a number the signed-in customer
// wants on their account.
func (s *LoginCodeService) RequestPhoneVerification(ctx context.Context, customerID, raw string) error {
	dest, err := NormalizePhone(raw, s.defaultCC)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(customerID)
	if err != nil {
		return ErrCustomerNotFound
	}
	return s.issue(ctx, ChannelSMS, dest, verifyPhonePurpose, &id)
}

// ConfirmPhoneVerification checks the code and marks the number verified.
func (s *LoginCodeService) ConfirmPhoneVerification(ctx context.Context, customerID, raw, code string) error {
	dest, err := NormalizePhone(raw, s.defaultCC)
	if err != nil {
		return ErrLoginCodeWrong
	}
	id, err := uuid.Parse(customerID)
	if err != nil {
		return ErrCustomerNotFound
	}
	if err := s.check(ctx, ChannelSMS, dest, verifyPhonePurpose, code, &id); err != nil {
		return err
	}
	if err := s.customers.SetVerifiedPhone(ctx, customerID, strings.TrimSpace(raw), dest); err != nil {
		if errors.Is(err, repository.ErrCustomerDuplicate) {
			return ErrPhoneTaken
		}
		return err
	}
	return nil
}

func (s *LoginCodeService) issue(ctx context.Context, channel, dest, purpose string, customerID *uuid.UUID) error {
	now := time.Now().UTC()
	cur, err := s.codes.Get(ctx, channel, dest, purpose)
	switch {
	case err == nil:
		fresh := now.Sub(cur.WindowStart) >= loginCodeWindow
		if now.Sub(cur.SentAt) < loginCodeCooldown {
			return ErrLoginCodeWait
		}
		if !fresh && cur.Attempts >= loginCodeDailyWrong {
			return ErrLoginCodeLocked
		}
		if !fresh && cur.SendCount >= loginCodeDailySends {
			return ErrLoginCodeTooMany
		}
	case !errors.Is(err, repository.ErrLoginCodeNotFound):
		return err
	}

	code, err := newEmailCode()
	if err != nil {
		return err
	}
	hash, err := utils.HashPassword(code)
	if err != nil {
		return err
	}
	if err := s.codes.Save(ctx, channel, dest, purpose, customerID, hash, now.Add(loginCodeTTL)); err != nil {
		return err
	}
	// Printed on the API terminal so a local sign-in can continue when SMS
	// or email delivery does not arrive. Do not leave this on in production.
	log.Printf("OTP %s to %s: %s", channel, dest, code)
	if channel == ChannelSMS {
		return s.sms.SendLoginCode(ctx, dest, code, loginCodeTTL)
	}
	return s.email.SendLoginCode(ctx, dest, code, loginCodeTTL)
}

// check compares the code and spends it. With customerID set, the code must
// have been asked for by that customer.
func (s *LoginCodeService) check(ctx context.Context, channel, dest, purpose, code string, customerID *uuid.UUID) error {
	code = strings.TrimSpace(code)
	if len(code) != emailCodeLength {
		return ErrLoginCodeWrong
	}
	cur, err := s.codes.Get(ctx, channel, dest, purpose)
	if errors.Is(err, repository.ErrLoginCodeNotFound) {
		return ErrLoginCodeExpired
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if cur.Attempts >= loginCodeDailyWrong && now.Sub(cur.WindowStart) < loginCodeWindow {
		return ErrLoginCodeLocked
	}
	if cur.CodeHash == "" || !cur.ExpiresAt.After(now) {
		return ErrLoginCodeExpired
	}
	wrongOwner := customerID != nil && (cur.CustomerID == nil || *cur.CustomerID != *customerID)
	if wrongOwner || !utils.CheckPassword(code, cur.CodeHash) {
		n, err := s.codes.AddAttempt(ctx, channel, dest, purpose)
		if err != nil {
			return err
		}
		if n >= loginCodeDailyWrong {
			return ErrLoginCodeLocked
		}
		return ErrLoginCodeWrong
	}
	ok, err := s.codes.Consume(ctx, channel, dest, purpose, cur.CodeHash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrLoginCodeExpired
	}
	return nil
}

func (s *LoginCodeService) findCustomer(ctx context.Context, channel, dest string) (*models.Customer, error) {
	if channel == ChannelEmail {
		return s.customers.GetByEmail(ctx, dest)
	}
	return findCustomerByPhone(ctx, s.customers, s.defaultCC, dest)
}

// findCustomerByPhone matches a verified phone, a phone saved on the
// account, or a recipient phone whose email is that account.
func findCustomerByPhone(ctx context.Context, customers *repository.CustomerRepository, defaultCC, raw string) (*models.Customer, error) {
	dest, err := NormalizePhone(raw, defaultCC)
	if err != nil {
		return nil, err
	}
	id, err := customers.CustomerIDByVerifiedPhone(ctx, dest)
	if err == nil {
		return customers.GetByID(ctx, id)
	}
	if !errors.Is(err, repository.ErrCustomerNotFound) {
		return nil, err
	}
	matches, err := customers.AccountsByPhoneTail(ctx, dest)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var ids []string
	for _, match := range matches {
		norm, err := NormalizePhone(match.Phone, defaultCC)
		if err != nil || norm != dest {
			continue
		}
		if _, ok := seen[match.CustomerID]; ok {
			continue
		}
		seen[match.CustomerID] = struct{}{}
		ids = append(ids, match.CustomerID)
	}
	if len(ids) == 0 {
		return nil, repository.ErrCustomerNotFound
	}
	if len(ids) > 1 {
		return nil, ErrPhoneTaken
	}
	return customers.GetByID(ctx, ids[0])
}

// A sign-up token carries no role, so the auth middleware never accepts it
// as a session.
type codeSignupClaims struct {
	Use         string `json:"use"`
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
	jwt.RegisteredClaims
}

func (s *LoginCodeService) signupToken(channel, dest string) (string, error) {
	claims := codeSignupClaims{
		Use: codeSignupUse, Channel: channel, Destination: dest,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(codeSignupTTL))},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.jwtSecret))
}

// ReadSignupToken returns the channel and destination a sign-up token proves.
func (s *LoginCodeService) ReadSignupToken(raw string) (string, string, error) {
	claims := &codeSignupClaims{}
	_, err := jwt.ParseWithClaims(strings.TrimSpace(raw), claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrCodeSignupExpired
		}
		return []byte(s.jwtSecret), nil
	})
	if err != nil || claims.Use != codeSignupUse || claims.Destination == "" {
		return "", "", ErrCodeSignupExpired
	}
	return claims.Channel, claims.Destination, nil
}
