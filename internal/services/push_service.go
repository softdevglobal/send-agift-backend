package services

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
)

// ErrInvalidPushDevice is a device registration with a missing token or an
// unknown platform.
var ErrInvalidPushDevice = errors.New("token is required and platform must be android or ios")

// maxPushAttempts is how many times a notification is tried before it is
// given up on.
const maxPushAttempts = 5

// PushService registers app installs and delivers queued push notifications.
type PushService struct {
	repo   *repository.PushRepository
	sender PushSender
}

// NewPushService builds the service. sender may be nil while push is not
// configured: devices still register, and notifications wait in the queue.
func NewPushService(repo *repository.PushRepository, sender PushSender) *PushService {
	return &PushService{repo: repo, sender: sender}
}

// Enabled reports whether notifications can actually be sent.
func (s *PushService) Enabled() bool { return s.sender != nil }

// PushDeviceInput is what the app sends after the customer signs in, and
// whenever Firebase gives it a new token.
type PushDeviceInput struct {
	Token      string  `json:"token"`
	Platform   string  `json:"platform"`
	AppVersion *string `json:"app_version"`
}

func (s *PushService) RegisterDevice(ctx context.Context, customerID uuid.UUID, in PushDeviceInput) error {
	token := strings.TrimSpace(in.Token)
	platform := strings.ToLower(strings.TrimSpace(in.Platform))
	if token == "" || len(token) > 4096 || (platform != "android" && platform != "ios") {
		return ErrInvalidPushDevice
	}
	return s.repo.UpsertDevice(ctx, customerID, token, platform, in.AppVersion)
}

// UnregisterDevice stops notifications to an install, on sign-out.
func (s *PushService) UnregisterDevice(ctx context.Context, customerID uuid.UUID, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrInvalidPushDevice
	}
	return s.repo.DeleteDevice(ctx, customerID, token)
}

// RunDeliveryLoop sends due notifications on a timer until ctx ends. With
// several API servers, only the one holding the lock sends.
func (s *PushService) RunDeliveryLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := exclusive.run(ctx, func(ctx context.Context) error {
				for {
					n, err := s.DeliverDue(ctx, 200)
					if err != nil || n < 200 {
						return err
					}
				}
			}); err != nil {
				log.Printf("push delivery: %v", err)
			}
		}
	}
}

// DeliverDue sends up to limit due notifications and returns how many it
// handled. Each goes to every device of its customer; it counts as sent once
// any device takes it.
func (s *PushService) DeliverDue(ctx context.Context, limit int) (int, error) {
	if s.sender == nil {
		return 0, nil
	}
	due, err := s.repo.DuePushes(ctx, limit)
	if err != nil {
		return 0, err
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		dead []string
		sem  = make(chan struct{}, 8)
	)
	for _, p := range due {
		if p.Stale || len(p.Tokens) == 0 {
			reason := "no device registered"
			if p.Stale {
				reason = "competition no longer open"
			}
			if err := s.repo.MarkPush(ctx, p.ID, "skipped", &reason, nil); err != nil {
				return 0, err
			}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p repository.PendingPush) {
			defer wg.Done()
			defer func() { <-sem }()

			var delivered bool
			var lastErr error
			for _, token := range p.Tokens {
				err := s.sender.Send(ctx, token, p.Message)
				switch {
				case err == nil:
					delivered = true
				case errors.Is(err, ErrPushTokenInvalid):
					mu.Lock()
					dead = append(dead, token)
					mu.Unlock()
				default:
					lastErr = err
				}
			}

			var markErr error
			switch {
			case delivered:
				markErr = s.repo.MarkPush(ctx, p.ID, "sent", nil, nil)
			case lastErr == nil:
				reason := "every device token was invalid"
				markErr = s.repo.MarkPush(ctx, p.ID, "skipped", &reason, nil)
			case p.Attempts+1 >= maxPushAttempts:
				msg := lastErr.Error()
				markErr = s.repo.MarkPush(ctx, p.ID, "failed", &msg, nil)
			default:
				// Back off: 1, 2, 4, 8 minutes.
				msg := lastErr.Error()
				retry := time.Now().Add(time.Minute << p.Attempts)
				markErr = s.repo.MarkPush(ctx, p.ID, "pending", &msg, &retry)
			}
			if markErr != nil {
				log.Printf("push delivery: mark %s: %v", p.ID, markErr)
			}
		}(p)
	}
	wg.Wait()

	if err := s.repo.DeleteTokens(ctx, dead); err != nil {
		return len(due), err
	}
	return len(due), nil
}
