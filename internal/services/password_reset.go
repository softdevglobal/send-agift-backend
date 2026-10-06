package services

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
	"myapp/internal/utils"
)

const (
	passwordPurposeForgot   = "forgot"
	passwordPurposeProfile  = "profile"
	passwordSubjectCustomer = "customer"
	passwordSubjectSeller   = "seller"
)

var (
	// ErrPasswordCodeWrong is a code that does not match. An unknown email on
	// the signed-out reset looks the same, so the endpoint cannot list accounts.
	ErrPasswordCodeWrong = errors.New("that code is not right")
	// ErrPasswordCodeExpired means the code timed out or was never sent.
	ErrPasswordCodeExpired = errors.New("that code has expired. Send a new one")
	// ErrPasswordCodeLocked is five wrong tries.
	ErrPasswordCodeLocked = errors.New("too many tries. Send a new code")
	// ErrPasswordCodeWait is a resend inside the one-minute cooldown.
	ErrPasswordCodeWait = errors.New("wait a minute before asking for another code")
	// ErrPasswordUnchanged is a new password equal to the current one.
	ErrPasswordUnchanged = errors.New("choose a different password")
)

// newPasswordCode is the 6-digit generator. Tests replace it.
var newPasswordCode = newEmailCode

func passwordCodeBlock(hash string, expires time.Time, attempts int, now time.Time) error {
	if hash == "" || !expires.After(now) {
		return ErrPasswordCodeExpired
	}
	if attempts >= emailCodeMaxAttempts {
		return ErrPasswordCodeLocked
	}
	return nil
}

func acceptableNewPassword(password, currentHash string, banned ...string) error {
	if len(password) < 8 {
		return ErrInvalidInput
	}
	for _, bad := range banned {
		if password == bad {
			return ErrInvalidInput
		}
	}
	if currentHash != "" && utils.CheckPassword(password, currentHash) {
		return ErrPasswordUnchanged
	}
	return nil
}

type passwordAccount struct {
	Type         string
	ID           uuid.UUID
	Email        string
	Name         string
	PasswordHash string
}

// passwordCodeStore is the live-code table. The repository implements it;
// tests use a memory stand-in.
type passwordCodeStore interface {
	Save(ctx context.Context, subjectType string, subjectID uuid.UUID, purpose, hash string, expiresAt time.Time) error
	Get(ctx context.Context, subjectType string, subjectID uuid.UUID, purpose string) (*repository.PasswordResetCode, error)
	AddAttempt(ctx context.Context, subjectType string, subjectID uuid.UUID, purpose string) (int, error)
	Delete(ctx context.Context, subjectType string, subjectID uuid.UUID, purpose string) error
}

func codeStore(r *repository.PasswordResetRepository) passwordCodeStore {
	if r == nil {
		return nil
	}
	return r
}

func sendPasswordCode(ctx context.Context, codes passwordCodeStore, mail *EmailService, account passwordAccount, purpose string) error {
	if codes == nil {
		return errors.New("password reset is not configured")
	}
	existing, err := codes.Get(ctx, account.Type, account.ID, purpose)
	if err != nil && !errors.Is(err, repository.ErrPasswordResetNotFound) {
		return err
	}
	if existing != nil && emailCodeCoolingDown(&existing.SentAt, time.Now().UTC()) {
		return ErrPasswordCodeWait
	}
	code, err := newPasswordCode()
	if err != nil {
		return err
	}
	hash, err := utils.HashPassword(code)
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(emailCodeTTL)
	if err := codes.Save(ctx, account.Type, account.ID, purpose, hash, expires); err != nil {
		return err
	}
	if err := mail.SendPasswordResetCode(ctx, account.Type, account.ID, purpose, account.Email, account.Name, code, emailCodeTTL); err != nil {
		log.Printf("password code for %s %s: %v", account.Type, account.ID, err)
	}
	return nil
}

// checkPasswordCode compares the code. A wrong code counts an attempt. A
// right code is left in place until the password write succeeds.
func checkPasswordCode(ctx context.Context, codes passwordCodeStore, account passwordAccount, purpose, code string) error {
	if codes == nil {
		return errors.New("password reset is not configured")
	}
	code = strings.TrimSpace(code)
	if len(code) != emailCodeLength {
		return ErrPasswordCodeWrong
	}
	stored, err := codes.Get(ctx, account.Type, account.ID, purpose)
	if err != nil {
		if errors.Is(err, repository.ErrPasswordResetNotFound) {
			return ErrPasswordCodeWrong
		}
		return err
	}
	if err := passwordCodeBlock(stored.Hash, stored.ExpiresAt, stored.Attempts, time.Now().UTC()); err != nil {
		return err
	}
	if !utils.CheckPassword(code, stored.Hash) {
		attempts, addErr := codes.AddAttempt(ctx, account.Type, account.ID, purpose)
		if addErr != nil {
			return addErr
		}
		if attempts >= emailCodeMaxAttempts {
			return ErrPasswordCodeLocked
		}
		return ErrPasswordCodeWrong
	}
	return nil
}

func (s *CustomerService) customerAccount(ctx context.Context, id string) (passwordAccount, error) {
	customer, err := s.customers.GetByID(ctx, id)
	if err != nil {
		return passwordAccount{}, err
	}
	return passwordAccount{
		Type:         passwordSubjectCustomer,
		ID:           customer.ID,
		Email:        customer.Email,
		Name:         firstName(derefOr(customer.DisplayName, ""), customer.Email),
		PasswordHash: customer.PasswordHash,
	}, nil
}

