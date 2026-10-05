package services

import (
	"fmt"
	"math"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // time zones are checked even where the host has no zoneinfo

	"github.com/google/uuid"

	"myapp/internal/models"
)

// ApplicationError is a seller application with a field that needs fixing.
type ApplicationError struct {
	Field   string
	Message string
}

func (e *ApplicationError) Error() string { return e.Message }

func appErr(field, format string, args ...any) error {
	return &ApplicationError{Field: field, Message: fmt.Sprintf(format, args...)}
}

var (
	isoCountryRe   = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyRe     = regexp.MustCompile(`^[A-Z]{3}$`)
	shopSlugRe     = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	e164Re         = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)
	cutoffRe       = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	languageCodeRe = regexp.MustCompile(`^[a-z]{2,3}$`)

	// identifierDigits are the formats of identifiers with a fixed length.
	identifierDigits = map[string]int{"ABN": 11, "ACN": 9, "BN": 9, "NZBN": 13, "SIREN": 9, "SIRET": 14, "JP_CN": 13}

	entityTypes         = []string{"sole_proprietor", "company", "partnership", "nonprofit", "trust", "other"}
	registrationStates  = []string{"registered", "pending", "no_number"}
	taxStates           = []string{"registered", "not_registered", "unsure"}
	representativeRoles = []string{"owner", "director", "authorised"}
	shopCategories      = []string{"flowers", "hampers", "food", "personalised", "home", "beauty", "jewellery", "toys", "art", "other"}
	giftOptions         = []string{"message", "wrapping", "personalisation"}
	publicLocations     = []string{"city_country", "country_only", "full_pickup"}
	weekDays            = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
)

// sellerTypeFor is the account's seller type for a legal entity type.
func sellerTypeFor(entityType string) string {
	switch entityType {
	case "sole_proprietor":
		return "individual"
	case "partnership":
		return "partnership"
	default:
		return "company"
	}
}

