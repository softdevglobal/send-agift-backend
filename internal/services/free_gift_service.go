package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// The first free gift program. Every registered customer has one free gift
// to send, once, to a recipient who has not had one. The seller funds it from
// a marketing budget they set, on products they choose.
//
// The order is an ordinary marketplace order marked free_gift. The product's
// price is the seller's cost, held against their budget. Delivery is paid by
// the customer, so the order total is the delivery price and goes through the
// same payment as any order. It is priced and date-checked by the same
// helpers as a paid order, and fulfilled by the same seller routes.

var (
	ErrFreeGiftDisabled    = errors.New("the free gift program is not available")
	ErrVerifyEmailRequired = errors.New("verify your email address to send your free gift")
	ErrVerifyPhoneRequired = errors.New("verify your phone number to send your free gift")
	// ErrFreeGiftRecipientContact is a recipient without an email or a phone
	// number that can be read: without both, the one-per-recipient limit
	// cannot be checked.
	ErrFreeGiftRecipientContact = errors.New("add the recipient's email address and a valid phone number to send them a free gift")
	ErrFreeGiftSelf             = errors.New("your free gift is for someone else. Choose a recipient with a different email and phone number")
	ErrIdempotencyKeyRequired   = errors.New("an Idempotency-Key header is required")
	ErrIdempotencyMismatch      = errors.New("this Idempotency-Key was already used for a different request")
	// ErrFreeGiftInput is a seller or admin request that does not make sense.
	ErrFreeGiftInput = errors.New("invalid free gift request")
	// ErrFreeGiftSellerTermsMissing is an admin setting a budget for a seller
	// who has not agreed to it.
	ErrFreeGiftSellerTermsMissing = errors.New("this seller has not agreed to the free gift marketing budget")

	// Passed through from the repository, where the database decides them.
	ErrFreeGiftAlreadyUsed          = repository.ErrFreeGiftAlreadyUsed
	ErrFreeGiftRecipientUsed        = repository.ErrFreeGiftRecipientUsed
	ErrFreeGiftUnavailable          = repository.ErrFreeGiftUnavailable
	ErrFreeGiftOutOfStock           = repository.ErrFreeGiftOutOfStock
	ErrFreeGiftProgramNotFound      = repository.ErrFreeGiftProgramNotFound
	ErrFreeGiftProductNotFound      = repository.ErrFreeGiftProductNotFound
	ErrFreeGiftProgramStopped       = repository.ErrFreeGiftProgramStopped
	ErrFreeGiftTermsRequired        = repository.ErrFreeGiftTermsRequired
	ErrFreeGiftBudgetBelowCommitted = repository.ErrFreeGiftBudgetBelowCommitted
	ErrFreeGiftCurrencyLocked       = repository.ErrFreeGiftCurrencyLocked
	ErrFreeGiftProductNotEligible   = repository.ErrFreeGiftProductNotEligible
	ErrFreeGiftCapBelowCount        = repository.ErrFreeGiftCapBelowCount
)

// Reasons a recipient cannot be sent a free gift, as the status route reports them.
const (
	freeGiftReasonSelf     = "SELF_GIFT"
	freeGiftReasonContact  = "RECIPIENT_CONTACT_REQUIRED"
	freeGiftReasonReceived = "RECIPIENT_ALREADY_USED"
)

type FreeGiftService struct {
	enabled      bool
	secret       string
	termsVersion string
	defaultCC    string
	gifts        *repository.FreeGiftRepository
	customers    *repository.CustomerRepository
	countries    *repository.CountryRepository
	// The paid order service: its pricing and delivery-date helpers are the
	// one copy both kinds of order use.
	orderSvc *OrderService
	// avail narrows the free gifts to the ones a delivery address and day
	// can be served. Optional: without it the list is not narrowed.
	avail *AvailabilityService
}

// UseAvailability lets the free gift list be filtered by a delivery address
// and a day, with the same zone and stock rules the Find gifts search uses.
func (s *FreeGiftService) UseAvailability(a *AvailabilityService) { s.avail = a }

