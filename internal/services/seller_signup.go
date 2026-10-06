package services

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
)

// ErrInvalidSellerSignup wraps every sign-up rule failure. The handler returns
// the wrapped message as-is, so each one names the field to fix.
var ErrInvalidSellerSignup = errors.New("invalid seller sign-up")

func invalidSignup(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidSellerSignup, msg) }

const (
	maxSignupIdentifiers      = 10
	maxSignupTaxRegistrations = 10
)

var (
	registrationStatuses = map[string]bool{"registered": true, "pending": true, "no_number": true}
	taxStatuses          = map[string]bool{"registered": true, "not_registered": true, "unsure": true}
	contactRoles         = map[string]bool{"owner": true, "director": true, "authorised": true}
	shopCategories       = map[string]bool{
		"flowers": true, "hampers": true, "food": true, "personalised": true, "home": true,
		"beauty": true, "jewellery": true, "toys": true, "art": true, "other": true,
	}
	shopGiftOptions = map[string]bool{"message": true, "wrapping": true, "personalisation": true}
	workingDayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
)

// SellerIdentifierInput is one business identifier, issued in the seller's
// business country.
type SellerIdentifierInput struct {
	Type         string  `json:"type"`
	Value        string  `json:"value"`
	Authority    *string `json:"authority"`
	Jurisdiction *string `json:"jurisdiction"`
}

// SellerTaxRegistrationInput is one business tax registration. It can belong
// to a different country than the business.
type SellerTaxRegistrationInput struct {
	CountryID    string  `json:"country_id"`
	Jurisdiction *string `json:"jurisdiction"`
	Scheme       string  `json:"scheme"`
	Number       string  `json:"number"`
}

// applySignupDetails validates the business and contact sections and copies
// them onto seller. All of them are optional for older clients, but once a
// status is given its follow-up fields are required.
func applySignupDetails(in SellerRegisterInput, seller *models.Seller, now time.Time) (
	[]models.SellerIdentifier, []models.SellerTaxRegistration, error,
) {
	seller.LocalName = trimmedOrNil(in.LocalName, 200)

	status := strings.TrimSpace(in.RegistrationStatus)
	if status != "" && !registrationStatuses[status] {
		return nil, nil, invalidSignup("registration_status must be registered, pending or no_number")
	}
	if status != "" {
		seller.RegistrationStatus = &status
	}
	if status != "registered" && len(in.Identifiers) > 0 {
		return nil, nil, invalidSignup("identifiers are only accepted when registration_status is registered")
	}
	if status == "registered" && len(in.Identifiers) == 0 {
		return nil, nil, invalidSignup("add at least one business identifier")
	}
	if len(in.Identifiers) > maxSignupIdentifiers {
		return nil, nil, invalidSignup(fmt.Sprintf("at most %d business identifiers", maxSignupIdentifiers))
	}
	if status == "pending" || status == "no_number" {
		note := trimmedOrNil(in.RegistrationNote, 1000)
		if note == nil || len([]rune(*note)) < 10 {
			return nil, nil, invalidSignup("registration_note needs at least 10 characters when registration is pending or no number was issued")
		}
		seller.RegistrationNote = note
	}

	identifiers := make([]models.SellerIdentifier, 0, len(in.Identifiers))
	for _, item := range in.Identifiers {
		idType := strings.TrimSpace(item.Type)
		value := strings.TrimSpace(item.Value)
		if idType == "" || value == "" {
			return nil, nil, invalidSignup("each business identifier needs a type and a value")
		}
		if len(idType) > 60 || len(value) > 120 {
			return nil, nil, invalidSignup("business identifier type is at most 60 characters and value at most 120")
		}
		identifiers = append(identifiers, models.SellerIdentifier{
			CountryID:       seller.CountryID,
			Type:            idType,
			Value:           value,
			ValueNormalised: normaliseIdentifier(value),
			Authority:       trimmedOrNil(item.Authority, 200),
			Jurisdiction:    trimmedOrNil(item.Jurisdiction, 120),
		})
	}

	taxStatus := strings.TrimSpace(in.TaxStatus)
	if taxStatus != "" && !taxStatuses[taxStatus] {
		return nil, nil, invalidSignup("tax_status must be registered, not_registered or unsure")
	}
	if taxStatus != "" {
		seller.TaxStatus = &taxStatus
	}
	if taxStatus != "registered" && len(in.TaxRegistrations) > 0 {
		return nil, nil, invalidSignup("tax_registrations are only accepted when tax_status is registered")
	}
	if taxStatus == "registered" && len(in.TaxRegistrations) == 0 {
		return nil, nil, invalidSignup("add at least one tax registration")
	}
	if len(in.TaxRegistrations) > maxSignupTaxRegistrations {
		return nil, nil, invalidSignup(fmt.Sprintf("at most %d tax registrations", maxSignupTaxRegistrations))
	}

	taxes := make([]models.SellerTaxRegistration, 0, len(in.TaxRegistrations))
	for _, item := range in.TaxRegistrations {
		countryID, err := uuid.Parse(strings.TrimSpace(item.CountryID))
		if err != nil {
			return nil, nil, invalidSignup("each tax registration needs a valid country_id")
		}
		scheme := strings.TrimSpace(item.Scheme)
		number := strings.TrimSpace(item.Number)
		if scheme == "" || number == "" {
			return nil, nil, invalidSignup("each tax registration needs a scheme and a number")
		}
		if len(scheme) > 80 || len(number) > 60 {
			return nil, nil, invalidSignup("tax scheme is at most 80 characters and number at most 60")
		}
		taxes = append(taxes, models.SellerTaxRegistration{
			CountryID:    countryID,
			Jurisdiction: trimmedOrNil(item.Jurisdiction, 120),
			Scheme:       scheme,
			Number:       number,
		})
	}

	role := strings.TrimSpace(in.ContactRole)
	if role != "" && !contactRoles[role] {
		return nil, nil, invalidSignup("contact_role must be owner, director or authorised")
	}
	if role != "" {
		seller.ContactRole = &role
	}
	seller.ContactName = trimmedOrNil(in.ContactName, 150)
	seller.ContactJobTitle = trimmedOrNil(in.ContactJobTitle, 100)
	if in.AuthorityConfirmed {
		seller.AuthorityConfirmedAt = &now
	}
	if in.TermsAccepted {
		seller.TermsAcceptedAt = &now
	}
	seller.MarketingOptIn = in.MarketingOptIn

	return identifiers, taxes, nil
}

