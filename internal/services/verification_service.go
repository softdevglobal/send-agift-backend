package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

var (
	// ErrEmailNotVerified is a seller signing in before confirming their email.
	ErrEmailNotVerified = errors.New("confirm your email before signing in")
	// ErrEmailAlreadyVerified is a code sent for an email that is already confirmed.
	ErrEmailAlreadyVerified = errors.New("email already confirmed")
	// ErrEmailCodeInvalid is a wrong code, or an email with no seller account.
	ErrEmailCodeInvalid = errors.New("that code isn't right")
	// ErrEmailCodeExpired is a code past its expiry; a new one must be sent.
	ErrEmailCodeExpired = errors.New("that code has expired")
	// ErrEmailCodeLocked is too many wrong codes; a new one must be sent.
	ErrEmailCodeLocked = errors.New("too many wrong codes")
	// ErrSellerNotApproved is a seller whose account an admin has not approved yet.
	ErrSellerNotApproved = errors.New("seller account is not approved yet")
	// ErrInvalidSellerReview is an admin decision that is not verified or
	// rejected, or a rejection with no reason.
	ErrInvalidSellerReview = errors.New("status must be verified or rejected, and a rejection needs a note")
	// ErrSellerEmailUnconfirmed is reviewing a seller still confirming their email.
	ErrSellerEmailUnconfirmed = errors.New("seller has not confirmed their email yet")
)

// EmailCodeCooldownError is a code requested again too soon.
type EmailCodeCooldownError struct{ RetryAfter time.Duration }

func (e *EmailCodeCooldownError) Error() string {
	return fmt.Sprintf("wait %d seconds before asking for another code", int(e.RetryAfter.Seconds())+1)
}

const (
	// emailCodeTTL is how long a seller's email code works.
	emailCodeTTL = 15 * time.Minute
	// emailCodeCooldown is how soon another code can be sent.
	emailCodeCooldown = 60 * time.Second
	// maxEmailCodeAttempts is how many wrong codes are allowed before a new
	// one must be sent.
	maxEmailCodeAttempts = 5
)

// SellerVerificationService confirms sellers' email addresses with a
// 6-digit code, and lets admins approve or reject seller accounts.
type SellerVerificationService struct {
	sellers   *repository.SellerRepository
	email     *EmailService
	jwtSecret string
	jwtExpiry time.Duration
}

func NewSellerVerificationService(
	sellers *repository.SellerRepository,
	email *EmailService,
	jwtSecret string,
	jwtExpiry time.Duration,
) *SellerVerificationService {
	return &SellerVerificationService{sellers: sellers, email: email, jwtSecret: jwtSecret, jwtExpiry: jwtExpiry}
}

// IssueCode makes a new email code for a seller and emails it.
func (s *SellerVerificationService) IssueCode(ctx context.Context, sellerID uuid.UUID, email, name string) error {
	code, err := newEmailCode()
	if err != nil {
		return err
	}
	if err := s.sellers.SetEmailCode(ctx, sellerID, hashEmailCode(sellerID, code), time.Now().Add(emailCodeTTL)); err != nil {
		return err
	}
	return s.email.SendSellerEmailCode(ctx, sellerID, email, name, code, emailCodeTTL)
}

