package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
	"myapp/internal/utils"
)

func TestPasswordCodeBlock(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(time.Minute)
	if err := passwordCodeBlock("", fresh, 0, now); err != ErrPasswordCodeExpired {
		t.Fatalf("empty hash: %v", err)
	}
	if err := passwordCodeBlock("hash", now.Add(-time.Second), 0, now); err != ErrPasswordCodeExpired {
		t.Fatalf("expired: %v", err)
	}
	if err := passwordCodeBlock("hash", fresh, emailCodeMaxAttempts, now); err != ErrPasswordCodeLocked {
		t.Fatalf("locked: %v", err)
	}
	if err := passwordCodeBlock("hash", fresh, 0, now); err != nil {
		t.Fatalf("usable: %v", err)
	}
}

func TestAcceptableNewPassword(t *testing.T) {
	hash, err := utils.HashPassword("current-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := acceptableNewPassword("short", hash); err != ErrInvalidInput {
		t.Fatalf("short: %v", err)
	}
	if err := acceptableNewPassword("00001111", hash, "00001111"); err != ErrInvalidInput {
		t.Fatalf("banned: %v", err)
	}
	if err := acceptableNewPassword("current-password", hash); err != ErrPasswordUnchanged {
		t.Fatalf("same: %v", err)
	}
	if err := acceptableNewPassword("a-new-password", hash, "00001111"); err != nil {
		t.Fatalf("accepted: %v", err)
	}
}

func TestRenderPasswordResetCode(t *testing.T) {
	content, err := renderPasswordResetCode("Amara", "048213", "forgot", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content.Text, "048213") || strings.Contains(content.Subject, "048213") {
		t.Fatalf("code should be in the body only: subject %q", content.Subject)
	}
	profile, err := renderPasswordResetCode("Amara", "048213", "profile", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile.HTML, "Confirm your new password") {
		t.Fatal("profile email should describe a password change")
	}
}

type memCodes struct {
	rows map[string]*repository.PasswordResetCode
}

func (m *memCodes) key(kind string, id uuid.UUID, purpose string) string {
	return kind + "|" + id.String() + "|" + purpose
}

func (m *memCodes) Save(_ context.Context, kind string, id uuid.UUID, purpose, hash string, expires time.Time) error {
	if m.rows == nil {
		m.rows = map[string]*repository.PasswordResetCode{}
	}
	m.rows[m.key(kind, id, purpose)] = &repository.PasswordResetCode{
		Hash: hash, ExpiresAt: expires, SentAt: time.Now().UTC(), Attempts: 0,
	}
	return nil
}

func (m *memCodes) Get(_ context.Context, kind string, id uuid.UUID, purpose string) (*repository.PasswordResetCode, error) {
	row := m.rows[m.key(kind, id, purpose)]
	if row == nil {
		return nil, repository.ErrPasswordResetNotFound
	}
	copy := *row
	return &copy, nil
}

func (m *memCodes) AddAttempt(_ context.Context, kind string, id uuid.UUID, purpose string) (int, error) {
	row := m.rows[m.key(kind, id, purpose)]
	if row == nil {
		return 0, repository.ErrPasswordResetNotFound
	}
	row.Attempts++
	return row.Attempts, nil
}

func (m *memCodes) Delete(_ context.Context, kind string, id uuid.UUID, purpose string) error {
	delete(m.rows, m.key(kind, id, purpose))
	return nil
}

func TestPasswordCodeFlow(t *testing.T) {
	previous := newPasswordCode
	newPasswordCode = func() (string, error) { return "123456", nil }
	t.Cleanup(func() { newPasswordCode = previous })

	store := &memCodes{}
	account := passwordAccount{Type: "customer", ID: uuid.New(), Email: "a@example.com", Name: "Ada"}
	ctx := context.Background()

	if err := sendPasswordCode(ctx, store, nil, account, "forgot"); err != nil {
		t.Fatal(err)
	}
	if err := sendPasswordCode(ctx, store, nil, account, "forgot"); err != ErrPasswordCodeWait {
		t.Fatalf("cooldown: %v", err)
	}
	if err := checkPasswordCode(ctx, store, account, "forgot", "000000"); err != ErrPasswordCodeWrong {
		t.Fatalf("wrong code: %v", err)
	}
	if err := checkPasswordCode(ctx, store, account, "forgot", "123456"); err != nil {
		t.Fatalf("right code: %v", err)
	}
	if err := store.Delete(ctx, account.Type, account.ID, "forgot"); err != nil {
		t.Fatal(err)
	}
	if err := checkPasswordCode(ctx, store, account, "forgot", "123456"); err != ErrPasswordCodeWrong {
		t.Fatalf("used code: %v", err)
	}
}

func TestPasswordCodeLocksAfterFiveTries(t *testing.T) {
	previous := newPasswordCode
	newPasswordCode = func() (string, error) { return "123456", nil }
	t.Cleanup(func() { newPasswordCode = previous })

	store := &memCodes{}
	account := passwordAccount{Type: "seller", ID: uuid.New(), Email: "s@example.com", Name: "Sam"}
	ctx := context.Background()
	if err := sendPasswordCode(ctx, store, nil, account, "profile"); err != nil {
		t.Fatal(err)
	}
	var last error
	for i := 0; i < emailCodeMaxAttempts; i++ {
		last = checkPasswordCode(ctx, store, account, "profile", "000000")
	}
	if last != ErrPasswordCodeLocked {
		t.Fatalf("after five tries: %v", last)
	}
}
