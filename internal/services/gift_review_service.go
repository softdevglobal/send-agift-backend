package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
	"myapp/internal/utils"
)

const (
	giftReviewUse = "gift_review"
	giftReviewTTL = 60 * 24 * time.Hour
)

var (
	ErrGiftReviewLink    = errors.New("this review link is not valid any more")
	ErrGiftReviewAccount = errors.New("this email belongs to another kind of account. Sign in to review your gift")
)

// giftReviewClaims is what a review link proves: this order was sent to this
// email or phone. It carries no role, so it is never accepted as a session.
type giftReviewClaims struct {
	Use         string `json:"use"`
	OrderID     string `json:"order_id"`
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
	jwt.RegisteredClaims
}

func newGiftReviewToken(secret string, orderID uuid.UUID, channel, dest string) (string, error) {
	claims := giftReviewClaims{
		Use: giftReviewUse, OrderID: orderID.String(), Channel: channel, Destination: dest,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(giftReviewTTL))},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// VerifyDestination checks a code sent to an email or phone and spends it,
// without signing anyone in. The caller decides what the proof unlocks.
func (s *LoginCodeService) VerifyDestination(ctx context.Context, channel, raw, code string) error {
	dest, err := s.normalize(channel, raw)
	if err != nil {
		return ErrLoginCodeWrong
	}
	return s.check(ctx, channel, dest, loginPurpose, code, nil)
}

// GiftReviewService lets a gift recipient review what they were sent from a
// link in their email or text: they prove the address with a one-time code,
// and an account is made for them if they have none.
type GiftReviewService struct {
	orders    *repository.OrderRepository
	customers *repository.CustomerRepository
	codes     *LoginCodeService
	secret    string
	expiry    time.Duration
}

func NewGiftReviewService(
	orders *repository.OrderRepository,
	customers *repository.CustomerRepository,
	codes *LoginCodeService,
	jwtSecret string,
	jwtExpiry time.Duration,
) *GiftReviewService {
	return &GiftReviewService{orders: orders, customers: customers, codes: codes, secret: jwtSecret, expiry: jwtExpiry}
}

// GiftReviewPreview is what the review link shows before anyone signs in.
type GiftReviewPreview struct {
	OrderNumber string                      `json:"order_number"`
	SenderName  string                      `json:"sender_name"`
	Channel     string                      `json:"channel"`
	Destination string                      `json:"destination"` // masked
	Items       []repository.GiftReviewItem `json:"items"`
}

// GiftReviewSession is a signed-in recipient, ready to review.
type GiftReviewSession struct {
	Token          string `json:"token"`
	Role           string `json:"role"`
	AccountCreated bool   `json:"account_created"`
}

func (s *GiftReviewService) parse(raw string) (*giftReviewClaims, uuid.UUID, error) {
	claims := &giftReviewClaims{}
	_, err := jwt.ParseWithClaims(strings.TrimSpace(raw), claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrGiftReviewLink
		}
		return []byte(s.secret), nil
	})
	if err != nil || claims.Use != giftReviewUse || claims.Destination == "" {
		return nil, uuid.Nil, ErrGiftReviewLink
	}
	orderID, err := uuid.Parse(claims.OrderID)
	if err != nil {
		return nil, uuid.Nil, ErrGiftReviewLink
	}
	return claims, orderID, nil
}