// NewFreeGiftService builds the service. secret keys the identity hashes;
// defaultCC reads a recipient's local phone number as E.164.
func NewFreeGiftService(
	gifts *repository.FreeGiftRepository,
	customers *repository.CustomerRepository,
	countries *repository.CountryRepository,
	orders *OrderService,
	enabled bool,
	secret, termsVersion, defaultCC string,
) *FreeGiftService {
	return &FreeGiftService{
		enabled: enabled, secret: secret, termsVersion: termsVersion, defaultCC: defaultCC,
		gifts: gifts, customers: customers, countries: countries, orderSvc: orders,
	}
}

// Enabled reports whether FREE_GIFT_ENABLED is on.
func (s *FreeGiftService) Enabled() bool { return s != nil && s.enabled }

// ─── Customer ─────────────────────────────────────────────────────────────

// FreeGiftFilter narrows the free gifts to what can be delivered to one
// address on one day. Latitude and Longitude go together; DeliveryDate is
// yyyy-mm-dd and may be left empty.
type FreeGiftFilter struct {
	Country      string
	Latitude     *float64
	Longitude    *float64
	DeliveryDate string
}

// Available lists the products that can be sent as a free gift right now,
// narrowed to the ones the address and day can be served when they are given.
func (s *FreeGiftService) Available(ctx context.Context, f FreeGiftFilter) ([]models.FreeGiftOffer, error) {
	offers, err := s.gifts.ListAvailable(ctx, f.Country)
	if err != nil {
		return nil, err
	}
	if (f.Latitude == nil) != (f.Longitude == nil) {
		return nil, fmt.Errorf("%w: latitude and longitude go together", ErrFreeGiftInput)
	}
	date := strings.TrimSpace(f.DeliveryDate)
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return nil, fmt.Errorf("%w: delivery_date must be yyyy-mm-dd", ErrFreeGiftInput)
		}
	}
	// A day alone cannot be checked: shops deliver to places.
	if s.avail == nil || f.Latitude == nil || f.Longitude == nil {
		return offers, nil
	}
	found, err := s.avail.Search(ctx, GiftAvailabilityQuery{
		Latitude: *f.Latitude, Longitude: *f.Longitude,
		DeliveryDate: date, CustomerType: "personal",
	})
	if err != nil {
		return nil, err
	}
	reachable := map[string]bool{}
	for _, shop := range found.Shops {
		for _, id := range shop.ProductIDs {
			reachable[id] = true
		}
	}
	out := make([]models.FreeGiftOffer, 0, len(offers))
	for _, o := range offers {
		if reachable[o.ProductID.String()] {
			out = append(out, o)
		}
	}
	return out, nil
}

// recipientContact returns the recipient's email and E.164 phone, or ok
// false when either is missing or the phone cannot be read.
func (s *FreeGiftService) recipientContact(rec *models.Recipient) (email, phone string, ok bool) {
	if rec.Email == nil || rec.Phone == nil {
		return "", "", false
	}
	email = normalizeEmail(*rec.Email)
	if email == "" || !strings.Contains(email, "@") {
		return "", "", false
	}
	phone, err := NormalizePhone(*rec.Phone, s.defaultCC)
	if err != nil {
		return "", "", false
	}
	return email, phone, true
}

// isSelf reports a recipient who is the sender: the same email or phone.
func isSelf(sender *repository.FreeGiftCustomer, email, phone string) bool {
	if normalizeEmail(sender.Email) == email {
		return true
	}
	return sender.PhoneE164 != nil && *sender.PhoneE164 != "" && *sender.PhoneE164 == phone
}

func (s *FreeGiftService) identity(ctx context.Context, customerID uuid.UUID) (*repository.FreeGiftCustomer, error) {
	ident, err := s.gifts.CustomerIdentity(ctx, customerID)
	if errors.Is(err, repository.ErrCustomerNotFound) {
		return nil, ErrCustomerNotFound
	}
	if err != nil {
		return nil, err
	}
	if ident.Status != "active" {
		return nil, ErrCustomerNotFound
	}
	return ident, nil
}

// verifiedPhone is the sender's phone once they have proved they hold it.
func verifiedPhone(c *repository.FreeGiftCustomer) string {
	if c.PhoneVerifiedAt == nil || c.PhoneE164 == nil {
		return ""
	}
	return strings.TrimSpace(*c.PhoneE164)
}