// ResendCode emails a fresh code. An unknown email gets the same answer as a
// known one, so this cannot be used to find out who is a seller.
func (s *SellerVerificationService) ResendCode(ctx context.Context, email string) error {
	c, err := s.sellers.GetEmailCode(ctx, normalizeEmail(email))
	if errors.Is(err, repository.ErrSellerNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.EmailVerifiedAt != nil {
		return ErrEmailAlreadyVerified
	}
	if c.SentAt != nil {
		if wait := emailCodeCooldown - time.Since(*c.SentAt); wait > 0 {
			return &EmailCodeCooldownError{RetryAfter: wait}
		}
	}
	return s.IssueCode(ctx, c.SellerID, c.Email, derefOr(c.TradingName, c.LegalName))
}

// VerifyEmailResult signs the seller in once their email is confirmed.
type VerifyEmailResult struct {
	Token              string `json:"token"`
	Role               string `json:"role"`
	VerificationStatus string `json:"verification_status"`
}

// VerifyEmail checks a seller's code. A right code confirms the email, puts
// the account in the admin review queue, sends the welcome email and signs
// the seller in.
func (s *SellerVerificationService) VerifyEmail(ctx context.Context, email, code string) (*VerifyEmailResult, error) {
	code = strings.TrimSpace(code)
	c, err := s.sellers.GetEmailCode(ctx, normalizeEmail(email))
	if errors.Is(err, repository.ErrSellerNotFound) {
		return nil, ErrEmailCodeInvalid
	}
	if err != nil {
		return nil, err
	}
	if c.EmailVerifiedAt != nil {
		return nil, ErrEmailAlreadyVerified
	}
	if c.CodeHash == nil || c.ExpiresAt == nil || time.Now().After(*c.ExpiresAt) {
		return nil, ErrEmailCodeExpired
	}
	if c.Attempts >= maxEmailCodeAttempts {
		return nil, ErrEmailCodeLocked
	}
	if subtle.ConstantTimeCompare([]byte(hashEmailCode(c.SellerID, code)), []byte(*c.CodeHash)) != 1 {
		if err := s.sellers.CountEmailCodeAttempt(ctx, c.SellerID); err != nil {
			return nil, err
		}
		if c.Attempts+1 >= maxEmailCodeAttempts {
			return nil, ErrEmailCodeLocked
		}
		return nil, ErrEmailCodeInvalid
	}

	changed, err := s.sellers.MarkEmailVerified(ctx, c.SellerID)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, ErrEmailAlreadyVerified
	}
	if err := s.email.SendSellerPendingReview(ctx, c.SellerID, c.Email, derefOr(c.TradingName, c.LegalName)); err != nil {
		log.Printf("seller %s welcome email: %v", c.SellerID, err)
	}

	token, err := utils.GenerateJWT(c.SellerID.String(), c.Email, "seller", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &VerifyEmailResult{Token: token, Role: "seller", VerificationStatus: "pending"}, nil
}

// VerificationStatus is a seller's review status, for gating what they can do.
func (s *SellerVerificationService) VerificationStatus(ctx context.Context, sellerID string) (string, error) {
	status, err := s.sellers.VerificationStatus(ctx, sellerID)
	if errors.Is(err, repository.ErrSellerNotFound) {
		return "", ErrSellerNotFound
	}
	return status, err
}

// AdminSellerList is one page of sellers for review, with counts per status.
type AdminSellerList struct {
	Sellers []models.AdminSellerSummary `json:"sellers"`
	Total   int                         `json:"total"`
	Counts  map[string]int              `json:"counts"`
}

func (s *SellerVerificationService) AdminList(ctx context.Context, status, query string, limit, offset int) (*AdminSellerList, error) {
	switch status {
	case "", "unverified", "pending", "verified", "rejected":
	default:
		return nil, ErrInvalidInput
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	sellers, total, err := s.sellers.AdminList(ctx, repository.AdminSellerFilter{
		VerificationStatus: status, Query: query, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	counts, err := s.sellers.CountByVerification(ctx)
	if err != nil {
		return nil, err
	}
	return &AdminSellerList{Sellers: sellers, Total: total, Counts: counts}, nil
}

// AdminGet is a seller's full profile. Addresses and shops. For review.
func (s *SellerVerificationService) AdminGet(ctx context.Context, sellerID string) (*models.SellerDetails, error) {
	if _, err := uuid.Parse(sellerID); err != nil {
		return nil, ErrSellerNotFound
	}
	seller, err := s.sellers.GetByID(ctx, sellerID)
	if errors.Is(err, repository.ErrSellerNotFound) {
		return nil, ErrSellerNotFound
	}
	if err != nil {
		return nil, err
	}
	addresses, err := s.sellers.ListAddresses(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	shops, err := s.sellers.ListShops(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	return &models.SellerDetails{Seller: *seller, Addresses: addresses, Shops: shops}, nil
}

// SellerReviewInput is an admin's decision on a seller account.
type SellerReviewInput struct {
	Status string  `json:"status"` // verified | rejected
	Note   *string `json:"note"`
}

// AdminReview approves or rejects a seller and emails them the outcome.
func (s *SellerVerificationService) AdminReview(ctx context.Context, adminID, sellerID string, in SellerReviewInput) (*models.Seller, error) {
	status := strings.ToLower(strings.TrimSpace(in.Status))
	var note *string
	if in.Note != nil && strings.TrimSpace(*in.Note) != "" {
		n := strings.TrimSpace(*in.Note)
		if len(n) > 2000 {
			n = n[:2000]
		}
		note = &n
	}
	if status != "verified" && status != "rejected" {
		return nil, ErrInvalidSellerReview
	}
	if status == "rejected" && note == nil {
		return nil, ErrInvalidSellerReview
	}
	if _, err := uuid.Parse(sellerID); err != nil {
		return nil, ErrSellerNotFound
	}

	seller, err := s.sellers.GetByID(ctx, sellerID)
	if errors.Is(err, repository.ErrSellerNotFound) || (err == nil && seller.Status != "active") {
		return nil, ErrSellerNotFound
	}
	if err != nil {
		return nil, err
	}
	if seller.EmailVerifiedAt == nil {
		return nil, ErrSellerEmailUnconfirmed
	}

	var reviewer *uuid.UUID
	if id, err := uuid.Parse(adminID); err == nil {
		reviewer = &id
	}
	if err := s.sellers.SetVerification(ctx, sellerID, status, note, reviewer); err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		return nil, err
	}
	seller, err = s.sellers.GetByID(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	if err := s.email.SendSellerReviewed(ctx, seller); err != nil {
		log.Printf("seller %s review email: %v", seller.ID, err)
	}
	return seller, nil
}

// newEmailCode is a uniformly random 6-digit code.
func newEmailCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// hashEmailCode keys the code to the seller, so a stored hash is useless
// for any other account.
func hashEmailCode(sellerID uuid.UUID, code string) string {
	sum := sha256.Sum256([]byte(sellerID.String() + ":" + code))
	return hex.EncodeToString(sum[:])
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
