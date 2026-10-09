ere's the complete build guide for customer sign-in with a one-time code sent by phone (textbee) or email. I haven't changed any files, and I haven't compiled or run this code, so run go build ./... after the backend steps and npm run build after the frontend steps. The code follows your existing patterns: email_outbox, the seller email code, password_reset_codes, and the social sign-up token.

What the feature does:

A customer chooses Phone or Email, gets a 6-digit code, and enters it.
If an account exists, they're signed in. If not, they go to /register with that phone or email already verified and locked.
Phone sign-in only works for a number someone has verified with a code. The unverified phones typed at signup can't be used to open an account.
Gift-created accounts can no longer use the shared default password. Those recipients sign in with an email code instead, which closes the P0 issue.
Before you start: in .env, TEXTBEE_DEFAULT_COUNTRY_CODE=+94 disagrees with its comment ("Australia = +61"). Set whichever is right. It decides how local numbers like 0771234567 get converted.

Backend (SendAGift_GO)
Step 1: Add the phone-number library

cd C:\Users\syapa\OneDrive\Desktop\softdevglobal\projects\SendAGift_GO
go get github.com/nyaruka/phonenumbers
Step 2: Migration
New file internal/database/migrations/000064_customer_login_codes.up.sql


-- Customers sign in with a 6-digit code sent by SMS (textbee) or email, and
-- verify the phone on their profile the same way. A phone only signs in once
-- it has been verified; the phone typed at sign-up was never checked.
ALTER TABLE customer.customers
    ADD COLUMN IF NOT EXISTS phone_e164        text,
    ADD COLUMN IF NOT EXISTS phone_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS email_verified_at timestamptz;
-- A verified number belongs to one live account.
CREATE UNIQUE INDEX IF NOT EXISTS customers_verified_phone_uq
    ON customer.customers (phone_e164)
    WHERE phone_verified_at IS NOT NULL AND deleted_at IS NULL;
-- One live code per destination and purpose. Sends and wrong tries are
-- counted per day from window_start, so a resend does not reset guesses.
CREATE TABLE IF NOT EXISTS core.login_codes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    channel       text NOT NULL CHECK (channel IN ('email', 'sms')),
    destination   text NOT NULL,
    purpose       text NOT NULL CHECK (purpose IN ('login', 'verify_phone')),
    customer_id   uuid REFERENCES customer.customers (id) ON DELETE CASCADE,
    code_hash     text NOT NULL DEFAULT '',
    expires_at    timestamptz NOT NULL,
    sent_at       timestamptz NOT NULL DEFAULT now(),
    window_start  timestamptz NOT NULL DEFAULT now(),
    send_count    integer NOT NULL DEFAULT 1 CHECK (send_count >= 0),
    attempts      integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    UNIQUE (channel, destination, purpose)
);
-- Every SMS is written here first and sent by a background worker,
-- like core.email_outbox.
CREATE TABLE IF NOT EXISTS core.sms_outbox (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            text NOT NULL,
    dedupe_key      text NOT NULL UNIQUE,
    to_phone        text NOT NULL,
    body            text NOT NULL,
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);
CREATE INDEX IF NOT EXISTS sms_outbox_pending_idx
    ON core.sms_outbox (next_attempt_at)
    WHERE status = 'pending';
New file internal/database/migrations/000064_customer_login_codes.down.sql


DROP TABLE IF EXISTS core.sms_outbox;
DROP TABLE IF EXISTS core.login_codes;
DROP INDEX IF EXISTS customer.customers_verified_phone_uq;
ALTER TABLE customer.customers
    DROP COLUMN IF EXISTS email_verified_at,
    DROP COLUMN IF EXISTS phone_verified_at,
    DROP COLUMN IF EXISTS phone_e164;
main.go already runs MigrateUp at startup, so this applies on the next run.

Step 3: Config
internal/config/config.go: add these fields to Config, after the Facebook fields:


// SMS through a textbee.dev Android gateway. Without a key, messages
// queue up and are sent once one is set.
TextBeeAPIKey          string
TextBeeDeviceID        string
TextBeeAPIBase         string
TextBeeSIMSubscription string
// DefaultPhoneCountryCode reads a local number (0771234567) as E.164.
DefaultPhoneCountryCode string
In Load(), inside cfg := &Config{ ... }:


TextBeeAPIKey:           os.Getenv("TEXTBEE_API_KEY"),
TextBeeDeviceID:         os.Getenv("TEXTBEE_DEVICE_ID"),
TextBeeAPIBase:          strings.TrimRight(envOr("TEXTBEE_API_BASE", "https://api.textbee.dev/api/v1"), "/"),
TextBeeSIMSubscription:  os.Getenv("TEXTBEE_SIM_SUBSCRIPTION_ID"),
DefaultPhoneCountryCode: envOr("TEXTBEE_DEFAULT_COUNTRY_CODE", "+61"),
Next to the other pair checks:


if (cfg.TextBeeAPIKey == "") != (cfg.TextBeeDeviceID == "") {
	return nil, fmt.Errorf("set both TEXTBEE_API_KEY and TEXTBEE_DEVICE_ID, or neither")
}
Step 4: Phone normalisation
New file internal/services/phone.go


package services
import (
	"errors"
	"strconv"
	"strings"
	"github.com/nyaruka/phonenumbers"
)
var ErrInvalidPhone = errors.New("enter a valid phone number")
// NormalizePhone returns raw in E.164 (+94771234567). A number without a
// country code is read as one from defaultCountryCode ("+94").
func NormalizePhone(raw, defaultCountryCode string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInvalidPhone
	}
	if strings.HasPrefix(raw, "00") {
		raw = "+" + raw[2:]
	}
	cc, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(defaultCountryCode), "+"))
	num, err := phonenumbers.Parse(raw, phonenumbers.GetRegionCodeForCountryCode(cc))
	if err != nil || !phonenumbers.IsValidNumber(num) {
		return "", ErrInvalidPhone
	}
	return phonenumbers.Format(num, phonenumbers.E164), nil
}
// maskPhone keeps a number out of logs: +94771234567 -> +947*****567.
func maskPhone(e164 string) string {
	if len(e164) < 8 {
		return "***"
	}
	return e164[:4] + strings.Repeat("*", len(e164)-7) + e164[len(e164)-3:]
}
New file internal/services/phone_test.go


