package services

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
)

func TestNormalizeVerificationStatus(t *testing.T) {
	got, err := normalizeVerificationStatus(" Verified ")
	if err != nil || got != "verified" {
		t.Fatalf("verified: %q %v", got, err)
	}
	if _, err := normalizeVerificationStatus("pending"); err == nil {
		t.Fatal("pending should be rejected")
	}
}

func TestClampAdminPage(t *testing.T) {
	limit, offset := clampAdminPage(0, -4)
	if limit != 50 || offset != 0 {
		t.Fatalf("defaults: got %d %d", limit, offset)
	}
	limit, offset = clampAdminPage(500, 10)
	if limit != 100 || offset != 10 {
		t.Fatalf("cap: got %d %d", limit, offset)
	}
}

func TestSellerSessionAllowed(t *testing.T) {
	verified := time.Now()
	active := &models.Seller{ID: uuid.New(), Status: "active", EmailVerifiedAt: &verified}
	if err := sellerSessionAllowed(active); err != nil {
		t.Fatal(err)
	}

	unconfirmed := &models.Seller{Status: "active"}
	if !errors.Is(sellerSessionAllowed(unconfirmed), ErrSellerEmailUnconfirmed) {
		t.Fatalf("unconfirmed: %v", sellerSessionAllowed(unconfirmed))
	}

	suspended := &models.Seller{Status: "suspended", EmailVerifiedAt: &verified}
	if !errors.Is(sellerSessionAllowed(suspended), ErrSellerSuspended) {
		t.Fatalf("suspended: %v", sellerSessionAllowed(suspended))
	}

	closed := &models.Seller{Status: "deleted", EmailVerifiedAt: &verified}
	if !errors.Is(sellerSessionAllowed(closed), ErrInvalidCredentials) {
		t.Fatalf("closed: %v", sellerSessionAllowed(closed))
	}
}
