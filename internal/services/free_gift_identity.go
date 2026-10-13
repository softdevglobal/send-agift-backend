package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/google/uuid"
)

// The one-time limits are keyed on who a person is, not on an account row: a
// second account on the same phone, or the same recipient saved twice under
// another name, has to clash with the first. Emails and phones are stored as
// a keyed HMAC, so the claims table holds no contact details.

// hmacKey is "<kind>:<hex hmac>" of a normalised email or E.164 phone.
func hmacKey(secret, kind, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(kind + ":" + value))
	return kind + ":" + hex.EncodeToString(mac.Sum(nil))
}

// sendKeys are the rows a sender's free gift writes: the account, the
// verified phone and the verified email. Empty values are left out, so a
// status check can run before the phone is verified.
func sendKeys(secret string, customerID uuid.UUID, email, phoneE164 string) []string {
	keys := []string{"customer:" + customerID.String()}
	if phoneE164 = strings.TrimSpace(phoneE164); phoneE164 != "" {
		keys = append(keys, hmacKey(secret, "phone", phoneE164))
	}
	if email = normalizeEmail(email); email != "" {
		keys = append(keys, hmacKey(secret, "email", email))
	}
	return keys
}

// receiveKeys are the rows a recipient's free gift writes: their phone and
// their email, as the sender typed them at the moment of sending.
func receiveKeys(secret, email, phoneE164 string) []string {
	return []string{
		hmacKey(secret, "phone", strings.TrimSpace(phoneE164)),
		hmacKey(secret, "email", normalizeEmail(email)),
	}
}