// Status tells the customer app whether the free gift is still there, what
// is missing before it can be sent, and whether a recipient can receive one.
// It only describes; Send checks everything again.
func (s *FreeGiftService) Status(ctx context.Context, customerID, recipientID string) (*models.FreeGiftStatus, error) {
	cid, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrCustomerNotFound
	}
	ident, err := s.identity(ctx, cid)
	if err != nil {
		return nil, err
	}

	out := &models.FreeGiftStatus{Missing: []string{}}
	if ident.EmailVerifiedAt == nil {
		out.Missing = append(out.Missing, "email")
	}
	phone := verifiedPhone(ident)
	if phone == "" {
		out.Missing = append(out.Missing, "phone")
	}
	if out.Used, err = s.gifts.SendClaimUsed(ctx, sendKeys(s.secret, cid, ident.Email, phone)); err != nil {
		return nil, err
	}
	if err := ensureCanRedeem(ctx, s.customers, cid); errors.Is(err, ErrRedemptionLocked) {
		out.Locked = true
	} else if err != nil {
		return nil, err
	}
	out.Available = !out.Used && len(out.Missing) == 0 && !out.Locked

	if recipientID = strings.TrimSpace(recipientID); recipientID != "" {
		rec, err := s.customers.GetRecipientByID(ctx, customerID, recipientID)
		if errors.Is(err, repository.ErrRecipientNotFound) {
			return nil, ErrRecipientNotFound
		}
		if err != nil {
			return nil, err
		}
		rs := &models.FreeGiftRecipientStatus{ID: rec.ID}
		reason := ""
		if email, recPhone, ok := s.recipientContact(rec); !ok {
			reason = freeGiftReasonContact
		} else if isSelf(ident, email, recPhone) {
			reason = freeGiftReasonSelf
		} else {
			used, err := s.gifts.ReceiveClaimUsed(ctx, receiveKeys(s.secret, email, recPhone))
			if err != nil {
				return nil, err
			}
			if used {
				reason = freeGiftReasonReceived
			}
		}
		if reason == "" {
			rs.CanReceive = true
		} else {
			rs.Reason = &reason
		}
		out.Recipient = rs
	}
	return out, nil
}

// FreeGiftSendInput is the body of POST /customers/me/free-gifts. There is
// no price and no quantity: the server reads the price, and the quantity is 1.
type FreeGiftSendInput struct {
	ProductID    string  `json:"product_id"`
	RecipientID  string  `json:"recipient_id"`
	CountryID    string  `json:"country_id"`
	DeliveryDate string  `json:"delivery_date"`
	GiftMessage  *string `json:"gift_message"`
}