package services
import "testing"
func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		raw, want string
		ok        bool
	}{
		{"0771234567", "+94771234567", true},
		{"+94 77 123 4567", "+94771234567", true},
		{"0094771234567", "+94771234567", true},
		{"+61 412 345 678", "+61412345678", true},
		{"12", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, err := NormalizePhone(c.raw, "+94")
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q ok=%v", c.raw, got, err, c.want, c.ok)
		}
	}
}
Step 5: SMS outbox repository
New file internal/repository/sms_repository.go


package repository
import (
	"context"
	"time"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)
// OutboxSMS is one text message waiting in core.sms_outbox.
type OutboxSMS struct {
	ID        uuid.UUID
	Kind      string
	DedupeKey string
	ToPhone   string
	Body      string
	Attempts  int
}
type SMSRepository struct {
	db *pgxpool.Pool
}
func NewSMSRepository(db *pgxpool.Pool) *SMSRepository {
	return &SMSRepository{db: db}
}
// Enqueue writes a message to the outbox and reports whether it was new.
func (r *SMSRepository) Enqueue(ctx context.Context, m *OutboxSMS) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		insert into core.sms_outbox (kind, dedupe_key, to_phone, body)
		values ($1, $2, $3, $4)
		on conflict (dedupe_key) do nothing`,
		m.Kind, m.DedupeKey, m.ToPhone, m.Body)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
// Due returns up to limit messages whose turn to send has come, oldest first.
func (r *SMSRepository) Due(ctx context.Context, limit int) ([]OutboxSMS, error) {
	rows, err := r.db.Query(ctx, `
		select id, kind, dedupe_key, to_phone, body, attempts
		from core.sms_outbox
		where status = 'pending' and next_attempt_at <= now()
		order by next_attempt_at
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutboxSMS{}
	for rows.Next() {
		var m OutboxSMS
		if err := rows.Scan(&m.ID, &m.Kind, &m.DedupeKey, &m.ToPhone, &m.Body, &m.Attempts); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (r *SMSRepository) MarkSent(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update core.sms_outbox
		set status = 'sent', sent_at = now(), attempts = attempts + 1, last_error = null
		where id = $1`, id)
	return err
}
// MarkAttemptFailed records a failed send. With a retry time the message
// waits for it; without one it is given up on.
func (r *SMSRepository) MarkAttemptFailed(ctx context.Context, id uuid.UUID, reason string, retryAt *time.Time) error {
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	_, err := r.db.Exec(ctx, `
		update core.sms_outbox
		set attempts = attempts + 1,
		    last_error = $2,
		    status = case when $3::timestamptz is null then 'failed' else 'pending' end,
		    next_attempt_at = coalesce($3::timestamptz, next_attempt_at)
		where id = $1`, id, reason, retryAt)
	return err
}
Step 6: SMS service and textbee sender
internal/database/lock.go: add to the const block:


LockSMSDelivery         int64 = 7_301_007
New file internal/services/sms_service.go


package services
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"myapp/internal/repository"
)
// maxSMSAttempts is low: a sign-in code is useless after ten minutes.
const maxSMSAttempts = 3
// SMSSender hands one text message to an SMS gateway.
type SMSSender interface {
	Send(ctx context.Context, m repository.OutboxSMS) error
}
// SMSService queues text messages and delivers them in the background, the
// same way EmailService does. A nil *SMSService sends nothing.
type SMSService struct {
	repo   *repository.SMSRepository
	sender SMSSender
	wake   chan struct{}
}
// NewSMSService builds the service. sender may be nil while SMS is not
// configured: messages still queue, and are sent once a sender is set.
func NewSMSService(repo *repository.SMSRepository, sender SMSSender) *SMSService {
	return &SMSService{repo: repo, sender: sender, wake: make(chan struct{}, 1)}
}
func (s *SMSService) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *SMSService) queue(ctx context.Context, kind, dedupeKey, toPhone, body string) error {
	if s == nil {
		return nil
	}
	added, err := s.repo.Enqueue(ctx, &repository.OutboxSMS{
		Kind: kind, DedupeKey: dedupeKey, ToPhone: toPhone, Body: body,
	})
	if err != nil {
		return fmt.Errorf("queue %s sms: %w", kind, err)
	}
	if added {
		s.Wake()
	}
	return nil
}
// SendLoginCode texts the 6-digit code a customer signs in or verifies with.
func (s *SMSService) SendLoginCode(ctx context.Context, phone, code string, validFor time.Duration) error {
	body := fmt.Sprintf("%s is your SendAGift code. It expires in %d minutes. Never share it.",
		code, int(validFor.Minutes()))
	key := fmt.Sprintf("login_code:%s:%d", phone, time.Now().UnixNano())
	return s.queue(ctx, "login_code", key, phone, body)
}
// RunDeliveryLoop sends due messages on a timer until ctx ends. With several
// API servers, only the one holding the lock sends.
func (s *SMSService) RunDeliveryLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
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
				n, err := s.DeliverDue(ctx, 50)
				if err != nil || n < 50 {
					return err
				}
			}
		}); err != nil {
			log.Printf("sms delivery: %v", err)
		}
	}
}
// DeliverDue sends up to limit due messages and returns how many it handled.
func (s *SMSService) DeliverDue(ctx context.Context, limit int) (int, error) {
	if s.sender == nil {
		return 0, nil
	}
	due, err := s.repo.Due(ctx, limit)
	if err != nil {
		return 0, err
	}
	for _, m := range due {
		sendErr := s.sender.Send(ctx, m)
		if sendErr == nil {
			if err := s.repo.MarkSent(ctx, m.ID); err != nil {
				return 0, err
			}
			continue
		}
		var retryAt *time.Time
		var perm *permanentSMSError
		if !errors.As(sendErr, &perm) && m.Attempts+1 < maxSMSAttempts {
			next := time.Now().Add(time.Duration(m.Attempts+1) * 30 * time.Second)
			retryAt = &next
		}
		log.Printf("sms %s to %s (%s): %v", m.ID, maskPhone(m.ToPhone), m.Kind, sendErr)
		if err := s.repo.MarkAttemptFailed(ctx, m.ID, sendErr.Error(), retryAt); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}
// permanentSMSError is a send the gateway refused for good, so it is not retried.
type permanentSMSError struct{ msg string }
func (e *permanentSMSError) Error() string { return e.msg }
// ── textbee.dev ─────────────────────────────────────────────────────────
// TextBeeSender sends through a textbee.dev Android device.
type TextBeeSender struct {
	client *http.Client
	url    string
	apiKey string
	simID  *int
}
// NewTextBeeSender builds a sender, or returns nil when no key or device is set.
func NewTextBeeSender(apiBase, deviceID, apiKey, simSubscriptionID string) *TextBeeSender {
	apiKey, deviceID = strings.TrimSpace(apiKey), strings.TrimSpace(deviceID)
	if apiKey == "" || deviceID == "" {
		return nil
	}
	t := &TextBeeSender{
		client: &http.Client{Timeout: 15 * time.Second},
		url:    strings.TrimRight(apiBase, "/") + "/gateway/devices/" + url.PathEscape(deviceID) + "/send-sms",
		apiKey: apiKey,
	}
	if n, err := strconv.Atoi(strings.TrimSpace(simSubscriptionID)); err == nil {
		t.simID = &n
	}
	return t
}
func (t *TextBeeSender) Send(ctx context.Context, m repository.OutboxSMS) error {
	payload := map[string]any{"recipients": []string{m.ToPhone}, "message": m.Body}
	if t.simID != nil {
		payload["simSubscriptionId"] = *t.simID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &permanentSMSError{msg: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", t.apiKey)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	msg := fmt.Sprintf("textbee %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return &permanentSMSError{msg: msg}
	}
	return errors.New(msg)
}
Check the textbee endpoint and body shape (/gateway/devices/{id}/send-sms, x-api-key, recipients, message) against their current docs before relying on it.

Step 7: Email login code
internal/services/email_templates.go: add this after renderPasswordResetCode. It reuses the existing passwordResetCodeContent layout.


func renderLoginCode(code string, validFor time.Duration) (*EmailContent, error) {
	minutes := int(validFor.Round(time.Minute).Minutes())
	if minutes < 1 {
		minutes = 1
	}
	digits := make([]string, 0, len(code))
	for _, r := range code {
		digits = append(digits, string(r))
	}
	data := map[string]any{
		"Eyebrow": "Sign in",
		"Heading": "Your sign-in code",
		"Intro":   fmt.Sprintf("Hi,\nEnter this code to sign in to SendAGift. It expires in %d minutes. If you didn't ask for it, you can ignore this email.", minutes),
		"Digits":  digits,
		"Callout": emailCallout{Label: "Keep this code private", Body: "We will never ask you to read it out or forward this email."},
	}
	text := fmt.Sprintf("Hi,\n\nYour SendAGift sign-in code is %s.\n\nIt expires in %d minutes.\n\nThe SendAGift team", code, minutes)
	return renderEmail("", passwordResetCodeContent, "Your SendAGift sign-in code",
		"Enter this code to sign in.", data, text)
}
internal/services/email_service.go: add this after SendPasswordResetCode:


// SendLoginCode emails the 6-digit code a customer signs in or signs up with.
// Each send has its own dedupe key, so a resend is delivered.
func (s *EmailService) SendLoginCode(ctx context.Context, email, code string, validFor time.Duration) error {
	if s == nil {
		return nil
	}
	content, err := renderLoginCode(code, validFor)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("login_code:%s:%d", email, time.Now().UnixNano())
	return s.queue(ctx, "login_code", key, email, "", content)
}
Step 8: Login-code repository
New file internal/repository/login_code_repository.go


package repository
import (
	"context"
	"errors"
	"time"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)
var ErrLoginCodeNotFound = errors.New("login code not found")
// LoginCode is the live code for one email or phone and purpose. SendCount
// and Attempts count from WindowStart, so a resend brings no new guesses.
type LoginCode struct {
	CustomerID  *uuid.UUID
	CodeHash    string
	ExpiresAt   time.Time
	SentAt      time.Time
	WindowStart time.Time
	SendCount   int
	Attempts    int
}
type LoginCodeRepository struct {
	db *pgxpool.Pool
}
func NewLoginCodeRepository(db *pgxpool.Pool) *LoginCodeRepository {
	return &LoginCodeRepository{db: db}
}
func (r *LoginCodeRepository) Get(ctx context.Context, channel, destination, purpose string) (*LoginCode, error) {
	c := &LoginCode{}
	err := r.db.QueryRow(ctx, `
		select customer_id, code_hash, expires_at, sent_at, window_start, send_count, attempts
		from core.login_codes
		where channel = $1 and destination = $2 and purpose = $3`,
		channel, destination, purpose,
	).Scan(&c.CustomerID, &c.CodeHash, &c.ExpiresAt, &c.SentAt, &c.WindowStart, &c.SendCount, &c.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLoginCodeNotFound
	}
	return c, err
}
// Save replaces the live code. The day's counters carry over until the
// window is a day old.
func (r *LoginCodeRepository) Save(ctx context.Context, channel, destination, purpose string, customerID *uuid.UUID, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		insert into core.login_codes as lc (channel, destination, purpose, customer_id, code_hash, expires_at)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (channel, destination, purpose) do update set
		    customer_id  = excluded.customer_id,
		    code_hash    = excluded.code_hash,
		    expires_at   = excluded.expires_at,
		    sent_at      = now(),
		    window_start = case when lc.window_start < now() - interval '1 day' then now() else lc.window_start end,
		    send_count   = case when lc.window_start < now() - interval '1 day' then 1 else lc.send_count + 1 end,
		    attempts     = case when lc.window_start < now() - interval '1 day' then 0 else lc.attempts end`,
		channel, destination, purpose, customerID, hash, expiresAt)
	return err
}
func (r *LoginCodeRepository) AddAttempt(ctx context.Context, channel, destination, purpose string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		update core.login_codes set attempts = attempts + 1
		where channel = $1 and destination = $2 and purpose = $3
		returning attempts`, channel, destination, purpose).Scan(&n)
	return n, err
}
// Consume spends the code so it works once. False means another request
// spent it first.
func (r *LoginCodeRepository) Consume(ctx context.Context, channel, destination, purpose, hash string) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		update core.login_codes set code_hash = ''
		where channel = $1 and destination = $2 and purpose = $3
		  and code_hash = $4 and code_hash <> ''`, channel, destination, purpose, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
Step 9: Customer repository and model
internal/models/customer.go: add this to Customer after PasswordChangeRequired:


PhoneVerifiedAt *time.Time `json:"phone_verified_at,omitempty"`
internal/repository/customer_repository.go

In GetByID, add , phone_verified_at to the end of the select list and , &c.PhoneVerifiedAt to the end of Scan(...). The profile page then knows whether the phone is verified.
In Update, changing the phone must drop its verification. Add these two lines inside the set clause. Postgres reads the old row values for every expression in a SET, so their order doesn't matter.

phone_e164 = case when phone is distinct from $3 then null else phone_e164 end,
phone_verified_at = case when phone is distinct from $3 then null else phone_verified_at end,
Add three methods:

// CustomerIDByVerifiedPhone finds the live account that proved it holds
// this E.164 number.
func (r *CustomerRepository) CustomerIDByVerifiedPhone(ctx context.Context, phone string) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `
		select id::text from customer.customers
		where phone_e164 = $1 and phone_verified_at is not null
		  and deleted_at is null and status = 'active'`, phone).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCustomerNotFound
	}
	return id, err
}
// SetVerifiedPhone stores a number the customer proved they hold. One that
// another live account verified first is ErrCustomerDuplicate.
func (r *CustomerRepository) SetVerifiedPhone(ctx context.Context, id, display, e164 string) error {
	tag, err := r.db.Exec(ctx, `
		update customer.customers
		set phone = $2, phone_e164 = $3, phone_verified_at = now(), updated_at = now()
		where id = $1 and deleted_at is null`, id, display, e164)
	if err != nil {
		return mapCustomerWriteError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCustomerNotFound
	}
	return nil
}
func (r *CustomerRepository) MarkEmailVerified(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `
		update customer.customers
		set email_verified_at = coalesce(email_verified_at, now())
		where id = $1`, id)
	return err
}
Step 10: Login-code service
New file internal/services/login_code_service.go


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
	ChannelEmail = "email"
	ChannelSMS   = "sms"
	loginPurpose       = "login"
	verifyPhonePurpose = "verify_phone"
	loginCodeTTL        = 10 * time.Minute
	loginCodeCooldown   = time.Minute
	loginCodeDailySends = 5
	loginCodeDailyWrong = 10
	loginCodeWindow     = 24 * time.Hour
	codeSignupTTL = 20 * time.Minute
	codeSignupUse = "code_signup"
)
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
type LoginCodeService struct {
	codes     *repository.LoginCodeRepository
	customers *repository.CustomerRepository
	email     *EmailService
	sms       *SMSService
	defaultCC string
	jwtSecret string
	jwtExpiry time.Duration
}
func NewLoginCodeService(
	codes *repository.LoginCodeRepository,
	customers *repository.CustomerRepository,
	email *EmailService,
	sms *SMSService,
	defaultCountryCode, jwtSecret string,
	jwtExpiry time.Duration,
) *LoginCodeService {
	return &LoginCodeService{codes: codes, customers: customers, email: email, sms: sms,
		defaultCC: defaultCountryCode, jwtSecret: jwtSecret, jwtExpiry: jwtExpiry}
}
// CodeLoginResult is a signed-in customer, or someone new who proved they
// hold the destination and may sign up with it.
type CodeLoginResult struct {
	Status      string `json:"status"` // "signed_in" | "needs_signup"
	Token       string `json:"token,omitempty"`
	Role        string `json:"role,omitempty"`
	SignupToken string `json:"signup_token,omitempty"`
	Channel     string `json:"channel,omitempty"`
	Destination string `json:"destination,omitempty"`
}
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
	return s.issue(ctx, channel, dest, loginPurpose, nil)
}
// VerifyLoginCode checks the code and signs the customer in, or hands back
// a short-lived sign-up token when no account uses the destination.
func (s *LoginCodeService) VerifyLoginCode(ctx context.Context, channel, raw, code string) (*CodeLoginResult, error) {
	dest, err := s.normalize(channel, raw)
	if err != nil {
		return nil, ErrLoginCodeWrong
	}
	if err := s.check(ctx, channel, dest, loginPurpose, code, nil); err != nil {
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
	id, err := s.customers.CustomerIDByVerifiedPhone(ctx, dest)
	if err != nil {
		return nil, err
	}
	return s.customers.GetByID(ctx, id)
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
Step 11: Handler
New file internal/handlers/login_code_handler.go


package handlers
import (
	"encoding/json"
	"errors"
	"net/http"
	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)
type LoginCodeHandler struct {
	codes *services.LoginCodeService
}
func NewLoginCodeHandler(codes *services.LoginCodeService) *LoginCodeHandler {
	return &LoginCodeHandler{codes: codes}
}
type loginCodeRequest struct {
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
	Code        string `json:"code"`
}
type phoneCodeRequest struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}
// RequestCode handles POST /customers/login/code. The answer is the same
// whether or not an account exists.
func (h *LoginCodeHandler) RequestCode(w http.ResponseWriter, r *http.Request) {
	var req loginCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.codes.RequestLoginCode(r.Context(), req.Channel, req.Destination); err != nil {
		writeLoginCodeError(w, err, "could not send a code")
		return
	}
	utils.JSON(w, http.StatusAccepted, map[string]string{"message": "If that is yours, a code is on its way."})
}
// VerifyCode handles POST /customers/login/code/verify.
func (h *LoginCodeHandler) VerifyCode(w http.ResponseWriter, r *http.Request) {
	var req loginCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := h.codes.VerifyLoginCode(r.Context(), req.Channel, req.Destination, req.Code)
	if err != nil {
		writeLoginCodeError(w, err, "could not check the code")
		return
	}
	utils.JSON(w, http.StatusOK, res)
}
// RequestPhoneCode handles POST /customers/me/phone/code.
func (h *LoginCodeHandler) RequestPhoneCode(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req phoneCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.codes.RequestPhoneVerification(r.Context(), customerID, req.Phone); err != nil {
		writeLoginCodeError(w, err, "could not send a code")
		return
	}
	utils.JSON(w, http.StatusAccepted, map[string]string{"message": "Code sent."})
}
// VerifyPhone handles POST /customers/me/phone/verify.
func (h *LoginCodeHandler) VerifyPhone(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req phoneCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.codes.ConfirmPhoneVerification(r.Context(), customerID, req.Phone, req.Code); err != nil {
		writeLoginCodeError(w, err, "could not verify the phone")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "Phone verified."})
}
func writeLoginCodeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrLoginChannel), errors.Is(err, services.ErrInvalidPhone):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "enter a valid email")
	case errors.Is(err, services.ErrLoginCodeWait), errors.Is(err, services.ErrLoginCodeTooMany),
		errors.Is(err, services.ErrLoginCodeLocked):
		utils.Error(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, services.ErrLoginCodeWrong), errors.Is(err, services.ErrLoginCodeExpired):
		utils.Error(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrPhoneTaken):
		utils.Error(w, http.StatusConflict, err.Error())
	default:
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
Step 12: Routes
New file internal/routes/login_code_routes.go


