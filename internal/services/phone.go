package services

import (
	"errors"
	"strconv"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

var ErrInvalidPhone = errors.New("enter a valid phone number")

// NormalizePhone returns raw in E.164 (+94771234567). A number without a
// country code is read as one from defaultCountryCode ("+94").
func NormalizePhone(raw, defaultCountryCode string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInvalidPhone
	}
	if strings.HasPrefix(raw, "00") {
		raw = "+" + raw[2:]
	}
	cc, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(defaultCountryCode), "+"))
	num, err := phonenumbers.Parse(raw, phonenumbers.GetRegionCodeForCountryCode(cc))
	if err != nil || !phonenumbers.IsValidNumber(num) {
		return "", ErrInvalidPhone
	}
	return phonenumbers.Format(num, phonenumbers.E164), nil
}

// maskPhone keeps a number out of logs: +94771234567 -> +947*****567.
func maskPhone(e164 string) string {
	if len(e164) < 8 {
		return "***"
	}
	return e164[:4] + strings.Repeat("*", len(e164)-7) + e164[len(e164)-3:]
}