// replay returns the order this Idempotency-Key already made. A key reused
// for a different product, recipient or date is ErrIdempotencyMismatch.
func (s *FreeGiftService) replay(ctx context.Context, customerID uuid.UUID, key string, in FreeGiftSendInput) (*models.OrderDetails, error) {
	prev, err := s.gifts.FindByIdempotencyKey(ctx, customerID, key)
	if errors.Is(err, repository.ErrOrderNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	same := prev.OrderKind == models.OrderKindFreeGift &&
		len(prev.ProductIDs) == 1 && strings.EqualFold(prev.ProductIDs[0].String(), strings.TrimSpace(in.ProductID)) &&
		prev.RecipientID != nil && strings.EqualFold(prev.RecipientID.String(), strings.TrimSpace(in.RecipientID)) &&
		prev.DeliveryDate.Format("2006-01-02") == strings.TrimSpace(in.DeliveryDate)
	if !same {
		return nil, ErrIdempotencyMismatch
	}
	return s.orderSvc.Get(ctx, customerID.String(), prev.OrderID.String())
}

// Send sends the customer's free gift and creates its order: confirmed, with
// a total of 0. idempotencyKey makes a retry return the first order.
func (s *FreeGiftService) Send(ctx context.Context, customerID, idempotencyKey string, in FreeGiftSendInput) (*models.OrderDetails, error) {
	if !s.Enabled() {
		return nil, ErrFreeGiftDisabled
	}
	cid, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrCustomerNotFound
	}
	key := strings.TrimSpace(idempotencyKey)
	if len(key) < 8 || len(key) > 128 {
		return nil, ErrIdempotencyKeyRequired
	}
	if prev, err := s.replay(ctx, cid, key, in); err != nil || prev != nil {
		return prev, err
	}

	// The sender: active, email and phone both verified, no email-change hold.
	ident, err := s.identity(ctx, cid)
	if err != nil {
		return nil, err
	}
	if ident.EmailVerifiedAt == nil {
		return nil, ErrVerifyEmailRequired
	}
	senderPhone := verifiedPhone(ident)
	if senderPhone == "" {
		return nil, ErrVerifyPhoneRequired
	}
	if err := ensureCanRedeem(ctx, s.customers, cid); err != nil {
		return nil, err
	}

	// The recipient: the sender's own, with an email and a phone, and not
	// the sender.
	rec, err := s.customers.GetRecipientByID(ctx, customerID, strings.TrimSpace(in.RecipientID))
	if errors.Is(err, repository.ErrRecipientNotFound) {
		return nil, ErrRecipientNotFound
	}
	if err != nil {
		return nil, err
	}
	recEmail, recPhone, ok := s.recipientContact(rec)
	if !ok {
		return nil, ErrFreeGiftRecipientContact
	}
	if isSelf(ident, recEmail, recPhone) {
		return nil, ErrFreeGiftSelf
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
	deliveryDate, err := repository.ParseDate(strings.TrimSpace(in.DeliveryDate))
	if err != nil || deliveryDate == nil {
		return nil, ErrInvalidOrder
	}

	// The product: one unit, at the catalog price. Whether it is in the
	// program, and whether the budget and stock have room, is decided by the
	// guarded updates when the order is written.
	pid, err := uuid.Parse(strings.TrimSpace(in.ProductID))
	if err != nil {
		return nil, ErrFreeGiftUnavailable
	}
	snap, err := s.orderSvc.orders.GetCheckoutProduct(ctx, pid.String())
	if errors.Is(err, repository.ErrOrderProductNotFound) {
		return nil, ErrFreeGiftUnavailable
	}
	if err != nil {
		return nil, err
	}
	if snap.Status != "published" || snap.ShopStatus != "active" || snap.CustomerTypeVisibility == "corporate" {
		return nil, ErrFreeGiftUnavailable
	}
	sellerID, _ := uuid.Parse(snap.SellerID)
	shopID, _ := uuid.Parse(snap.ShopID)
	items := []models.OrderItem{{
		SellerID:         sellerID,
		ShopID:           shopID,
		ProductID:        pid,
		Quantity:         1,
		UnitAmount:       snap.PriceAmount,
		TotalAmount:      snap.PriceAmount,
		FulfilmentStatus: "pending",
		RewardStatus:     "none", // a free gift earns no reward points
	}}
	shops := map[string]uuid.UUID{shopID.String(): sellerID}

	// Delivery price and date: the same helpers the paid route uses.
	sellerDeliveries, err := s.orderSvc.priceShopDeliveries(ctx, customerID, &rec.ID, shops, snap.Currency)
	if err != nil {
		return nil, err
	}
	if err := s.orderSvc.checkOrderDate(ctx, deliveryDate.Format("2006-01-02"), items, sellerDeliveries); err != nil {
		return nil, err
	}
	deliveries, err := shopDeliveriesFromZones(sellerDeliveries, shops, snap.Currency)
	if err != nil {
		return nil, err
	}
	deliveryAmount := 0
	for _, d := range deliveries {
		deliveryAmount += d.Amount
	}

	// The product is free; the delivery is not. A delivery to pay waits for
	// payment like any order. With none to pay, the gift is confirmed at once.
	status := "confirmed"
	if deliveryAmount > 0 {
		status = "pending_payment"
	}
	order := &models.Order{
		OrderNumber:    newOrderNumber(),
		CustomerID:     cid,
		RecipientID:    &rec.ID,
		CountryID:      countryID,
		CustomerType:   "personal",
		DeliveryDate:   *deliveryDate,
		Status:         status,
		SubtotalAmount: snap.PriceAmount,
		DeliveryAmount: deliveryAmount,
		TotalAmount:    deliveryAmount, // what the customer pays: the delivery only
		Currency:       snap.Currency,
		GiftMessage:    in.GiftMessage,
		FreeGiftCost:   snap.PriceAmount, // what the seller's budget funds: the product only
		IdempotencyKey: &key,
	}
	write := repository.FreeGiftOrder{
		Order: order, Items: items, Deliveries: deliveries,
		SendKeys:    sendKeys(s.secret, cid, ident.Email, senderPhone),
		ReceiveKeys: receiveKeys(s.secret, recEmail, recPhone),
	}
	err = s.gifts.CreateOrder(ctx, write)
	if errors.Is(err, repository.ErrOrderDuplicate) {
		order.OrderNumber = newOrderNumber()
		err = s.gifts.CreateOrder(ctx, write)
	}
	// A double click: the second request lost the race to the first, which
	// used the same key. It gets the first one's order, not a refusal.
	if errors.Is(err, ErrFreeGiftAlreadyUsed) || errors.Is(err, repository.ErrOrderIdempotencyDuplicate) {
		if prev, rerr := s.replay(ctx, cid, key, in); rerr != nil || prev != nil {
			return prev, rerr
		}
		return nil, ErrFreeGiftAlreadyUsed
	}
	if err != nil {
		return nil, err
	}

	if err := s.orderSvc.persistCheckoutQuotes(ctx, order.ID, items, sellerDeliveries); err != nil {
		return nil, err
	}
	shopDeliveries, err := s.orderSvc.orders.ListShopDeliveries(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	// With a delivery to pay, the confirmation and the recipient's account wait
	// for the payment, as they do for any order.
	if !s.orderSvc.requirePayment || order.TotalAmount == 0 {
		s.orderSvc.gifts.OrderPlaced(ctx, order.ID)
	}
	return &models.OrderDetails{Order: *order, Items: items, ShopDeliveries: shopDeliveries}, nil
}

// ─── Seller ───────────────────────────────────────────────────────────────

// Program returns the seller's budget, or a "not_set" placeholder before
// they have set one.
func (s *FreeGiftService) Program(ctx context.Context, sellerID string) (*models.FreeGiftProgram, error) {
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrSellerNotFound
	}
	agreed, err := s.gifts.SellerAcceptedFreeGiftTerms(ctx, sid)
	if errors.Is(err, repository.ErrFreeGiftSellerNotFound) {
		return nil, ErrSellerNotFound
	}
	if err != nil {
		return nil, err
	}
	p, err := s.gifts.GetProgram(ctx, sid)
	if errors.Is(err, repository.ErrFreeGiftProgramNotFound) {
		// An agreed seller in a country with a budget has it, even if they
		// registered or agreed after the admin set it.
		if agreed {
			if applied, aerr := s.applyCountryBudgetTo(ctx, sid); aerr == nil && applied != nil {
				applied.SellerAgreed = true
				return applied, nil
			}
		}
		return &models.FreeGiftProgram{SellerID: sid, Status: models.FreeGiftProgramNotSet, SellerAgreed: agreed}, nil
	}
	if p != nil {
		p.SellerAgreed = agreed
	}
	return p, err
}

// FreeGiftProgramInput is the body of PUT /sellers/me/free-gift-program. The
// budget is set by SendAGift, so a seller can only turn their program on or
// pause it, and agree to the marketing budget if they have not yet.
type FreeGiftProgramInput struct {
	Status      string `json:"status"`
	AcceptTerms bool   `json:"accept_terms"`
}

// SetProgram turns the seller's program on or pauses it. The budget and
// currency stay what the admin set.
func (s *FreeGiftService) SetProgram(ctx context.Context, sellerID string, in FreeGiftProgramInput) (*models.FreeGiftProgram, error) {
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrSellerNotFound
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	// Agreeing on its own: a seller who registered before the agreement
	// existed, and has no budget yet, can still agree.
	if in.AcceptTerms && status == "" {
		if err := s.gifts.AcceptFreeGiftTerms(ctx, sid); err != nil {
			return nil, err
		}
		return s.Program(ctx, sellerID)
	}
	if status != models.FreeGiftProgramActive && status != models.FreeGiftProgramPaused {
		return nil, fmt.Errorf("%w: status must be active or paused", ErrFreeGiftInput)
	}
	if in.AcceptTerms {
		if err := s.gifts.AcceptFreeGiftTerms(ctx, sid); err != nil {
			return nil, err
		}
	}
	accepted, err := s.gifts.SellerAcceptedFreeGiftTerms(ctx, sid)
	if errors.Is(err, repository.ErrFreeGiftSellerNotFound) {
		return nil, ErrSellerNotFound
	}
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, ErrFreeGiftTermsRequired
	}
	if _, err := s.gifts.SetSellerProgramStatus(ctx, sid, status); err != nil {
		return nil, err
	}
	return s.Program(ctx, sellerID)
}