package routes
import (
	"time"
	"github.com/go-chi/chi/v5"
	"myapp/internal/handlers"
	"myapp/internal/middleware"
)
// RegisterLoginCodeRoutes mounts customer sign-in by SMS or email code, and
// phone verification for a signed-in customer.
func RegisterLoginCodeRoutes(r chi.Router, codes *handlers.LoginCodeHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(10, time.Minute))
		r.Post("/customers/login/code", codes.RequestCode)
		r.Post("/customers/login/code/verify", codes.VerifyCode)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(10, time.Minute))
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Post("/customers/me/phone/code", codes.RequestPhoneCode)
		r.Post("/customers/me/phone/verify", codes.VerifyPhone)
	})
}
internal/routes/router.go

In New(...), add a parameter after social *handlers.SocialAuthHandler,:

// Handler instance that signs customers in with an SMS or email code
loginCodes *handlers.LoginCodeHandler,
Inside r.Route("/api/v1", ...), after RegisterAuthRoutes(...):

// Customer sign-in with a 6-digit code by SMS (textbee) or email
RegisterLoginCodeRoutes(r, loginCodes, jwtSecret)
internal/routes/router_smoke_test.go (line 11): add one more nil before "secret". There should now be 25 nils.

Step 13: Sign-up accepts the verified phone or email
internal/services/customer_service.go