// normalizeApplication tidies and checks an application, and lists what an
// admin should look at closely. businessCountry is the ISO code of the
// seller's account country, which is always the business country.
func normalizeApplication(a *models.SellerApplication, businessCountry string) ([]string, error) {
	b := &a.Business
	b.Country = businessCountry
	b.EntityType = strings.TrimSpace(b.EntityType)
	if !slices.Contains(entityTypes, b.EntityType) {
		return nil, appErr("business.entity_type", "Choose your business type")
	}
	b.EntityTypeOther = clean(b.EntityTypeOther, 120)
	if b.EntityType != "other" {
		b.EntityTypeOther = nil
	} else if b.EntityTypeOther == nil {
		return nil, appErr("business.entity_type_other", "Enter your local legal structure")
	}
	b.LegalName = cut(b.LegalName, 200)
	if b.LegalName == "" {
		return nil, appErr("business.legal_name", "Enter your legal business name")
	}
	b.LocalName = clean(b.LocalName, 200)
	b.TradingName = clean(b.TradingName, 200)

	b.RegistrationStatus = strings.TrimSpace(b.RegistrationStatus)
	if !slices.Contains(registrationStates, b.RegistrationStatus) {
		return nil, appErr("business.registration_status", "Choose your registration status")
	}
	b.RegistrationNote = clean(b.RegistrationNote, 1000)
	if b.RegistrationStatus == "registered" {
		b.RegistrationNote = nil
		if len(b.Identifiers) == 0 || len(b.Identifiers) > 6 {
			return nil, appErr("business.identifiers", "Add your business identifier")
		}
		for i := range b.Identifiers {
			id := &b.Identifiers[i]
			id.Type = strings.ToUpper(cut(id.Type, 40))
			id.TypeLabel = clean(id.TypeLabel, 120)
			id.Value = cut(id.Value, 120)
			id.Authority = clean(id.Authority, 200)
			id.Jurisdiction = clean(id.Jurisdiction, 120)
			if id.Type == "" || id.Value == "" {
				return nil, appErr("business.identifiers", "Each business identifier needs a type and a number")
			}
			if id.Type == "OTHER" && id.TypeLabel == nil {
				return nil, appErr("business.identifiers", "Name the identifier you entered")
			}
			if n, ok := identifierDigits[id.Type]; ok {
				digits := strings.Join(strings.Fields(id.Value), "")
				if len(digits) != n || strings.Trim(digits, "0123456789") != "" {
					return nil, appErr("business.identifiers", "Check the %s: it has %d digits", id.Type, n)
				}
				id.Value = digits
			}
		}
	} else {
		b.Identifiers = []models.BusinessIdentifier{}
		if b.RegistrationNote == nil || len([]rune(*b.RegistrationNote)) < 10 {
			return nil, appErr("business.registration_note", "Tell us a little about your registration (at least 10 characters)")
		}
	}

	b.TaxStatus = strings.TrimSpace(b.TaxStatus)
	if !slices.Contains(taxStates, b.TaxStatus) {
		return nil, appErr("business.tax_status", "Choose your tax registration status")
	}
	if b.TaxStatus == "registered" {
		if len(b.TaxRegistrations) == 0 || len(b.TaxRegistrations) > 10 {
			return nil, appErr("business.tax_registrations", "Add at least one tax registration")
		}
		for i := range b.TaxRegistrations {
			t := &b.TaxRegistrations[i]
			t.Country = strings.ToUpper(strings.TrimSpace(t.Country))
			t.Jurisdiction = clean(t.Jurisdiction, 120)
			t.Scheme = cut(t.Scheme, 80)
			t.Number = cut(t.Number, 120)
			if !isoCountryRe.MatchString(t.Country) || t.Scheme == "" || t.Number == "" {
				return nil, appErr("business.tax_registrations", "Each tax registration needs a country, a type and a number")
			}
		}
	} else {
		b.TaxRegistrations = []models.TaxRegistration{}
	}

	r := &a.Representative
	r.FullName = cut(r.FullName, 150)
	if r.FullName == "" {
		return nil, appErr("representative.full_name", "Enter your full name")
	}
	if !slices.Contains(representativeRoles, r.Role) {
		return nil, appErr("representative.role", "Choose your role in the business")
	}
	r.JobTitle = clean(r.JobTitle, 120)
	r.Language = strings.ToLower(strings.TrimSpace(r.Language))
	r.LanguageOther = clean(r.LanguageOther, 80)
	if r.Language == "other" {
		if r.LanguageOther == nil {
			return nil, appErr("representative.language_other", "Enter your preferred language")
		}
	} else if !languageCodeRe.MatchString(r.Language) {
		return nil, appErr("representative.language", "Choose your preferred language")
	} else {
		r.LanguageOther = nil
	}
	if !r.AuthorityConfirmed {
		return nil, appErr("representative.authority_confirmed", "Confirm you're authorised to set up this account")
	}

	ad := &a.Addresses
	if err := normalizeAppAddress(&ad.Registered, "addresses.registered"); err != nil {
		return nil, err
	}
	if ad.PickupSameAsRegistered {
		ad.Pickup = ad.Registered
	} else if err := normalizeAppAddress(&ad.Pickup, "addresses.pickup"); err != nil {
		return nil, err
	}
	if ad.ReturnSameAsPickup {
		ad.Return = ad.Pickup
	} else if err := normalizeAppAddress(&ad.Return, "addresses.return"); err != nil {
		return nil, err
	}

	s := &a.Shop
	s.DisplayName = cut(s.DisplayName, 80)
	if s.DisplayName == "" {
		return nil, appErr("shop.display_name", "Enter your shop name")
	}
	s.Slug = strings.ToLower(cut(s.Slug, 60))
	if !shopSlugRe.MatchString(s.Slug) {
		return nil, appErr("shop.slug", "Shop web address: lowercase letters, numbers and single hyphens")
	}
	s.Description = cut(s.Description, 1000)
	if len([]rune(s.Description)) < 20 {
		return nil, appErr("shop.description", "Tell customers about your shop (at least 20 characters)")
	}
	var err error
	if s.Categories, err = pickFrom(s.Categories, shopCategories); err != nil || len(s.Categories) == 0 {
		return nil, appErr("shop.categories", "Choose what you'll sell")
	}
	if s.GiftOptions, err = pickFrom(s.GiftOptions, giftOptions); err != nil {
		return nil, appErr("shop.gift_options", "Unknown gift option")
	}
	s.Website = clean(s.Website, 500)
	if s.Website != nil {
		w := strings.ToLower(*s.Website)
		if !strings.HasPrefix(w, "https://") && !strings.HasPrefix(w, "http://") {
			return nil, appErr("shop.website", "Website must start with https://")
		}
	}
	s.Currency = strings.ToUpper(strings.TrimSpace(s.Currency))
	if !currencyRe.MatchString(s.Currency) {
		return nil, appErr("shop.currency", "Choose your shop currency")
	}
	s.TimeZone = strings.TrimSpace(s.TimeZone)
	if _, err := time.LoadLocation(s.TimeZone); err != nil || s.TimeZone == "" || s.TimeZone == "Local" {
		return nil, appErr("shop.time_zone", "Choose your business time zone")
	}
	s.SupportEmail = clean(s.SupportEmail, 254)
	if s.SupportEmail != nil {
		if _, err := mail.ParseAddress(*s.SupportEmail); err != nil {
			return nil, appErr("shop.support_email", "Check the support email")
		}
		lower := strings.ToLower(*s.SupportEmail)
		s.SupportEmail = &lower
	}
	if !slices.Contains(publicLocations, s.PublicLocation) {
		return nil, appErr("shop.public_location", "Choose what customers see of your location")
	}

	f := &a.Fulfilment
	if !f.DeliveryEnabled && !f.PickupEnabled {
		return nil, appErr("fulfilment", "Choose delivery, pickup or both")
	}
	if f.WorkingDays, err = pickFrom(f.WorkingDays, weekDays); err != nil || len(f.WorkingDays) == 0 {
		return nil, appErr("fulfilment.working_days", "Choose at least one working day")
	}
	f.DeliveryNotes = clean(f.DeliveryNotes, 1000)
	if f.DeliveryEnabled {
		if len(f.Bands) == 0 || len(f.Bands) > 10 {
			return nil, appErr("fulfilment.bands", "Add at least one delivery band")
		}
		prev := 0.0
		for _, band := range f.Bands {
			if band.UpToKm <= prev || band.UpToKm > 20000 {
				return nil, appErr("fulfilment.bands", "Each band must reach further than the one before")
			}
			if band.Fee < 0 || band.Fee > 1_000_000 || math.IsNaN(band.Fee) {
				return nil, appErr("fulfilment.bands", "Delivery prices can't be negative")
			}
			if band.Days < 0 || band.Days > 365 {
				return nil, appErr("fulfilment.bands", "Delivery days must be between 0 and 365")
			}
			prev = band.UpToKm
		}
		if f.OrderCutoff == nil || !cutoffRe.MatchString(strings.TrimSpace(*f.OrderCutoff)) {
			return nil, appErr("fulfilment.order_cutoff", "Set your daily order cutoff")
		}
		cutoff := strings.TrimSpace(*f.OrderCutoff)
		f.OrderCutoff = &cutoff
	} else {
		f.Bands, f.OrderCutoff, f.DeliveryNotes = []models.DeliveryBand{}, nil, nil
	}
	f.PickupInstructions = clean(f.PickupInstructions, 1000)
	if !f.PickupEnabled {
		f.PickupInstructions = nil
	} else if f.PickupInstructions == nil {
		return nil, appErr("fulfilment.pickup_instructions", "Tell customers how pickup works")
	}
	f.ReturnsPolicy = cut(f.ReturnsPolicy, 2000)
	if f.ReturnsPolicy == "" {
		return nil, appErr("fulfilment.returns_policy", "Describe your returns process")
	}

	p := &a.Payout
	p.BankCountry = strings.ToUpper(strings.TrimSpace(p.BankCountry))
	if !isoCountryRe.MatchString(p.BankCountry) {
		return nil, appErr("payout.bank_country", "Choose the country of your bank account")
	}
	p.BankCountryOther = clean(p.BankCountryOther, 120)
	if p.BankCountry != "ZZ" {
		p.BankCountryOther = nil
	} else if p.BankCountryOther == nil {
		return nil, appErr("payout.bank_country_other", "Enter the country of your bank account")
	}
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	if !currencyRe.MatchString(p.Currency) {
		return nil, appErr("payout.currency", "Choose your payout currency")
	}

	if !a.Consents.DetailsConfirmed || !a.Consents.TermsAccepted {
		return nil, appErr("consents", "Confirm your details and accept the seller terms")
	}

	return reviewReasons(a), nil
}