// Products lists the seller's products in the program.
func (s *FreeGiftService) Products(ctx context.Context, sellerID string) ([]models.FreeGiftProduct, error) {
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrSellerNotFound
	}
	return s.gifts.ListProducts(ctx, sid)
}

// FreeGiftProductInput is the body of PUT /sellers/me/free-gift-products/{productID}.
type FreeGiftProductInput struct {
	GiftsCap *int   `json:"gifts_cap"`
	Status   string `json:"status"`
}

// SetProduct adds a product to the program, or changes its cap or status.
func (s *FreeGiftService) SetProduct(ctx context.Context, sellerID string, productID uuid.UUID, in FreeGiftProductInput) (*models.FreeGiftProduct, error) {
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrSellerNotFound
	}
	if in.GiftsCap != nil && *in.GiftsCap <= 0 {
		return nil, fmt.Errorf("%w: gifts_cap must be more than 0, or left out for no cap", ErrFreeGiftInput)
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "paused" {
		return nil, fmt.Errorf("%w: status must be active or paused", ErrFreeGiftInput)
	}
	return s.gifts.UpsertProduct(ctx, sid, productID, in.GiftsCap, status)
}

// RemoveProduct takes a product out of the program.
func (s *FreeGiftService) RemoveProduct(ctx context.Context, sellerID string, productID uuid.UUID) error {
	sid, err := uuid.Parse(sellerID)
	if err != nil {
		return ErrSellerNotFound
	}
	return s.gifts.RemoveProduct(ctx, sid, productID)
}