Add a field to CustomerService:

codeSignups  *LoginCodeService // sign-ups proven by an SMS or email code
and this method next to SendEmailsWith:

// UseCodeSignups lets Register accept a sign-up token from a verified code.
func (s *CustomerService) UseCodeSignups(codes *LoginCodeService) { s.codeSignups = codes }
Add a field to CustomerRegisterInput:

SignupToken  string // from a verified sign-in code; marks the phone or email verified
In Register, right after the in.CustomerType defaulting and before the if in.Email == "" || ... check:

proven, provenPhone := "", ""
if strings.TrimSpace(in.SignupToken) != "" {
	if s.codeSignups == nil {
		return nil, ErrCodeSignupExpired
	}
	channel, dest, err := s.codeSignups.ReadSignupToken(in.SignupToken)
	if err != nil {
		return nil, err
	}
	switch channel {
	case ChannelEmail:
		if dest != in.Email {
			return nil, ErrCodeSignupExpired
		}
	case ChannelSMS:
		if _, err := s.customers.CustomerIDByVerifiedPhone(ctx, dest); err == nil {
			return nil, ErrPhoneTaken
		} else if !errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, err
		}
		in.Phone = &dest
		provenPhone = dest
	}
	proven = channel
}
Replace the gift-account takeover block (around line 174) with a plain conflict. This is part of the P0 fix.

