package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// maxEmailAttempts is how many times an email is tried before it is given
// up on.
const maxEmailAttempts = 6

// EmailSender hands one rendered email to a mail provider.
type EmailSender interface {
	Send(ctx context.Context, e repository.OutboxEmail) error
}

// EmailService queues transactional emails and delivers them in the
// background. Every email is rendered and written to the outbox first, so a
// slow or failing provider never holds up a request, and a send that fails
// is retried.
//
// A nil *EmailService is valid and sends nothing, so services work without
// one in tests.
type EmailService struct {
	repo   *repository.EmailRepository
	sender EmailSender
	webURL string
	wake   chan struct{}
}

// NewEmailService builds the service. sender may be nil while email is not
// configured: emails still queue, and are sent once a sender is set.
func NewEmailService(repo *repository.EmailRepository, sender EmailSender, webURL string) *EmailService {
	return &EmailService{repo: repo, sender: sender, webURL: strings.TrimRight(webURL, "/"), wake: make(chan struct{}, 1)}
}

// Wake runs the delivery loop now instead of waiting for its next tick.
func (s *EmailService) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default: // a run is already due
	}
}

func (s *EmailService) queue(ctx context.Context, kind, dedupeKey, toEmail, toName string, c *EmailContent) error {
	if s == nil {
		return nil
	}
	toEmail = strings.TrimSpace(toEmail)
	if toEmail == "" {
		return nil
	}
	added, err := s.repo.Enqueue(ctx, &repository.OutboxEmail{
		Kind:      kind,
		DedupeKey: dedupeKey,
		ToEmail:   toEmail,
		ToName:    strings.TrimSpace(toName),
		Subject:   c.Subject,
		HTMLBody:  c.HTML,
		TextBody:  c.Text,
	})
	if err != nil {
		return fmt.Errorf("queue %s email: %w", kind, err)
	}
	if added {
		s.Wake()
	}
	return nil
}

// ── The emails ──────────────────────────────────────────────────────────

// SendCustomerWelcome greets a customer who just signed up.
func (s *EmailService) SendCustomerWelcome(ctx context.Context, c *models.Customer) error {
	if s == nil {
		return nil
	}
	name := firstName(derefOr(c.DisplayName, ""), c.Email)
	content, err := renderCustomerWelcome(s.webURL, name)
	if err != nil {
		return err
	}
	return s.queue(ctx, "customer_welcome", "customer_welcome:"+c.ID.String(), c.Email, name, content)
}

// SendSellerEmailCode sends a seller the 6-digit code that confirms their
// email. Every code is its own email, so a resend is never deduplicated away.
func (s *EmailService) SendSellerEmailCode(ctx context.Context, sellerID uuid.UUID, email, name, code string, ttl time.Duration) error {
	if s == nil {
		return nil
	}
	content, err := renderSellerEmailCode(s.webURL, businessName(name, email), code, ttl)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("seller_email_code:%s:%d", sellerID, time.Now().UnixNano())
	return s.queue(ctx, "seller_email_code", key, email, name, content)
}

// SendSellerPendingReview welcomes a seller whose email is confirmed and
// tells them an admin is reviewing their account.
func (s *EmailService) SendSellerPendingReview(ctx context.Context, sellerID uuid.UUID, email, name string) error {
	if s == nil {
		return nil
	}
	content, err := renderSellerPendingReview(s.webURL, businessName(name, email))
	if err != nil {
		return err
	}
	return s.queue(ctx, "seller_pending_review", "seller_pending_review:"+sellerID.String(), email, name, content)
}

// SendSellerReviewed tells a seller an admin approved or rejected them.
func (s *EmailService) SendSellerReviewed(ctx context.Context, seller *models.Seller) error {
	if s == nil {
		return nil
	}
	name := sellerName(seller)
	var (
		content *EmailContent
		err     error
	)
	switch seller.VerificationStatus {
	case "verified":
		content, err = renderSellerApproved(s.webURL, businessName(name, seller.Email), derefOr(seller.VerificationNote, ""))
	case "rejected":
		content, err = renderSellerRejected(s.webURL, businessName(name, seller.Email), derefOr(seller.VerificationNote, ""))
	default:
		return nil
	}
	if err != nil {
		return err
	}
	reviewedAt := time.Now()
	if seller.VerificationReviewedAt != nil {
		reviewedAt = *seller.VerificationReviewedAt
	}
	kind := "seller_" + seller.VerificationStatus
	key := fmt.Sprintf("%s:%s:%d", kind, seller.ID, reviewedAt.UnixNano())
	return s.queue(ctx, kind, key, seller.Email, name, content)
}

// SendOrderPlaced confirms a new order to the customer who placed it.
func (s *EmailService) SendOrderPlaced(ctx context.Context, o *repository.OrderEmailSummary) error {
	if s == nil {
		return nil
	}
	content, err := renderOrderPlaced(s.webURL, o)
	if err != nil {
		return err
	}
	return s.queue(ctx, "order_placed", "order_placed:"+o.OrderID.String(), o.CustomerEmail, derefOr(o.CustomerName, ""), content)
}

// SendGiftDelivered tells the recipient their gift arrived. tempPassword is
// the password their new account starts with, or empty when they already
// chose their own.
func (s *EmailService) SendGiftDelivered(ctx context.Context, o *repository.OrderEmailSummary, tempPassword string) error {
	if s == nil || o.RecipientEmail == nil {
		return nil
	}
	content, err := renderGiftDelivered(s.webURL, o, tempPassword)
	if err != nil {
		return err
	}
	return s.queue(ctx, "gift_delivered", "gift_delivered:"+o.OrderID.String(), *o.RecipientEmail, derefOr(o.RecipientName, ""), content)
}