// signupShopAddresses picks the shop's delivery origin (first pickup or both
// address) and return address (first return or both address). A registered
// address is never used for either.
func signupShopAddresses(addresses []models.SellerAddress) (pickup, ret *int) {
	for i := range addresses {
		t := addresses[i].AddressType
		if pickup == nil && (t == "pickup" || t == "both") {
			idx := i
			pickup = &idx
		}
		if ret == nil && (t == "return" || t == "both") {
			idx := i
			ret = &idx
		}
	}
	return pickup, ret
}

// keepOneDefaultAddress leaves exactly one default: the first one flagged, or
// the delivery origin, or the first address.
func keepOneDefaultAddress(addresses []models.SellerAddress, pickup *int) {
	if len(addresses) == 0 {
		return
	}
	chosen := -1
	for i := range addresses {
		if addresses[i].IsDefault {
			chosen = i
			break
		}
	}
	if chosen < 0 && pickup != nil {
		chosen = *pickup
	}
	if chosen < 0 {
		chosen = 0
	}
	for i := range addresses {
		addresses[i].IsDefault = i == chosen
	}
}

// applyShopExtras copies the optional storefront fields onto shop. A nil field
// keeps the shop's current value, so older clients that never send these
// fields do not clear them on update.
func applyShopExtras(shop *models.Shop, in ShopInput) error {
	if in.Website != nil {
		website := trimmedOrNil(in.Website, 500)
		if website != nil {
			u, err := url.Parse(*website)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return invalidSignup("website must be an http:// or https:// address")
			}
		}
		shop.Website = website
	}
	if in.SupportEmail != nil {
		email := trimmedOrNil(in.SupportEmail, 254)
		if email != nil {
			addr, err := mail.ParseAddress(*email)
			if err != nil || addr.Address != *email {
				return invalidSignup("support_email must be a plain email address")
			}
		}
		shop.SupportEmail = email
	}
	if in.ReturnsPolicy != nil {
		shop.ReturnsPolicy = trimmedOrNil(in.ReturnsPolicy, 2000)
	}
	if in.Categories != nil {
		values, err := pickAllowed(in.Categories, shopCategories, "categories")
		if err != nil {
			return err
		}
		shop.Categories = values
	}
	if in.GiftOptions != nil {
		values, err := pickAllowed(in.GiftOptions, shopGiftOptions, "gift_options")
		if err != nil {
			return err
		}
		shop.GiftOptions = values
	}
	if in.WorkingDays != nil {
		seen := map[string]bool{}
		for _, raw := range in.WorkingDays {
			day := strings.ToLower(strings.TrimSpace(raw))
			if !contains(workingDayOrder, day) {
				return invalidSignup("working_days must use mon, tue, wed, thu, fri, sat, sun")
			}
			seen[day] = true
		}
		days := make([]string, 0, len(seen))
		for _, day := range workingDayOrder {
			if seen[day] {
				days = append(days, day)
			}
		}
		shop.WorkingDays = days
	}
	if in.PickupEnabled != nil {
		shop.PickupEnabled = *in.PickupEnabled
	}
	return nil
}

// normaliseIdentifier upper-cases and drops spaces, dots, slashes and hyphens
// so "12 345.678-9" and "123456789" compare equal during review.
func normaliseIdentifier(value string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(value) {
		switch r {
		case ' ', '.', '-', '/', '\t':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func pickAllowed(values []string, allowed map[string]bool, field string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		v := strings.ToLower(strings.TrimSpace(raw))
		if !allowed[v] {
			return nil, invalidSignup(fmt.Sprintf("%s has an unknown value %q", field, raw))
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

// trimmedOrNil trims value and caps it at max runes. Blank becomes nil.
func trimmedOrNil(value *string, max int) *string {
	if value == nil {
		return nil
	}
	v := strings.TrimSpace(*value)
	if v == "" {
		return nil
	}
	if r := []rune(v); len(r) > max {
		v = string(r[:max])
	}
	return &v
}

func contains(values []string, v string) bool {
	for _, item := range values {
		if item == v {
			return true
		}
	}
	return false
}