// ─── Admin ────────────────────────────────────────────────────────────────

// AdminPrograms lists every seller's program.
func (s *FreeGiftService) AdminPrograms(ctx context.Context) ([]models.AdminFreeGiftProgram, error) {
	return s.gifts.ListPrograms(ctx)
}

// AdminSetStopped is the kill switch for one seller. Stopping refuses new
// gifts at once; confirmed gifts are still fulfilled. Re-allowing leaves the
// program paused, so the seller turns it back on themselves.
func (s *FreeGiftService) AdminSetStopped(ctx context.Context, sellerID uuid.UUID, stopped bool) (*models.FreeGiftProgram, error) {
	if stopped {
		return s.gifts.SetProgramStatus(ctx, sellerID, models.FreeGiftProgramStopped)
	}
	current, err := s.gifts.GetProgram(ctx, sellerID)
	if err != nil || current.Status != models.FreeGiftProgramStopped {
		return current, err
	}
	return s.gifts.SetProgramStatus(ctx, sellerID, models.FreeGiftProgramPaused)
}

// AdminProgramInput is the body of PUT /admin/free-gifts/sellers/{sellerID}/program:
// the marketing budget SendAGift sets for one seller, and whether it is on.
type AdminProgramInput struct {
	BudgetCap int64  `json:"budget_cap"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
}

// AdminSetProgram creates or changes a seller's free gift program. The seller
// must have agreed to the marketing budget. Status "active" starts it.
func (s *FreeGiftService) AdminSetProgram(ctx context.Context, sellerID uuid.UUID, in AdminProgramInput) (*models.FreeGiftProgram, error) {
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if len(currency) != 3 {
		return nil, fmt.Errorf("%w: currency must be a 3-letter code such as AUD", ErrFreeGiftInput)
	}
	if in.BudgetCap <= 0 {
		return nil, fmt.Errorf("%w: budget_cap must be more than 0", ErrFreeGiftInput)
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status == "" {
		status = models.FreeGiftProgramPaused
	}
	if status != models.FreeGiftProgramActive && status != models.FreeGiftProgramPaused {
		return nil, fmt.Errorf("%w: status must be active or paused", ErrFreeGiftInput)
	}
	accepted, err := s.gifts.SellerAcceptedFreeGiftTerms(ctx, sellerID)
	if errors.Is(err, repository.ErrFreeGiftSellerNotFound) {
		return nil, ErrSellerNotFound
	}
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, ErrFreeGiftSellerTermsMissing
	}
	return s.gifts.UpsertProgram(ctx, repository.FreeGiftProgramWrite{
		SellerID: sellerID, Currency: currency, BudgetCap: in.BudgetCap, Status: status,
		AcceptTerms: true, TermsVersion: s.termsVersion,
	})
}

// AdminOrders lists the free gifts that were sent: which seller funded each,
// which customer sent it, to whom, what it was and what it cost. sellerID nil
// lists every seller.
func (s *FreeGiftService) AdminOrders(ctx context.Context, sellerID *uuid.UUID, limit int) ([]models.AdminFreeGiftOrder, error) {
	return s.gifts.ListOrders(ctx, sellerID, limit)
}

// applyCountryBudgetTo gives a seller their country's budget, when there is
// one. It returns nil when the country has none.
func (s *FreeGiftService) applyCountryBudgetTo(ctx context.Context, sellerID uuid.UUID) (*models.FreeGiftProgram, error) {
	country, err := s.gifts.SellerCountry(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	b, err := s.gifts.CountryBudget(ctx, country)
	if err != nil || b == nil {
		return nil, err
	}
	return s.gifts.UpsertProgram(ctx, repository.FreeGiftProgramWrite{
		SellerID: sellerID, Currency: b.Currency, BudgetCap: b.BudgetCap, Status: b.Status,
		AcceptTerms: true, TermsVersion: s.termsVersion,
	})
}

// AdminCountryBudgets lists the budgets set by country.
func (s *FreeGiftService) AdminCountryBudgets(ctx context.Context) ([]models.FreeGiftCountryBudget, error) {
	return s.gifts.ListCountryBudgets(ctx)
}

// AdminSetCountryBudget sets one amount for a country and applies it to every
// seller registered there who has agreed to the marketing budget. The amount
// is each seller's own cap. Sellers who cannot take it now are counted, not
// failed: they have not agreed, an admin stopped them, the amount is under
// what they already hold and spent, or their program is in another currency.
// A seller who agrees later gets the country budget when they next open
// their program.
func (s *FreeGiftService) AdminSetCountryBudget(ctx context.Context, countryID uuid.UUID, in AdminProgramInput) (*models.FreeGiftCountryApply, error) {
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if len(currency) != 3 {
		return nil, fmt.Errorf("%w: currency must be a 3-letter code such as AUD", ErrFreeGiftInput)
	}
	if in.BudgetCap <= 0 {
		return nil, fmt.Errorf("%w: budget_cap must be more than 0", ErrFreeGiftInput)
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status == "" {
		status = models.FreeGiftProgramPaused
	}
	if status != models.FreeGiftProgramActive && status != models.FreeGiftProgramPaused {
		return nil, fmt.Errorf("%w: status must be active or paused", ErrFreeGiftInput)
	}
	if err := s.gifts.UpsertCountryBudget(ctx, countryID, currency, in.BudgetCap, status); err != nil {
		if errors.Is(err, repository.ErrFreeGiftCountryNotFound) {
			return nil, ErrInvalidCountry
		}
		return nil, err
	}
	sellers, err := s.gifts.CountrySellers(ctx, countryID)
	if err != nil {
		return nil, err
	}
	out := &models.FreeGiftCountryApply{}
	for sellerID, agreed := range sellers {
		if !agreed {
			out.NotAgreed++
			continue
		}
		_, err := s.gifts.UpsertProgram(ctx, repository.FreeGiftProgramWrite{
			SellerID: sellerID, Currency: currency, BudgetCap: in.BudgetCap, Status: status,
			AcceptTerms: true, TermsVersion: s.termsVersion,
		})
		switch {
		case err == nil:
			out.Applied++
		case errors.Is(err, repository.ErrFreeGiftProgramStopped):
			out.Stopped++
		case errors.Is(err, repository.ErrFreeGiftBudgetBelowCommitted):
			out.BelowCommitted++
		case errors.Is(err, repository.ErrFreeGiftCurrencyLocked):
			out.CurrencyLocked++
		default:
			return nil, err
		}
	}
	b, err := s.gifts.CountryBudget(ctx, countryID)
	if err != nil {
		return nil, err
	}
	if b != nil {
		out.Budget = *b
		out.Budget.Sellers = len(sellers)
		for _, agreed := range sellers {
			if agreed {
				out.Budget.Agreed++
			}
		}
	}
	return out, nil
}
