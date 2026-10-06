package services

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

const (
	emailCodeLength      = 6
	emailCodeTTL         = 15 * time.Minute
	emailCodeMaxAttempts = 5
	emailCodeCooldown    = time.Minute
)

var (
	// ErrSellerEmailUnconfirmed is a right password for a seller who has not
	// entered the emailed code yet.
	ErrSellerEmailUnconfirmed = errors.New("confirm your email before signing in")
	// ErrSellerEmailCodeWrong is a code that does not match, or an email with
	// nothing waiting. The two look the same so the endpoint cannot be used
	// to discover accounts.
	ErrSellerEmailCodeWrong = errors.New("that code is not right")
	// ErrSellerEmailCodeExpired means the code timed out or was never sent.
	ErrSellerEmailCodeExpired = errors.New("that code has expired. Send a new one")
	// ErrSellerEmailCodeLocked is five wrong tries. A new code is required.
	ErrSellerEmailCodeLocked = errors.New("too many tries. Send a new code")
	// ErrSellerEmailCodeWait is a resend inside the cooldown.
	ErrSellerEmailCodeWait = errors.New("wait a minute before asking for another code")
	// ErrSellerEmailAlreadyVerified is a confirm or resend for an email that
	// is already confirmed.
	ErrSellerEmailAlreadyVerified = errors.New("this email is already confirmed")
)

// SendEmailsWith turns on the confirmation email for new sellers.
func (s *SellerService) SendEmailsWith(email *EmailService) { s.email = email }

// UsePasswordResets turns on emailed codes for a forgotten or profile password.
func (s *SellerService) UsePasswordResets(codes *repository.PasswordResetRepository) {
	s.resets = codes
}

func newEmailCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", emailCodeLength, n.Int64()), nil
}

// emailCodeBlock is why a stored code cannot be checked, before the digits
// are compared. nil means the code can be checked.
func emailCodeBlock(verified bool, hash *string, expires *time.Time, attempts int, now time.Time) error {
	if verified {
		return ErrSellerEmailAlreadyVerified
	}
	if hash == nil || *hash == "" || expires == nil || !expires.After(now) {
		return ErrSellerEmailCodeExpired
	}
	if attempts >= emailCodeMaxAttempts {
		return ErrSellerEmailCodeLocked
	}
	return nil
}

func emailCodeCoolingDown(sentAt *time.Time, now time.Time) bool {
	return sentAt != nil && now.Sub(*sentAt) < emailCodeCooldown
}

func (s *SellerService) issueEmailCode(ctx context.Context, seller *models.Seller) error {
	code, err := newEmailCode()
	if err != nil {
		return err
	}
	hash, err := utils.HashPassword(code)
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(emailCodeTTL)
	if err := s.sellers.SaveEmailCode(ctx, seller.ID, hash, expires); err != nil {
		log.Printf("seller email code for %s: %v", seller.ID, err)
		return err
	}
	name := strings.TrimSpace(derefOr(seller.ContactName, seller.LegalName))
	if err := s.email.SendSellerEmailCode(ctx, seller.ID, seller.Email, name, code, emailCodeTTL); err != nil {
		// The account and the code are already stored. The seller can ask
		// for another code from the confirm screen.
		log.Printf("seller email code for %s: %v", seller.ID, err)
	}
	return nil
}

// VerifyEmail checks the code emailed at registration and signs the seller in.
func (s *SellerService) VerifyEmail(ctx context.Context, email, code string) (*SellerLoginResult, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	code = strings.TrimSpace(code)
	if email == "" || len(code) != emailCodeLength {
		return nil, ErrSellerEmailCodeWrong
	}
	seller, err := s.sellers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerEmailCodeWrong
		}
		return nil, err
	}
	if err := emailCodeBlock(seller.EmailVerifiedAt != nil, seller.EmailCodeHash, seller.EmailCodeExpiresAt, seller.EmailCodeAttempts, time.Now().UTC()); err != nil {
		return nil, err
	}
	if !utils.CheckPassword(code, *seller.EmailCodeHash) {
		attempts, addErr := s.sellers.AddEmailCodeAttempt(ctx, seller.ID)
		if addErr != nil {
			return nil, addErr
		}
		if attempts >= emailCodeMaxAttempts {
			return nil, ErrSellerEmailCodeLocked
		}
		return nil, ErrSellerEmailCodeWrong
	}
	if err := s.sellers.ConfirmSellerEmail(ctx, seller.ID); err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerEmailAlreadyVerified
		}
		return nil, err
	}
	now := time.Now().UTC()
	seller.EmailVerifiedAt = &now
	token, err := utils.GenerateJWT(seller.ID.String(), seller.Email, "seller", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &SellerLoginResult{Token: token}, nil
}

// ResendEmailCode emails a fresh code. An unknown email returns nil, so the
// endpoint cannot be used to discover accounts.
func (s *SellerService) ResendEmailCode(ctx context.Context, email string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil
	}
	seller, err := s.sellers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil
		}
		return err
	}
	if seller.EmailVerifiedAt != nil {
		return ErrSellerEmailAlreadyVerified
	}
	if emailCodeCoolingDown(seller.EmailCodeSentAt, time.Now().UTC()) {
		return ErrSellerEmailCodeWait
	}
	return s.issueEmailCode(ctx, seller)
}
