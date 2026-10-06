package services

import (
	"strings"
	"testing"
	"time"
)

func TestNewEmailCode(t *testing.T) {
	for i := 0; i < 20; i++ {
		code, err := newEmailCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != emailCodeLength {
			t.Fatalf("code %q has length %d", code, len(code))
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Fatalf("code %q is not digits", code)
			}
		}
	}
}

func TestEmailCodeBlock(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	hash := "hash"
	fresh := now.Add(time.Minute)
	stale := now.Add(-time.Second)

	if err := emailCodeBlock(true, &hash, &fresh, 0, now); err != ErrSellerEmailAlreadyVerified {
		t.Fatalf("verified: %v", err)
	}
	if err := emailCodeBlock(false, nil, &fresh, 0, now); err != ErrSellerEmailCodeExpired {
		t.Fatalf("missing hash: %v", err)
	}
	if err := emailCodeBlock(false, &hash, &stale, 0, now); err != ErrSellerEmailCodeExpired {
		t.Fatalf("expired: %v", err)
	}
	if err := emailCodeBlock(false, &hash, &fresh, emailCodeMaxAttempts, now); err != ErrSellerEmailCodeLocked {
		t.Fatalf("locked: %v", err)
	}
	if err := emailCodeBlock(false, &hash, &fresh, emailCodeMaxAttempts-1, now); err != nil {
		t.Fatalf("usable code: %v", err)
	}
}

func TestEmailCodeCooldown(t *testing.T) {
	now := time.Now()
	recent := now.Add(-30 * time.Second)
	old := now.Add(-2 * time.Minute)
	if !emailCodeCoolingDown(&recent, now) {
		t.Fatal("a code sent 30s ago should still be cooling down")
	}
	if emailCodeCoolingDown(&old, now) {
		t.Fatal("a code sent 2 minutes ago can be sent again")
	}
	if emailCodeCoolingDown(nil, now) {
		t.Fatal("no previous code is not cooling down")
	}
}

func TestRenderSellerEmailCode(t *testing.T) {
	content, err := renderSellerEmailCode("Amara", "048213", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content.Text, "048213") {
		t.Fatalf("plain text missing the code: %s", content.Text)
	}
	if !strings.Contains(content.HTML, ">0<") || !strings.Contains(content.HTML, ">4<") {
		t.Fatal("html should show each digit")
	}
	if strings.Contains(content.Subject, "048213") {
		t.Fatal("the subject should not contain the code")
	}
}
