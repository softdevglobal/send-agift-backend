package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"myapp/internal/models"
)

// ErrPushTokenInvalid means the device token will never work again (the app
// was uninstalled or the token rotated), so it should be forgotten.
var ErrPushTokenInvalid = errors.New("push token is no longer valid")

// PushSender delivers one notification to one device.
type PushSender interface {
	Send(ctx context.Context, token string, msg models.PushMessage) error
}

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// FCMSender sends through the Firebase Cloud Messaging HTTP v1 API. The
// message carries a notification block, so Android and iOS show it
// themselves even when the app is closed.
type FCMSender struct {
	projectID string
	tokens    oauth2.TokenSource
	http      *http.Client
}

// NewFCMSender reads a Firebase service-account key from a file path or from
// the JSON itself. It returns nil, nil when neither is set: push is simply
// not configured yet.
func NewFCMSender(ctx context.Context, credentialsFile, credentialsJSON string) (*FCMSender, error) {
	raw := []byte(strings.TrimSpace(credentialsJSON))
	if len(raw) == 0 && credentialsFile != "" {
		b, err := os.ReadFile(credentialsFile)
		if err != nil {
			return nil, fmt.Errorf("read FIREBASE_CREDENTIALS_FILE: %w", err)
		}
		raw = b
	}
	if len(raw) == 0 {
		return nil, nil
	}
	creds, err := google.CredentialsFromJSON(ctx, raw, fcmScope)
	if err != nil {
		return nil, fmt.Errorf("firebase credentials: %w", err)
	}
	if creds.ProjectID == "" {
		return nil, errors.New("firebase credentials have no project_id")
	}
	return &FCMSender{
		projectID: creds.ProjectID,
		tokens:    creds.TokenSource,
		http:      &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type fcmRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data,omitempty"`
	Android      map[string]any    `json:"android"`
	APNS         map[string]any    `json:"apns"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// AndroidChannelCompetitions must match the channel the app creates.
const AndroidChannelCompetitions = "competitions"

func (s *FCMSender) Send(ctx context.Context, token string, msg models.PushMessage) error {
	body, err := json.Marshal(fcmRequest{Message: fcmMessage{
		Token:        token,
		Notification: fcmNotification{Title: msg.Title, Body: msg.Body},
		Data:         msg.Data,
		Android: map[string]any{
			"priority": "high",
			"notification": map[string]any{
				"channel_id": AndroidChannelCompetitions,
				"sound":      "default",
			},
		},
		APNS: map[string]any{
			"headers": map[string]string{"apns-priority": "10"},
			"payload": map[string]any{"aps": map[string]any{"sound": "default"}},
		},
	}})
	if err != nil {
		return err
	}
	tok, err := s.tokens.Token()
	if err != nil {
		return fmt.Errorf("firebase auth: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://fcm.googleapis.com/v1/projects/"+s.projectID+"/messages:send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	tok.SetAuthHeader(req)

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	// A token Firebase no longer knows comes back as 404 UNREGISTERED, or as
	// 400 INVALID_ARGUMENT naming the token.
	if resp.StatusCode == http.StatusNotFound || strings.Contains(string(detail), "UNREGISTERED") ||
		(resp.StatusCode == http.StatusBadRequest && strings.Contains(string(detail), "registration token")) {
		return ErrPushTokenInvalid
	}
	return fmt.Errorf("fcm %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
}
