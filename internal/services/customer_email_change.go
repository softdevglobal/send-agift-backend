package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
	"myapp/internal/utils"
)

const (
	emailChangeCodeTTL    = 15 * time.Minute   // How long the emailed verification code stays valid
	emailChangeUndoWindow = 7 * 24 * time.Hour // How long the old inbox can use the "this wasn't me" link
	emailChangeLockWindow = 3 * 24 * time.Hour // How long prizes/points are frozen after a change
	emailChangeMaxPer30d  = 2                  // Max email changes allowed in a rolling 30 days
)

var (
	ErrEmailChangeLimit = errors.New("you changed your email too often. Try again later or contact support")
	ErrEmailChangeWait  = errors.New("wait a minute before asking for another code")
	ErrEmailChangeTaken = errors.New("that email can't be used") // Deliberately vague to avoid leaking which emails exist
	ErrUndoInvalid      = errors.New("this undo link is not valid or has expired")
	ErrRedemptionLocked = errors.New("points and prizes are on hold for a few days after an email change")
	ErrEmailReserved    = errors.New("this email was just changed on another account. Use the undo link in that account's email, or contact support")
)

// customerUndoPath is the page in the customer app that posts the undo token.
const customerUndoPath = "/account/email/undo"

// UseEmailChanges injects the email-change repository (setter-style dependency injection).
func (s *CustomerService) UseEmailChanges(r *repository.EmailChangeRepository) { s.emailChanges = r }

// RequestEmailChange checks the password and mails a code to the new address.
// An address that is taken, reserved or staff looks the same as a success.
func (s *CustomerService) RequestEmailChange(ctx context.Context, customerID, newEmail, currentPassword, ip string) error {
	// Parse the customer ID string into a UUID; a bad ID is treated as "not found"
	id, err := uuid.Parse(customerID)
	if err != nil {
		return ErrCustomerNotFound
	}
	// Load the customer record
	customer, err := s.customers.GetByID(ctx, customerID)
	if err != nil {
		// Translate repository-level "not found" into the service-level error
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return ErrCustomerNotFound
		}
		return err
	}
	// Require the current password before allowing any email change
	if !utils.CheckPassword(currentPassword, customer.PasswordHash) {
		return ErrWrongPassword
	}
	// Normalize the new email (trim/lowercase) for consistent comparison
	email := normalizeEmail(newEmail)
	// Reject empty, malformed, or unchanged emails
	if email == "" || !strings.Contains(email, "@") || strings.EqualFold(email, customer.Email) {
		return ErrInvalidInput
	}
	now := time.Now().UTC()
	// Count how many changes this customer made in the last 30 days
	n, err := s.emailChanges.CountSince(ctx, id, now.Add(-30*24*time.Hour))
	if err != nil {
		return err
	}
	// Enforce the monthly change limit
	if n >= emailChangeMaxPer30d {
		return ErrEmailChangeLimit
	}
	// Rate-limit code requests: block if a code was sent very recently
	if pending, err := s.emailChanges.Get(ctx, id); err == nil && emailCodeCoolingDown(&pending.SentAt, now) {
		return ErrEmailChangeWait
	}

	// A taken, staff or reserved address looks the same as a success, and
	// costs the same time, so nobody can use this to list accounts.
	if used, err := s.emailChanges.EmailInUse(ctx, email); err != nil {
		return err
	} else if used {
		return s.silentReject()
	}
	if used, err := s.customers.EmailUsedByStaffOrSeller(ctx, email); err != nil {
		return err
	} else if used {
		return s.silentReject()
	}
	if reserved, err := s.customers.EmailReserved(ctx, email, id); err != nil {
		return err
	} else if reserved {
		return s.silentReject()
	}

	// Generate a fresh random verification code
	code, err := newEmailCode()
	if err != nil {
		return err
	}
	// Hash the code so the plaintext is never stored
	hash, err := utils.HashPassword(code)
	if err != nil {
		return err
	}
	// Persist the pending request (new email, code hash, requester IP, expiry)
	if err := s.emailChanges.Save(ctx, id, email, hash, ip, now.Add(emailChangeCodeTTL)); err != nil {
		return err
	}
	// Email the plaintext code to the NEW address to prove ownership
	return s.email.SendEmailChangeCode(ctx, email, code, emailChangeCodeTTL)
}

// silentReject spends the work of a real send so the answer's timing gives
// nothing away.
func (s *CustomerService) silentReject() error { return silentRejectCode() }

// EmailChangeResult carries the new session, since the old tokens stop working.
type EmailChangeResult struct {
	Email            string    `json:"email"`                   // The newly confirmed email
	Token            string    `json:"token"`                   // Fresh JWT bound to the new email
	RedemptionLocked time.Time `json:"redemption_locked_until"` // When prize/point redemption unlocks
}

