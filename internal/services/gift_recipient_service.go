package services

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

// GiftRecipientDefaultPassword is the password an account made for a gift
// recipient starts with. The recipient is asked to change it when they sign in.
const GiftRecipientDefaultPassword = "00001111"

// GiftRecipientService looks after the person a gift is sent to: it gives
// them a customer account when the order is placed — so they can review the
// gift and receive any points sent with it — and emails them once the gift
// is delivered. Until then they hear nothing, so the surprise holds.
type GiftRecipientService struct {
	orders    *repository.OrderRepository
	customers *repository.CustomerRepository
	email     *EmailService
}

func NewGiftRecipientService(
	orders *repository.OrderRepository,
	customers *repository.CustomerRepository,
	email *EmailService,
) *GiftRecipientService {
	return &GiftRecipientService{orders: orders, customers: customers, email: email}
}

// OrderPlaced runs after a customer places an order: the recipient gets an
// account (silently) and the customer gets their confirmation email. A
// failure here is logged, never failing the order itself.
func (s *GiftRecipientService) OrderPlaced(ctx context.Context, orderID uuid.UUID) {
	if s == nil {
		return
	}
	summary, err := s.orders.OrderEmailSummary(ctx, orderID)
	if err != nil {
		log.Printf("order %s placed: %v", orderID, err)
		return
	}
	if err := s.ensureAccount(ctx, summary); err != nil {
		log.Printf("order %s recipient account: %v", orderID, err)
	}
	if err := s.email.SendOrderPlaced(ctx, summary); err != nil {
		log.Printf("order %s confirmation email: %v", orderID, err)
	}
}

// ensureAccount links the order to the customer account of the recipient's
// email, making one with the default password if there is none.
func (s *GiftRecipientService) ensureAccount(ctx context.Context, o *repository.OrderEmailSummary) error {
	if o.RecipientCustomerID != nil || o.RecipientEmail == nil {
		return nil
	}
	email := normalizeEmail(*o.RecipientEmail)
	if email == "" || !strings.Contains(email, "@") || email == normalizeEmail(o.CustomerEmail) {
		return nil
	}

	existing, err := s.customers.GetByEmail(ctx, email)
	if err == nil {
		return s.orders.SetRecipientCustomer(ctx, o.OrderID, existing.ID)
	}
	if !errors.Is(err, repository.ErrCustomerNotFound) {
		return err
	}
	// A seller or admin signs in with this email; a customer account sharing
	// it would make signing in ambiguous.
	if used, err := s.customers.EmailUsedByStaffOrSeller(ctx, email); err != nil || used {
		return err
	}

	hash, err := utils.HashPassword(GiftRecipientDefaultPassword)
	if err != nil {
		return err
	}
	name := derefOr(o.RecipientName, strings.Split(email, "@")[0])
	customer := &models.Customer{
		CountryID:              o.CountryID,
		Email:                  email,
		PasswordHash:           hash,
		DisplayName:            &name,
		CustomerType:           "individual",
		Status:                 "active",
		PasswordChangeRequired: true,
	}
	if err := s.customers.Create(ctx, customer); err != nil {
		if errors.Is(err, repository.ErrCustomerDuplicate) {
			// Made by a concurrent order, or the email belongs to a closed
			// account; link to a live one if there is one.
			if existing, err := s.customers.GetByEmail(ctx, email); err == nil {
				return s.orders.SetRecipientCustomer(ctx, o.OrderID, existing.ID)
			}
			return nil
		}
		return err
	}
	return s.orders.SetRecipientCustomer(ctx, o.OrderID, customer.ID)
}

// RunDeliveredNotices emails recipients whose gifts have been delivered, on
// a timer until ctx ends. With several API servers only one does the work.
func (s *GiftRecipientService) RunDeliveredNotices(ctx context.Context, every time.Duration, exclusive Exclusive) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := exclusive.run(ctx, func(ctx context.Context) error {
			_, err := s.NotifyDelivered(ctx, 50)
			return err
		}); err != nil {
			log.Printf("gift delivered notices: %v", err)
		}
	}
}

// NotifyDelivered handles up to limit delivered orders whose recipient has
// not been told yet, and returns how many it handled.
func (s *GiftRecipientService) NotifyDelivered(ctx context.Context, limit int) (int, error) {
	due, err := s.orders.DueRecipientNotices(ctx, limit)
	if err != nil {
		return 0, err
	}
	for _, orderID := range due {
		if err := s.notifyDelivered(ctx, orderID); err != nil {
			// Left unmarked, so the next run tries again.
			log.Printf("order %s delivered notice: %v", orderID, err)
		}
	}
	return len(due), nil
}

func (s *GiftRecipientService) notifyDelivered(ctx context.Context, orderID uuid.UUID) error {
	summary, err := s.orders.OrderEmailSummary(ctx, orderID)
	if err != nil {
		return err
	}
	if summary.RecipientEmail != nil {
		// Orders placed before recipient accounts existed, or whose recipient
		// email was added later, get theirs now.
		if summary.RecipientCustomerID == nil {
			if err := s.ensureAccount(ctx, summary); err != nil {
				return err
			}
			if summary, err = s.orders.OrderEmailSummary(ctx, orderID); err != nil {
				return err
			}
		}
		tempPassword := ""
		if summary.RecipientPasswordChangeRequired {
			tempPassword = GiftRecipientDefaultPassword
		}
		if err := s.email.SendGiftDelivered(ctx, summary, tempPassword); err != nil {
			return err
		}
	}
	return s.orders.MarkRecipientNotified(ctx, orderID)
}

// ReceivedGifts lists the delivered gifts sent to a customer.
func (s *GiftRecipientService) ReceivedGifts(ctx context.Context, customerID string) ([]models.ReceivedGift, error) {
	return s.orders.ListReceivedGifts(ctx, customerID)
}