func (s *SellerService) sellerAccount(ctx context.Context, id string) (passwordAccount, error) {
	seller, err := s.sellers.GetByID(ctx, id)
	if err != nil {
		return passwordAccount{}, err
	}
	return passwordAccount{
		Type:         passwordSubjectSeller,
		ID:           seller.ID,
		Email:        seller.Email,
		Name:         firstName(derefOr(seller.ContactName, seller.LegalName), seller.Email),
		PasswordHash: seller.PasswordHash,
	}, nil
}

// SendProfilePasswordCode emails a code to the signed-in customer.
func (s *CustomerService) SendProfilePasswordCode(ctx context.Context, customerID string) error {
	account, err := s.customerAccount(ctx, customerID)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return ErrCustomerNotFound
		}
		return err
	}
	return sendPasswordCode(ctx, codeStore(s.resets), s.email, account, passwordPurposeProfile)
}

// ResetProfilePassword sets the customer's password after the emailed code matches.
func (s *CustomerService) ResetProfilePassword(ctx context.Context, customerID, code, newPassword string) error {
	account, err := s.customerAccount(ctx, customerID)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return ErrCustomerNotFound
		}
		return err
	}
	if err := acceptableNewPassword(newPassword, account.PasswordHash, GiftRecipientDefaultPassword); err != nil {
		return err
	}
	if err := checkPasswordCode(ctx, codeStore(s.resets), account, passwordPurposeProfile, code); err != nil {
		return err
	}
	hash, err := utils.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.customers.UpdatePassword(ctx, customerID, hash); err != nil {
		return err
	}
	return codeStore(s.resets).Delete(ctx, account.Type, account.ID, passwordPurposeProfile)
}

// SendForgotPasswordCode emails a reset code. An unknown email returns nil.
func (s *CustomerService) SendForgotPasswordCode(ctx context.Context, email string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil
	}
	customer, err := s.customers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil
		}
		return err
	}
	account := passwordAccount{
		Type: passwordSubjectCustomer, ID: customer.ID, Email: customer.Email,
		Name: firstName(derefOr(customer.DisplayName, ""), customer.Email), PasswordHash: customer.PasswordHash,
	}
	return sendPasswordCode(ctx, codeStore(s.resets), s.email, account, passwordPurposeForgot)
}

// ResetForgottenPassword sets a new customer password from the emailed code.
func (s *CustomerService) ResetForgottenPassword(ctx context.Context, email, code, newPassword string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	customer, err := s.customers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return ErrPasswordCodeWrong
		}
		return err
	}
	account := passwordAccount{
		Type: passwordSubjectCustomer, ID: customer.ID, Email: customer.Email,
		Name: firstName(derefOr(customer.DisplayName, ""), customer.Email), PasswordHash: customer.PasswordHash,
	}
	if err := acceptableNewPassword(newPassword, account.PasswordHash, GiftRecipientDefaultPassword); err != nil {
		return err
	}
	if err := checkPasswordCode(ctx, codeStore(s.resets), account, passwordPurposeForgot, code); err != nil {
		return err
	}
	hash, err := utils.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.customers.UpdatePassword(ctx, customer.ID.String(), hash); err != nil {
		return err
	}
	return codeStore(s.resets).Delete(ctx, account.Type, account.ID, passwordPurposeForgot)
}

// SendProfilePasswordCode emails a code to the signed-in seller.
func (s *SellerService) SendProfilePasswordCode(ctx context.Context, sellerID string) error {
	account, err := s.sellerAccount(ctx, sellerID)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return ErrSellerNotFound
		}
		return err
	}
	return sendPasswordCode(ctx, codeStore(s.resets), s.email, account, passwordPurposeProfile)
}

// ResetProfilePassword sets the seller's password after the emailed code matches.
func (s *SellerService) ResetProfilePassword(ctx context.Context, sellerID, code, newPassword string) error {
	account, err := s.sellerAccount(ctx, sellerID)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return ErrSellerNotFound
		}
		return err
	}
	if err := acceptableNewPassword(newPassword, account.PasswordHash); err != nil {
		return err
	}
	if err := checkPasswordCode(ctx, codeStore(s.resets), account, passwordPurposeProfile, code); err != nil {
		return err
	}
	hash, err := utils.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.sellers.UpdatePassword(ctx, sellerID, hash); err != nil {
		return err
	}
	return codeStore(s.resets).Delete(ctx, account.Type, account.ID, passwordPurposeProfile)
}

// SendForgotPasswordCode emails a seller reset code. An unknown email returns nil.
func (s *SellerService) SendForgotPasswordCode(ctx context.Context, email string) error {
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
	account := passwordAccount{
		Type: passwordSubjectSeller, ID: seller.ID, Email: seller.Email,
		Name: firstName(derefOr(seller.ContactName, seller.LegalName), seller.Email), PasswordHash: seller.PasswordHash,
	}
	return sendPasswordCode(ctx, codeStore(s.resets), s.email, account, passwordPurposeForgot)
}

// ResetForgottenPassword sets a new seller password from the emailed code.
func (s *SellerService) ResetForgottenPassword(ctx context.Context, email, code, newPassword string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	seller, err := s.sellers.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrSellerNotFound) {
			return ErrPasswordCodeWrong
		}
		return err
	}
	account := passwordAccount{
		Type: passwordSubjectSeller, ID: seller.ID, Email: seller.Email,
		Name: firstName(derefOr(seller.ContactName, seller.LegalName), seller.Email), PasswordHash: seller.PasswordHash,
	}
	if err := acceptableNewPassword(newPassword, account.PasswordHash); err != nil {
		return err
	}
	if err := checkPasswordCode(ctx, codeStore(s.resets), account, passwordPurposeForgot, code); err != nil {
		return err
	}
	hash, err := utils.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.sellers.UpdatePassword(ctx, seller.ID.String(), hash); err != nil {
		return err
	}
	return codeStore(s.resets).Delete(ctx, account.Type, account.ID, passwordPurposeForgot)
}