// ── Delivery ────────────────────────────────────────────────────────────

// RunDeliveryLoop sends due emails on a timer until ctx ends. With several
// API servers, only the one holding the lock sends.
func (s *EmailService) RunDeliveryLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		if _, err := exclusive.run(ctx, func(ctx context.Context) error {
			for {
				n, err := s.DeliverDue(ctx, 100)
				if err != nil || n < 100 {
					return err
				}
			}
		}); err != nil {
			log.Printf("email delivery: %v", err)
		}
	}
}

// DeliverDue sends up to limit due emails and returns how many it handled.
func (s *EmailService) DeliverDue(ctx context.Context, limit int) (int, error) {
	if s.sender == nil {
		return 0, nil
	}
	due, err := s.repo.Due(ctx, limit)
	if err != nil {
		return 0, err
	}
	for _, e := range due {
		sendErr := s.sender.Send(ctx, e)
		if sendErr == nil {
			if err := s.repo.MarkSent(ctx, e.ID); err != nil {
				return 0, err
			}
			continue
		}
		var retryAt *time.Time
		var perm *permanentEmailError
		if !errors.As(sendErr, &perm) && e.Attempts+1 < maxEmailAttempts {
			// 1, 4, 9, 16, 25 minutes.
			next := time.Now().Add(time.Duration((e.Attempts+1)*(e.Attempts+1)) * time.Minute)
			retryAt = &next
		}
		log.Printf("email %s to %s (%s): %v", e.ID, e.ToEmail, e.Kind, sendErr)
		if err := s.repo.MarkAttemptFailed(ctx, e.ID, sendErr.Error(), retryAt); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

// permanentEmailError is a send the provider refused for good. A bad
// address or a rejected sender. So it is not retried.
type permanentEmailError struct{ msg string }

func (e *permanentEmailError) Error() string { return e.msg }

// ── ZeptoMail ───────────────────────────────────────────────────────────

// ZeptoMailSender sends through the ZeptoMail email API.
type ZeptoMailSender struct {
	client      *http.Client
	url         string
	token       string
	fromAddress string
	fromName    string
}

// NewZeptoMailSender builds a sender, or returns nil when no token is set.
func NewZeptoMailSender(url, token, fromAddress, fromName string) *ZeptoMailSender {
	token = strings.Trim(strings.TrimSpace(token), `"`)
	if token == "" {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(token), "zoho-enczapikey") {
		token = "Zoho-enczapikey " + token
	}
	return &ZeptoMailSender{
		client:      &http.Client{Timeout: 20 * time.Second},
		url:         url,
		token:       token,
		fromAddress: fromAddress,
		fromName:    fromName,
	}
}

type zeptoAddress struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
}

type zeptoRecipient struct {
	EmailAddress zeptoAddress `json:"email_address"`
}

type zeptoInlineImage struct {
	MimeType string `json:"mime_type"`
	Content  string `json:"content"` // base64
	CID      string `json:"cid"`
}

type zeptoMessage struct {
	From            zeptoAddress       `json:"from"`
	To              []zeptoRecipient   `json:"to"`
	Subject         string             `json:"subject"`
	HTMLBody        string             `json:"htmlbody"`
	TextBody        string             `json:"textbody"`
	InlineImages    []zeptoInlineImage `json:"inline_images,omitempty"`
	ClientReference string             `json:"client_reference,omitempty"`
	TrackClicks     bool               `json:"track_clicks"`
	TrackOpens      bool               `json:"track_opens"`
}

func (z *ZeptoMailSender) Send(ctx context.Context, e repository.OutboxEmail) error {
	body, err := json.Marshal(zeptoMessage{
		From:            zeptoAddress{Address: z.fromAddress, Name: z.fromName},
		To:              []zeptoRecipient{{EmailAddress: zeptoAddress{Address: e.ToEmail, Name: e.ToName}}},
		Subject:         e.Subject,
		HTMLBody:        e.HTMLBody,
		TextBody:        e.TextBody,
		InlineImages:    inlineImagesFor(e.HTMLBody),
		ClientReference: e.ID.String(),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, z.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", z.token)

	resp, err := z.client.Do(req)
	if err != nil {
		return fmt.Errorf("zeptomail: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	msg := fmt.Sprintf("zeptomail: %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	// A rejected request fails the same way next time. Throttling, and a
	// rejected token or account (fixed by correcting the settings), are
	// retried instead, so a configuration mistake doesn't lose emails.
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden:
		return errors.New(msg)
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return &permanentEmailError{msg: msg}
	}
	return errors.New(msg)
}

// inlineImagesFor attaches the logo to an email that shows it. It travels
// with the email (cid:) rather than as a link, so it shows without the
// website being reachable and isn't blocked as a remote image.
func inlineImagesFor(html string) []zeptoInlineImage {
	if !strings.Contains(html, "cid:"+emailLogoCID) {
		return nil
	}
	return []zeptoInlineImage{{
		MimeType: "image/png",
		Content:  base64.StdEncoding.EncodeToString(emailLogoPNG),
		CID:      emailLogoCID,
	}}
}

// ── Helpers ─────────────────────────────────────────────────────────────

func derefOr(s *string, fallback string) string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return fallback
	}
	return strings.TrimSpace(*s)
}

// firstName is the first word of a name, or the start of the email address.
func firstName(name, email string) string {
	if f := strings.Fields(name); len(f) > 0 {
		return f[0]
	}
	if at := strings.Index(email, "@"); at > 0 {
		return email[:at]
	}
	return "there"
}

// businessName greets a seller by their whole trading name. The first word
// of "Kim's Blooms" is not a name.
func businessName(name, email string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return firstName("", email)
}

func sellerName(s *models.Seller) string {
	return derefOr(s.TradingName, s.LegalName)
}