// reviewReasons are what an admin should check closely in an application.
func reviewReasons(a *models.SellerApplication) []string {
	var out []string
	if a.Business.RegistrationStatus != "registered" {
		out = append(out, "Business registration needs a manual check")
	}
	if a.Business.TaxStatus == "unsure" {
		out = append(out, "Tax status needs review")
	}
	for _, t := range a.Business.TaxRegistrations {
		if t.Country == "ZZ" {
			out = append(out, "A tax country is not listed")
			break
		}
	}
	if a.Payout.BankCountry != a.Business.Country {
		out = append(out, "Bank country differs from business country")
	}
	if a.Shop.Currency != a.Payout.Currency {
		out = append(out, "Shop and payout currencies differ")
	}
	if a.Addresses.Pickup.Country != a.Business.Country {
		out = append(out, "Pickup address is outside the business country")
	}
	if a.Fulfilment.CrossBorderInterest {
		out = append(out, "Interested in international delivery")
	}
	if a.Representative.Role == "authorised" {
		out = append(out, "Applying as an authorised representative")
	}
	return out
}

func normalizeAppAddress(addr *models.ApplicationAddress, field string) error {
	addr.Country = strings.ToUpper(strings.TrimSpace(addr.Country))
	if !isoCountryRe.MatchString(addr.Country) {
		return appErr(field+".country", "Choose the address country")
	}
	addr.CountryOther = clean(addr.CountryOther, 120)
	if addr.Country != "ZZ" {
		addr.CountryOther = nil
	} else if addr.CountryOther == nil {
		return appErr(field+".country_other", "Enter the address country")
	}
	addr.Line1 = cut(addr.Line1, 240)
	addr.City = cut(addr.City, 120)
	if addr.Line1 == "" || addr.City == "" {
		return appErr(field, "Each address needs a street line and a city")
	}
	addr.Line2 = clean(addr.Line2, 240)
	addr.Region = clean(addr.Region, 120)
	addr.PostalCode = clean(addr.PostalCode, 24)
	if (addr.Latitude == nil) != (addr.Longitude == nil) ||
		(addr.Latitude != nil && (*addr.Latitude < -90 || *addr.Latitude > 90 || *addr.Longitude < -180 || *addr.Longitude > 180)) {
		addr.Latitude, addr.Longitude = nil, nil
	}
	return nil
}