// Preview shows the gift behind a review link.
func (s *GiftReviewService) Preview(ctx context.Context, raw string) (*GiftReviewPreview, error) {
	claims, orderID, err := s.parse(raw)
	if err != nil {
		return nil, err
	}
	summary, err := s.orders.OrderEmailSummary(ctx, orderID)
	if errors.Is(err, repository.ErrOrderNotFound) {
		return nil, ErrGiftReviewLink
	}
	if err != nil {
		return nil, err
	}
	items, err := s.orders.GiftReviewItems(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return &GiftReviewPreview{
		OrderNumber: summary.OrderNumber,
		SenderName:  firstName(derefOr(summary.CustomerName, ""), summary.CustomerEmail),
		Channel:     claims.Channel,
		Destination: maskDestination(claims.Channel, claims.Destination),
		Items:       items,
	}, nil
}

// RequestCode sends the one-time code to the address the link was sent to.
func (s *GiftReviewService) RequestCode(ctx context.Context, raw string) error {
	claims, _, err := s.parse(raw)
	if err != nil {
		return err
	}
	return s.codes.RequestLoginCode(ctx, claims.Channel, claims.Destination)
}

// Verify checks the code, finds or makes the recipient's account, links the
// gift to it and signs them in.
func (s *GiftReviewService) Verify(ctx context.Context, raw, code string) (*GiftReviewSession, error) {
	claims, orderID, err := s.parse(raw)
	if err != nil {
		return nil, err
	}
	if err := s.codes.VerifyDestination(ctx, claims.Channel, claims.Destination, code); err != nil {
		return nil, err
	}
	summary, err := s.orders.OrderEmailSummary(ctx, orderID)
	if errors.Is(err, repository.ErrOrderNotFound) {
		return nil, ErrGiftReviewLink
	}
	if err != nil {
		return nil, err
	}

	customer, created, err := s.accountFor(ctx, summary, claims.Channel, claims.Destination)
	if err != nil {
		return nil, err
	}
	if err := s.orders.SetRecipientCustomer(ctx, orderID, customer.ID); err != nil {
		return nil, err
	}
	token, err := utils.GenerateJWT(customer.ID.String(), customer.Email, "customer", s.secret, s.expiry)
	if err != nil {
		return nil, err
	}
	return &GiftReviewSession{Token: token, Role: "customer", AccountCreated: created}, nil
}

// accountFor returns the recipient's account, making one when there is none.
func (s *GiftReviewService) accountFor(ctx context.Context, o *repository.OrderEmailSummary, channel, dest string) (*models.Customer, bool, error) {
	if o.RecipientCustomerID != nil {
		c, err := s.customers.GetByID(ctx, o.RecipientCustomerID.String())
		return c, false, err
	}
	c, err := s.codes.findCustomer(ctx, channel, dest)
	if err == nil {
		s.markVerified(ctx, c, channel, dest)
		return c, false, nil
	}
	if !errors.Is(err, repository.ErrCustomerNotFound) {
		return nil, false, err
	}

	email := dest
	var phone *string
	if channel == ChannelSMS {
		// An account needs an email; .invalid can never receive mail, so the
		// recipient can swap in their own from their profile.
		email = strings.TrimPrefix(dest, "+") + "@phone.sendagift.invalid"
		phone = &dest
	} else if used, err := s.customers.EmailUsedByStaffOrSeller(ctx, email); err != nil {
		return nil, false, err
	} else if used {
		return nil, false, ErrGiftReviewAccount
	}

	hash, err := utils.HashPassword(uuid.NewString())
	if err != nil {
		return nil, false, err
	}
	name := derefOr(o.RecipientName, strings.Split(email, "@")[0])
	c = &models.Customer{
		CountryID: o.CountryID, Email: email, Phone: phone, PasswordHash: hash,
		DisplayName: &name, CustomerType: "individual", Status: "active",
		PasswordChangeRequired: true,
	}
	if err := s.customers.Create(ctx, c); err != nil {
		if errors.Is(err, repository.ErrCustomerDuplicate) {
			// Made a moment ago by a second tap of the same link.
			existing, err2 := s.codes.findCustomer(ctx, channel, dest)
			return existing, false, err2
		}
		return nil, false, err
	}
	s.markVerified(ctx, c, channel, dest)
	return c, true, nil
}

func (s *GiftReviewService) markVerified(ctx context.Context, c *models.Customer, channel, dest string) {
	if channel == ChannelEmail {
		_ = s.customers.MarkEmailVerified(ctx, c.ID.String())
		return
	}
	// A number another account verified first is left alone.
	_ = s.customers.SetVerifiedPhone(ctx, c.ID.String(), dest, dest)
}

func maskDestination(channel, dest string) string {
	if channel == ChannelSMS {
		return maskPhone(dest)
	}
	at := strings.Index(dest, "@")
	if at < 1 {
		return "***"
	}
	return dest[:1] + "***" + dest[at:]
}
