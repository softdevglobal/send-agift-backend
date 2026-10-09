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
	if s == nil {
		return nil
	}
	body := fmt.Sprintf("%s is your SendAGift code. It expires in %d minutes. Never share it.",
		code, int(validFor.Minutes()))
	key := fmt.Sprintf("login_code:%s:%d", phone, time.Now().UnixNano())
	return s.queue(ctx, "login_code", key, phone, body)
}

// SendGiftDelivered texts a recipient that their gift arrived, with a link
// that signs them in by code so they can review it.
func (s *SMSService) SendGiftDelivered(ctx context.Context, orderID, phone, senderName, link string) error {
	if s == nil {
		return nil
	}
	body := fmt.Sprintf("%s sent you a gift on SendAGift and it has been delivered! Review it here: %s",
		senderName, link)
	return s.queue(ctx, "gift_delivered", "gift_delivered:"+orderID+":"+phone, phone, body)
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