// pickFrom keeps each known value once, in the order allowed lists them.
func pickFrom(values, allowed []string) ([]string, error) {
	out := []string{}
	for _, v := range allowed {
		if slices.Contains(values, v) {
			out = append(out, v)
		}
	}
	for _, v := range values {
		if !slices.Contains(allowed, v) {
			return nil, fmt.Errorf("unknown value %q", v)
		}
	}
	return out, nil
}

// cut trims s and shortens it to at most n characters.
func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = strings.TrimSpace(string(r[:n]))
	}
	return s
}

// clean is cut for an optional value; blank becomes nil.
func clean(s *string, n int) *string {
	if s == nil {
		return nil
	}
	v := cut(*s, n)
	if v == "" {
		return nil
	}
	return &v
}

// normalizePhone strips spacing from a phone number and checks it is E.164.
func normalizePhone(phone string) (string, bool) {
	p := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(phone))
	return p, e164Re.MatchString(p)
}

// applyApplication checks a registration's application and fills in the
// account details that come from it.
func applyApplication(in *SellerRegisterInput, countryID uuid.UUID, countryISO string) error {
	a := in.Application
	reasons, err := normalizeApplication(a, countryISO)
	if err != nil {
		return err
	}
	a.ReviewReasons = reasons

	phone, ok := normalizePhone(derefOr(in.Phone, ""))
	if !ok {
		return appErr("phone", "Enter your phone number with its country code, like +61 400 000 000")
	}
	in.Phone = &phone
	in.LegalName = a.Business.LegalName
	in.SellerType = sellerTypeFor(a.Business.EntityType)
	trading := a.Shop.DisplayName
	if a.Business.TradingName != nil {
		trading = *a.Business.TradingName
	}
	in.TradingName = &trading

	// The pickup and return addresses become the account's addresses when
	// they are in the business country, ready for delivery zones later.
	in.Addresses = nil
	addressInput := func(addr models.ApplicationAddress, kind, label string, isDefault bool) SellerAddressInput {
		return SellerAddressInput{
			CountryID: countryID.String(), Label: &label, AddressType: kind,
			Line1: addr.Line1, Line2: addr.Line2, City: addr.City, Region: addr.Region,
			PostalCode: addr.PostalCode, Latitude: addr.Latitude, Longitude: addr.Longitude,
			IsDefault: isDefault,
		}
	}
	ad := a.Addresses
	if ad.Pickup.Country == countryISO {
		kind, label := "pickup", "Pickup"
		if ad.ReturnSameAsPickup {
			kind, label = "both", "Pickup and returns"
		}
		in.Addresses = append(in.Addresses, addressInput(ad.Pickup, kind, label, true))
	}
	if !ad.ReturnSameAsPickup && ad.Return.Country == countryISO {
		in.Addresses = append(in.Addresses, addressInput(ad.Return, "return", "Returns", len(in.Addresses) == 0))
	}
	return nil
}