if err := s.customers.Create(ctx, customer); err != nil {
	if errors.Is(err, repository.ErrCustomerDuplicate) {
		return nil, ErrCustomerConflict
	}
	return nil, err
}
switch proven {
case ChannelEmail:
	if err := s.customers.MarkEmailVerified(ctx, customer.ID.String()); err != nil {
		log.Printf("customer %s email verified: %v", customer.ID, err)
	}
case ChannelSMS:
	if err := s.customers.SetVerifiedPhone(ctx, customer.ID.String(), provenPhone, provenPhone); err != nil {
		log.Printf("customer %s verified phone: %v", customer.ID, err)
	}
}
internal/handlers/customer_handler.go

Add SignupToken string json:"signup_token"`` to customerRegisterRequest.
Pass SignupToken: req.SignupToken, into services.CustomerRegisterInput in Register.
Add to writeCustomerError, before the ErrCustomerConflict case:

case errors.Is(err, services.ErrCodeSignupExpired):
	utils.Error(w, http.StatusBadRequest, services.ErrCodeSignupExpired.Error())
case errors.Is(err, services.ErrPhoneTaken):
	utils.Error(w, http.StatusUnprocessableEntity, services.ErrPhoneTaken.Error())
Step 14: Wire it up in cmd/api/main.go
Add this right after the customerService.UsePasswordResets(passwordResets) line:


// SMS through textbee: queued like email and sent by this loop. Without
// a key the messages wait in the outbox until one is configured.
var smsSender services.SMSSender
if textbee := services.NewTextBeeSender(cfg.TextBeeAPIBase, cfg.TextBeeDeviceID, cfg.TextBeeAPIKey, cfg.TextBeeSIMSubscription); textbee != nil {
	smsSender = textbee
	fmt.Printf("📱 SMS: textbee device %s\n", cfg.TextBeeDeviceID)
} else {
	log.Printf("⚠️  TEXTBEE_API_KEY is not set: SMS are queued but not sent.")
}
smsService := services.NewSMSService(repository.NewSMSRepository(pool), smsSender)
go smsService.RunDeliveryLoop(context.Background(), 5*time.Second,
	database.Exclusive(pool, database.LockSMSDelivery))
// Customers sign in or sign up with a 6-digit code by SMS or email.
loginCodeService := services.NewLoginCodeService(repository.NewLoginCodeRepository(pool), customers,
	emailService, smsService, cfg.DefaultPhoneCountryCode, cfg.JWTSecret, cfg.JWTExpiry)
customerService.UseCodeSignups(loginCodeService)
loginCodeHandler := handlers.NewLoginCodeHandler(loginCodeService)
Then add loginCodeHandler to the routes.New(...) call, between socialAuthHandler and cfg.JWTSecret.

Step 15: Close the gift-account password hole
Email-code sign-in now gives gift recipients a safe way in, so the shared default password can go.

internal/services/auth_service.go


var ErrPasswordChangeRequired = errors.New("this account was made for a gift. Sign in with a code sent to your email, then choose a password")
In Login, in the customer branch:


if utils.CheckPassword(in.Password, customer.PasswordHash) {
	if customer.PasswordChangeRequired {
		return nil, ErrPasswordChangeRequired
	}
	return s.token(customer.ID.String(), customer.Email, "customer")
}
internal/handlers/auth_handler.go: in Login's switch:


case errors.Is(err, services.ErrPasswordChangeRequired):
	utils.Error(w, http.StatusForbidden, services.ErrPasswordChangeRequired.Error())
internal/services/gift_recipient_service.go

Delete the GiftRecipientDefaultPassword constant.
In ensureAccount, change the password hash line to:

// Nobody knows this password: the recipient signs in with an emailed code.
hash, err := utils.HashPassword(uuid.NewString())
In notifyDelivered, delete the tempPassword block and call s.email.SendGiftDelivered(ctx, summary, ""). Then read renderGiftDelivered and make sure that with an empty password the email tells the recipient to sign in with a code.
internal/services/gift_recipient_integration_test.go (line 69): drop the GiftRecipientDefaultPassword check and keep only if !recipient.PasswordChangeRequired {.

Existing gift recipients still on the old default password are now blocked from password login. They sign in by email code and can then set a password through the existing /customers/me/password/code flow.

Build check: go build ./... ; go test ./internal/services -run TestNormalizePhone ; go test ./internal/routes

Frontend (send-agift-frontend)
Step 16: API functions
src/api/auth.ts: append:


export type CodeChannel = 'sms' | 'email'
/** Signed in, or someone new who proved the phone/email and may sign up with it. */
export type CodeLoginResult =
  | { status: 'signed_in'; token: string; role: UserRole }
  | { status: 'needs_signup'; signup_token: string; channel: CodeChannel; destination: string }
export function requestLoginCode(body: { channel: CodeChannel; destination: string }) {
  return api<{ message: string }>('/customers/login/code', { method: 'POST', body, auth: false })
}
export function verifyLoginCode(body: { channel: CodeChannel; destination: string; code: string }) {
  return api<CodeLoginResult>('/customers/login/code/verify', { method: 'POST', body, auth: false })
}
export function requestPhoneCode(phone: string) {
  return api<{ message: string }>('/customers/me/phone/code', { method: 'POST', body: { phone } })
}
export function verifyPhoneCode(phone: string, code: string) {
  return api<{ message: string }>('/customers/me/phone/verify', { method: 'POST', body: { phone, code } })
}
src/api/customers.ts: add signup_token?: string to CustomerRegisterRequest.

src/api/types.ts: add phone_verified_at?: string to the Customer type.

Step 17: Code sign-in panel
New file src/features/auth/code-login-panel.tsx. It uses divs and buttons rather than a <form>, because it sits inside the login form and forms can't be nested.


import { useEffect, useState } from 'react'
import { LoaderCircle, Mail, Smartphone } from 'lucide-react'
import {
  requestLoginCode,
  verifyLoginCode,
  type CodeChannel,
  type CodeLoginResult,
} from '@/api/auth'
import { FormAlert } from '@/components/common/form-alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PhoneField } from '@/features/auth/phone-field'
import { getErrorMessage } from '@/lib/api'
const RESEND_SECONDS = 60
type CodeLoginPanelProps = {
  onResult: (result: CodeLoginResult) => void
}
export function CodeLoginPanel({ onResult }: CodeLoginPanelProps) {
  const [channel, setChannel] = useState<CodeChannel>('sms')
  const [destination, setDestination] = useState('')
  const [code, setCode] = useState('')
  const [sent, setSent] = useState(false)
  const [busy, setBusy] = useState(false)
  const [wait, setWait] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  useEffect(() => {
    if (wait <= 0) return
    const id = window.setTimeout(() => setWait((s) => s - 1), 1000)
    return () => window.clearTimeout(id)
  }, [wait])
  function switchChannel(next: CodeChannel) {
    setChannel(next)
    setDestination('')
    setCode('')
    setSent(false)
    setError(null)
    setNotice(null)
  }
  async function sendCode() {
    setError(null)
    const value = destination.trim()
    if (channel === 'email' && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)) return setError('Enter a valid email.')
    if (channel === 'sms' && value.replace(/\D/g, '').length < 7) return setError('Enter your phone number.')
    setBusy(true)
    try {
      const res = await requestLoginCode({ channel, destination: value })
      setSent(true)
      setNotice(res.message)
      setWait(RESEND_SECONDS)
    } catch (err) {
      setError(getErrorMessage(err, 'Could not send a code.'))
    } finally {
      setBusy(false)
    }
  }
  async function verify() {
    setError(null)
    if (!/^\d{6}$/.test(code.trim())) return setError('Enter the 6-digit code.')
    setBusy(true)
    try {
      onResult(await verifyLoginCode({ channel, destination: destination.trim(), code: code.trim() }))
    } catch (err) {
      setError(getErrorMessage(err, 'That code did not work.'))
      setBusy(false)
    }
  }
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-2">
        <Button type="button" variant={channel === 'sms' ? 'default' : 'outline'} onClick={() => switchChannel('sms')}>
          <Smartphone /> Phone
        </Button>
        <Button type="button" variant={channel === 'email' ? 'default' : 'outline'} onClick={() => switchChannel('email')}>
          <Mail /> Email
        </Button>
      </div>
      <div className="space-y-2">
        <Label htmlFor="code-destination">{channel === 'sms' ? 'Phone number' : 'Email'}</Label>
        {channel === 'sms' ? (
          <PhoneField id="code-destination" value={destination} onChange={setDestination} disabled={sent} />
        ) : (
          <Input
            id="code-destination"
            type="email"
            autoComplete="email"
            placeholder="you@example.com"
            value={destination}
            onChange={(e) => setDestination(e.target.value)}
            className="h-12 bg-surface"
            disabled={sent}
          />
        )}
      </div>
      {sent ? (
        <div className="space-y-2">
          <Label htmlFor="login-code">6-digit code</Label>
          <Input
            id="login-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
            placeholder="123456"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
            className="h-12 bg-surface text-center text-lg tracking-[0.5em]"
          />
          <div className="flex justify-between text-xs">
            <button type="button" className="text-muted-foreground hover:text-foreground" onClick={() => switchChannel(channel)}>
              Change {channel === 'sms' ? 'number' : 'email'}
            </button>
            <button
              type="button"
              className="font-medium text-primary disabled:text-muted-foreground"
              disabled={wait > 0 || busy}
              onClick={sendCode}
            >
              {wait > 0 ? `Resend in ${wait}s` : 'Resend code'}
            </button>
          </div>
        </div>
      ) : null}
      <FormAlert error={error} notice={notice} />
      <Button
        type="button"
        size="lg"
        disabled={busy}
        onClick={sent ? verify : sendCode}
        className="h-12 w-full rounded-full text-sm font-semibold"
      >
        {busy ? <LoaderCircle className="animate-spin" /> : null}
        {sent ? 'Sign in' : 'Send code'}
      </Button>
    </div>
  )
}
Step 18: Add a code option to the login page
src/features/auth/login-form.tsx

Import the panel:

import { CodeLoginPanel } from '@/features/auth/code-login-panel'
Add state next to the others:

const [mode, setMode] = useState<'password' | 'code'>('password')
Make the first line of handleSubmit, after event.preventDefault():

if (mode === 'code') return
In the JSX, right after the heading <div className="space-y-3">…</div>, add the toggle (customers only):

{role === 'customer' ? (
  <div className="grid grid-cols-2 gap-1 rounded-full bg-muted p-1 text-sm font-medium">
    {(['password', 'code'] as const).map((m) => (
      <button
        key={m}
        type="button"
        onClick={() => { setMode(m); setError(null) }}
        className={`rounded-full py-2 transition-colors ${mode === m ? 'bg-background shadow-sm' : 'text-muted-foreground'}`}
      >
        {m === 'password' ? 'Password' : 'One-time code'}
      </button>
    ))}
  </div>
) : null}
Wrap the email and password fields (the inner <div className="space-y-2"> blocks for email and password, but not the "Keep me signed in" checkbox), plus <FormAlert …/> and the submit <Button>, in a condition:

{mode === 'code' ? (
  <CodeLoginPanel
    onResult={(result) => {
      if (result.status === 'signed_in') login(result.token, 'customer', remember)
      else navigate('/register', { state: { codeSignup: result } })
    }}
  />
) : (
  <>
    {/* existing email field, password field, FormAlert and submit Button */}
  </>
)}
Keep the "Keep me signed in" checkbox outside the condition so it applies to both modes.
Step 19: Register page accepts the verified phone or email
src/features/auth/customer-register-form.tsx

