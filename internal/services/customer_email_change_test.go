package services

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestSuspiciousEmailChange(t *testing.T) {
	now := time.Now()
	old := now.Add(-90 * 24 * time.Hour)

	if flagged, _ := suspiciousEmailChange(old, 0, now); flagged {
		t.Fatal("an old account's first change should not be flagged")
	}
	if flagged, reason := suspiciousEmailChange(old, 1, now); !flagged || reason == "" {
		t.Fatalf("a second change in 30 days should be flagged with a reason, got %v %q", flagged, reason)
	}
	if flagged, _ := suspiciousEmailChange(now.Add(-24*time.Hour), 0, now); !flagged {
		t.Fatal("a change on a day-old account should be flagged")
	}
}

func TestNewUndoTokenStoresOnlyItsHash(t *testing.T) {
	token, hash, err := newUndoToken()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatal("hash must be the SHA-256 of the token the customer is emailed")
	}
	if hash == token || len(token) != 64 {
		t.Fatalf("token must be 32 random bytes in hex and differ from its hash, got len %d", len(token))
	}
	other, _, _ := newUndoToken()
	if other == token {
		t.Fatal("two tokens must differ")
	}
}

func TestEmailChangeEmailsRender(t *testing.T) {
	code, err := renderEmailChangeCode("123456", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(code.Text, "123456") || !strings.Contains(code.Text, "15 minutes") {
		t.Fatalf("code email text: %q", code.Text)
	}

	const undo = "https://app.example/account/email/undo?token=abc"
	alert, err := renderEmailChangedAlert("new@example.com", undo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(alert.Text, undo) || !strings.Contains(alert.HTML, "new@example.com") {
		t.Fatal("alert must name the new address and carry the undo link")
	}
	if !strings.Contains(alert.HTML, "token=abc") {
		t.Fatal("alert HTML must carry the undo link")
	}

	reverted, err := renderEmailChangeReverted("https://app.example", "/forgot-password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reverted.Text, "/forgot-password") {
		t.Fatalf("reverted email text: %q", reverted.Text)
	}
}

func TestEmailChangeLinksPointAtTheRightApp(t *testing.T) {
	const customer, seller = customerUndoPath, sellerUndoPath
	if customer == seller || !strings.HasSuffix(customer, "/email/undo") || !strings.HasSuffix(seller, "/email/undo") {
		t.Fatalf("undo pages must differ per app and end in /email/undo: %q %q", customer, seller)
	}

	mail, err := renderEmailChangeReverted("https://app.example", "/seller/forgot-password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mail.Text, "https://app.example/seller/forgot-password") {
		t.Fatalf("the reset link must use the path it was given: %q", mail.Text)
	}
}

func TestSilentRejectCodeSucceeds(t *testing.T) {
	if err := silentRejectCode(); err != nil {
		t.Fatal(err)
	}
}
