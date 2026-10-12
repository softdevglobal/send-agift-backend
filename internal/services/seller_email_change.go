package services

import (
	"context"
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

// ErrFundingLocked is a seller trying to buy points or raise a product reward
// in the days after their email changed.
var ErrFundingLocked = errors.New("buying points and raising rewards are on hold for a few days after an email change")

// sellerUndoPath is the page in the seller app that posts the undo token.
const sellerUndoPath = "/seller/email/undo"

// UseEmailChanges turns on changing the seller's login email.
func (s *SellerService) UseEmailChanges(r *repository.SellerEmailChangeRepository) {
	s.emailChanges = r
}

// RequestEmailChange checks the password and mails a code to the new address.
// An address that is taken, reserved or used by another kind of account looks
// the same as a success.
func (s *SellerService) RequestEmailChange(ctx context.Context, sellerID, newEmail, currentPassword, ip string) error {
	id, err := uuid.Parse(sellerID)
	if err != nil {
		return ErrSellerNotFound
	}
	seller, err := s.sellers.GetByID(ctx, sellerID)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return ErrSellerNotFound
		}
		return err
	}
	if !utils.CheckPassword(currentPassword, seller.PasswordHash) {
		return ErrWrongPassword
	}
	email := normalizeEmail(newEmail)
	if email == "" || !strings.Contains(email, "@") || strings.EqualFold(email, seller.Email) {
		return ErrInvalidInput
	}
	now := time.Now().UTC()
	n, err := s.emailChanges.CountSince(ctx, id, now.Add(-30*24*time.Hour))
	if err != nil {
		return err
	}
	if n >= emailChangeMaxPer30d {
		return ErrEmailChangeLimit
	}
	if pending, err := s.emailChanges.Get(ctx, id); err == nil && emailCodeCoolingDown(&pending.SentAt, now) {
		return ErrEmailChangeWait
	}

	if used, err := s.sellers.EmailInUseAnywhere(ctx, email); err != nil {
		return err
	} else if used {
		return silentRejectCode()
	}
	if reserved, err := s.sellers.EmailReserved(ctx, email, id); err != nil {
		return err
	} else if reserved {
		return silentRejectCode()
	}

	code, err := newEmailCode()
	if err != nil {
		return err
	}
	hash, err := utils.HashPassword(code)
	if err != nil {
		return err
	}
	if err := s.emailChanges.Save(ctx, id, email, hash, ip, now.Add(emailChangeCodeTTL)); err != nil {
		return err
	}
	return s.email.SendEmailChangeCode(ctx, email, code, emailChangeCodeTTL)
}

// ConfirmEmailChange checks the code and swaps the email.
func (s *SellerService) ConfirmEmailChange(ctx context.Context, sellerID, code, ip string) (*EmailChangeResult, error) {
	id, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrSellerNotFound
	}
	req, err := s.emailChanges.Get(ctx, id)
	if errors.Is(err, repository.ErrEmailChangeNotFound) {
		return nil, ErrPasswordCodeExpired
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := passwordCodeBlock(req.CodeHash, req.ExpiresAt, req.Attempts, now); err != nil {
		return nil, err
	}
	code = strings.TrimSpace(code)
	if len(code) != emailCodeLength || !utils.CheckPassword(code, req.CodeHash) {
		n, err := s.emailChanges.AddAttempt(ctx, id)
		if err != nil {
			return nil, err
		}
		if n >= emailCodeMaxAttempts {
			return nil, ErrPasswordCodeLocked
		}
		return nil, ErrPasswordCodeWrong
	}

	seller, err := s.sellers.GetByID(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	prior, err := s.emailChanges.CountSince(ctx, id, now.Add(-30*24*time.Hour))
	if err != nil {
		return nil, err
	}
	flagged, reason := suspiciousEmailChange(seller.CreatedAt, prior, now)

	undo, undoHash, err := newUndoToken()
	if err != nil {
		return nil, err
	}
	lockUntil := now.Add(emailChangeLockWindow)
	old, err := s.emailChanges.Apply(ctx, repository.SellerApplyInput{
		SellerID: id, NewEmail: req.NewEmail, IP: ip, UndoHash: undoHash,
		UndoUntil: now.Add(emailChangeUndoWindow), LockUntil: lockUntil,
		Flagged: flagged, FlagReason: reason,
	})
	if err != nil {
		if errors.Is(err, repository.ErrSellerDuplicate) {
			return nil, ErrEmailChangeTaken
		}
		if errors.Is(err, repository.ErrSellerNotFound) {
			return nil, ErrSellerNotFound
		}
		return nil, err
	}

	if err := s.email.SendEmailChangedAlert(ctx, old, req.NewEmail, undo, sellerUndoPath); err != nil {
		log.Printf("seller email change alert for %s: %v", id, err)
	}
	token, err := utils.GenerateJWT(sellerID, req.NewEmail, "seller", s.jwtSecret, s.jwtExpiry)
	if err != nil {
		return nil, err
	}
	return &EmailChangeResult{Email: req.NewEmail, Token: token, RedemptionLocked: lockUntil}, nil
}

// RevertEmailChange is the "this wasn't me" link from the old inbox. It also
// replaces the password with one nobody knows, so the owner signs in again
// through the forgotten-password code sent to the restored address.
func (s *SellerService) RevertEmailChange(ctx context.Context, rawToken string) error {
	sum := sha256.Sum256([]byte(strings.TrimSpace(rawToken)))
	locked, err := randomPasswordHash()
	if err != nil {
		return err
	}
	_, restored, err := s.emailChanges.Revert(ctx, hex.EncodeToString(sum[:]), locked,
		time.Now().UTC().Add(emailChangeLockWindow))
	if errors.Is(err, repository.ErrUndoNotFound) {
		return ErrUndoInvalid
	}
	if err != nil {
		return err
	}
	if err := s.email.SendEmailChangeReverted(ctx, restored, "/seller/forgot-password"); err != nil {
		log.Printf("seller email change revert mail: %v", err)
	}
	return nil
}

// ensureFundingOpen is shared by the services that spend or promise points.
func ensureFundingOpen(ctx context.Context, sellers *repository.SellerRepository, sellerID uuid.UUID) error {
	until, err := sellers.FundingLockedUntil(ctx, sellerID)
	if err != nil {
		return err
	}
	if until != nil && until.After(time.Now()) {
		return ErrFundingLocked
	}
	return nil
}

// silentRejectCode spends the work of a real send so the answer's timing
// gives nothing away.
func silentRejectCode() error {
	code, err := newEmailCode()
	if err != nil {
		return err
	}
	_, err = utils.HashPassword(code)
	return err
}