Imports and a helper next to socialFromState:

import type { CodeLoginResult } from '@/api/auth'
type CodeSignup = Extract<CodeLoginResult, { status: 'needs_signup' }>
/** A phone or email proven by a sign-in code on the login page, if any. */
function codeSignupFromState(state: unknown): CodeSignup | null {
  const codeSignup = (state as { codeSignup?: CodeLoginResult } | null)?.codeSignup
  return codeSignup?.status === 'needs_signup' ? codeSignup : null
}
Replace lines 74 and 76 (useState('') for email and phone):

const [codeSignup] = useState(() => codeSignupFromState(location.state))
const [email, setEmail] = useState(() => (codeSignup?.channel === 'email' ? codeSignup.destination : ''))
const [phone, setPhone] = useState(() => (codeSignup?.channel === 'sms' ? codeSignup.destination : ''))
PhoneField already splits +94771234567 into the +94 dial code and the number.
Lock the proven field. On the email <Input> add disabled={codeSignup?.channel === 'email'}, and on <PhoneField> add disabled={codeSignup?.channel === 'sms'}. Optionally show a small "✓ Verified" note under the locked field.
In registerCustomer({...}) (around line 152), add:

signup_token: codeSignup?.signup_token,
The existing loginCustomer call after registration still works, because the user also sets a password.
Step 20: "Verify phone" on the profile page
New file src/features/account/verify-phone-card.tsx


import { useState } from 'react'
import { BadgeCheck, LoaderCircle } from 'lucide-react'
import { requestPhoneCode, verifyPhoneCode } from '@/api/auth'
import { FormAlert } from '@/components/common/form-alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { getErrorMessage } from '@/lib/api'
type VerifyPhoneCardProps = {
  phone: string
  verified: boolean
  onVerified: () => void
}
export function VerifyPhoneCard({ phone, verified, onVerified }: VerifyPhoneCardProps) {
  const [sent, setSent] = useState(false)
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  if (verified) {
    return (
      <p className="flex items-center gap-1.5 text-sm font-medium text-emerald-600">
        <BadgeCheck className="size-4" /> Phone verified. You can sign in with it.
      </p>
    )
  }
  if (!phone.trim()) return null
  async function send() {
    setError(null)
    setBusy(true)
    try {
      await requestPhoneCode(phone)
      setSent(true)
    } catch (err) {
      setError(getErrorMessage(err, 'Could not send a code.'))
    } finally {
      setBusy(false)
    }
  }
  async function confirm() {
    setError(null)
    setBusy(true)
    try {
      await verifyPhoneCode(phone, code)
      onVerified()
    } catch (err) {
      setError(getErrorMessage(err, 'That code did not work.'))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="space-y-2 rounded-lg border p-3">
      <p className="text-sm text-muted-foreground">Verify this number to sign in with a text code.</p>
      {sent ? (
        <div className="flex gap-2">
          <Input
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
            placeholder="6-digit code"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
          />
          <Button type="button" onClick={confirm} disabled={busy || code.length !== 6}>
            {busy ? <LoaderCircle className="animate-spin" /> : 'Verify'}
          </Button>
        </div>
      ) : (
        <Button type="button" variant="outline" onClick={send} disabled={busy}>
          {busy ? <LoaderCircle className="animate-spin" /> : 'Send code'}
        </Button>
      )}
      <FormAlert error={error} notice={null} />
    </div>
  )
}
In src/pages/customer-profile-page.tsx, render it under the phone field, using the saved phone rather than the one being edited:


<VerifyPhoneCard
  phone={profile.phone ?? ''}
  verified={Boolean(profile.phone_verified_at)}
  onVerified={reloadProfile}
/>
Use the profile variable and reload function that page already has; I haven't read that file, so the names above are placeholders.

Build check: npm run build ; npm run lint

Step 21: Test it end to end
Start the API with go run ./cmd/api. The migration runs automatically. Then in PowerShell:


$api = "http://localhost:8080/api/v1"
# 1. Ask for an SMS code (real number on your textbee phone's network)
Invoke-RestMethod -Method Post "$api/customers/login/code" -ContentType 'application/json' `
  -Body '{"channel":"sms","destination":"0771234567"}'
# 2. Verify it. A new number returns status needs_signup + signup_token
Invoke-RestMethod -Method Post "$api/customers/login/code/verify" -ContentType 'application/json' `
  -Body '{"channel":"sms","destination":"0771234567","code":"123456"}'
If textbee isn't configured, the message waits in the outbox, and you can read the code there with select body from core.sms_outbox order by created_at desc limit 1;.

Check each of these:

Check	Expected
Email code for an existing customer
signed_in with a token
SMS code for a number nobody has verified
needs_signup; register page opens with the phone locked
An existing customer's unverified signup phone
needs_signup, not a sign-in to that account
Profile, then Verify phone, then sign out and use a phone code
Signs in to that account
A second account verifying the same number
409 "already verified on another account"
Resend within 60 seconds
429 "wait a minute"
6th code in a day to the same destination
429 "too many codes"
Wrong code 10 times
429 locked; resending doesn't unlock it
Same correct code used twice
Second attempt gets "expired"
Changing the phone on the profile
Verified badge disappears
Gift-created account with the old default password
Password login gets 403; email code signs in
Normal email and password login
Still works
Google or Facebook sign-in
Still works
Two things to know about textbee:

Messages go out from your Android phone's SIM, so carrier limits apply and it can be slower than a paid SMS provider. The 5-per-day and 60-second limits keep costs down.
Rows in sms_outbox (and email_outbox) hold the code as plain text, the same way emailed codes already work. Once codes expire, those rows are safe to delete.
When this is working, the next part of the free-gift plan is the one-time sender and recipient limits, which can key on the verified phone or email this adds.



Agent