// ConfirmEmailChange checks the code and swaps the email.
func (s *CustomerService) ConfirmEmailChange(ctx context.Context, customerID, code, ip string) (*EmailChangeResult, error) {
	// Validate the customer ID format
	id, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrCustomerNotFound
	}
	// Fetch the pending change request
	req, err := s.emailChanges.Get(ctx, id)
	// No pending request is reported as an expired code
	if errors.Is(err, repository.ErrEmailChangeNotFound) {
		return nil, ErrPasswordCodeExpired
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	// Reject if the code is expired or too many attempts were already made
	if err := passwordCodeBlock(req.CodeHash, req.ExpiresAt, req.Attempts, now); err != nil {
		return nil, err
	}
	// Check the submitted code's length and compare it against the stored hash
	if len(strings.TrimSpace(code)) != emailCodeLength || !utils.CheckPassword(strings.TrimSpace(code), req.CodeHash) {
		// Record the failed attempt
		n, err := s.emailChanges.AddAttempt(ctx, id)
		if err != nil {
			return nil, err
		}
		// Lock the code once the max attempts are reached
		if n >= emailCodeMaxAttempts {
			return nil, ErrPasswordCodeLocked
		}
		return nil, ErrPasswordCodeWrong
	}

	// Code is correct: reload the customer for fraud checks
	customer, err := s.customers.GetByID(ctx, customerID)
	if err != nil {
		return nil, err
	}
	// Count recent changes to feed the suspicion heuristic
	prior, err := s.emailChanges.CountSince(ctx, id, now.Add(-30*24*time.Hour))
	if err != nil {
		return nil, err
	}
	// Decide whether this change looks risky (and why)
	flagged, reason := suspiciousEmailChange(customer.CreatedAt, prior, now)

	// Create a one-time undo token (raw for the email, hash for the DB)
	undo, undoHash, err := newUndoToken()
	if err != nil {
		return nil, err
	}
	// Compute when the redemption freeze ends
	lockUntil := now.Add(emailChangeLockWindow)
	// Atomically apply the change; returns the OLD email for notification
	old, err := s.emailChanges.Apply(ctx, repository.ApplyInput{
		CustomerID: id, NewEmail: req.NewEmail, IP: ip, UndoHash: undoHash,
		UndoUntil: now.Add(emailChangeUndoWindow), LockUntil: lockUntil,
		Flagged: flagged, FlagReason: reason,
	})
	if err != nil {
		// Someone grabbed the email between request and confirm (race condition)
		if errors.Is(err, repository.ErrCustomerDuplicate) {
			return nil, ErrEmailChangeTaken
		}
		return nil, err
	}

	// Alert the OLD address with an undo link; failure is logged but doesn't abort the change
	if err := s.email.SendEmailChangedAlert(ctx, old, req.NewEmail, undo, customerUndoPath); err != nil {
		log.Printf("email change alert for %s: %v", id, err)
	}
	// Issue a new JWT since the old tokens carried the old email
	token, err := utils.GenerateJWT(customerID, req.NewEmail, "customer", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &EmailChangeResult{Email: req.NewEmail, Token: token, RedemptionLocked: lockUntil}, nil
}

// RevertEmailChange is the "this wasn't me" link from the old inbox.
func (s *CustomerService) RevertEmailChange(ctx context.Context, rawToken string) error {
	// Hash the raw token with SHA-256 to match what's stored in the DB
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawToken)))
	// Revert the change; also restarts the redemption lock for another lock window
	_, restored, err := s.emailChanges.Revert(ctx, hex.EncodeToString(sum[:]),
		time.Now().UTC().Add(emailChangeLockWindow))
	// Unknown/expired token maps to a user-friendly error
	if errors.Is(err, repository.ErrUndoNotFound) {
		return ErrUndoInvalid
	}
	if err != nil {
		return err
	}
	// Notify the restored (original) address; failure is only logged
	if err := s.email.SendEmailChangeReverted(ctx, restored, "/forgot-password"); err != nil {
		log.Printf("email change revert mail: %v", err)
	}
	return nil
}

// EnsureCanRedeem blocks prize claims and gift-point sends after a change.
func (s *CustomerService) EnsureCanRedeem(ctx context.Context, customerID uuid.UUID) error {
	return ensureCanRedeem(ctx, s.customers, customerID)
}

// ensureCanRedeem is shared by every service that pays out or sends points.
func ensureCanRedeem(ctx context.Context, customers *repository.CustomerRepository, customerID uuid.UUID) error {
	until, err := customers.RedemptionLockedUntil(ctx, customerID)
	if err != nil {
		return err
	}
	if until != nil && until.After(time.Now()) {
		return ErrRedemptionLocked
	}
	return nil
}

// ensureEmailFree stops a sign-up from taking an address its previous owner
// can still get back.
func ensureEmailFree(ctx context.Context, customers *repository.CustomerRepository, email string) error {
	reserved, err := customers.EmailReserved(ctx, email, uuid.Nil)
	if err != nil {
		return err
	}
	if reserved {
		return ErrEmailReserved
	}
	return nil
}

// suspiciousEmailChange flags risky changes (possible account takeover) for review.
func suspiciousEmailChange(createdAt time.Time, priorChanges int, now time.Time) (bool, string) {
	switch {
	case priorChanges >= 1: // Already changed email once in the last 30 days
		return true, "second change within 30 days"
	case now.Sub(createdAt) < 7*24*time.Hour: // Brand-new accounts are higher risk
		return true, "account younger than 7 days"
	}
	return false, ""
}

// newUndoToken creates a random token to email out, plus its SHA-256 hash to store.
func newUndoToken() (token, hash string, err error) {
	b := make([]byte, 32) // 32 random bytes = 256 bits of entropy
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b)       // Hex-encode for safe use in a URL
	sum := sha256.Sum256([]byte(token)) // Hash it so the DB never holds the raw token
	return token, hex.EncodeToString(sum[:]), nil
}
