package services

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFreeGiftKeysAreStableAndNormalised(t *testing.T) {
	const secret = "test-secret-test-secret-test-secret"
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	a := sendKeys(secret, id, " Sender@Example.com ", "+61412345678")
	b := sendKeys(secret, id, "sender@example.com", "+61412345678")
	if len(a) != 3 {
		t.Fatalf("send keys = %v, want customer, phone and email", a)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("key %d differs after trimming and lower-casing the email: %q vs %q", i, a[i], b[i])
		}
	}
	if a[0] != "customer:"+id.String() {
		t.Fatalf("customer key = %q", a[0])
	}
	if !strings.HasPrefix(a[1], "phone:") || !strings.HasPrefix(a[2], "email:") {
		t.Fatalf("send keys = %v, want phone then email", a)
	}
}

func TestFreeGiftKeysHoldNoContactDetails(t *testing.T) {
	const secret = "test-secret-test-secret-test-secret"
	for _, k := range receiveKeys(secret, "rita@example.com", "+61412345678") {
		if strings.Contains(k, "rita") || strings.Contains(k, "61412345678") {
			t.Fatalf("key %q holds the raw value", k)
		}
	}
}

func TestFreeGiftKeysDependOnSecretAndKind(t *testing.T) {
	a := hmacKey("secret-one", "email", "rita@example.com")
	if a == hmacKey("secret-two", "email", "rita@example.com") {
		t.Fatal("a different secret gave the same key")
	}
	// A phone and an email that happen to be the same text never clash.
	if strings.TrimPrefix(a, "email:") == strings.TrimPrefix(hmacKey("secret-one", "phone", "rita@example.com"), "phone:") {
		t.Fatal("the kind is not part of the hash")
	}
}

// Before the phone is verified the status check still runs, on the keys it has.
func TestFreeGiftSendKeysSkipMissingPhone(t *testing.T) {
	keys := sendKeys("s", uuid.New(), "a@example.com", "")
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want customer and email only", keys)
	}
}

// The same recipient saved twice, typed differently, is one recipient.
func TestFreeGiftReceiveKeysMatchAcrossSpellings(t *testing.T) {
	a := receiveKeys("s", "Rita@Example.com", "+61412345678")
	b := receiveKeys("s", " rita@example.com", "+61412345678 ")
	if a[0] != b[0] || a[1] != b[1] {
		t.Fatalf("keys differ: %v vs %v", a, b)
	}
}
