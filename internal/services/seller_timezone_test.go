package services

import "testing"

func TestResolveShopTimezone(t *testing.T) {
	zone, err := resolveShopTimezone("", "Asia/Colombo")
	if err != nil || zone != "Asia/Colombo" {
		t.Fatalf("blank zone should use the country default, got %q %v", zone, err)
	}
	zone, err = resolveShopTimezone("America/New_York", "Asia/Colombo")
	if err != nil || zone != "America/New_York" {
		t.Fatalf("explicit zone should win, got %q %v", zone, err)
	}
	if _, err := resolveShopTimezone("Not/AZone", "Asia/Colombo"); err == nil {
		t.Fatal("invalid IANA name should be rejected")
	}
	zone, err = resolveShopTimezone("  ", "")
	if err != nil || zone != "UTC" {
		t.Fatalf("missing country default should fall back to UTC, got %q %v", zone, err)
	}
}
