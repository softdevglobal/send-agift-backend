package services

import (
	"errors"
	"testing"

	"myapp/internal/models"
)

func ptr[T any](v T) *T { return &v }

func validApplication() *models.SellerApplication {
	addr := models.ApplicationAddress{Country: "AU", Line1: " 1 Collins St ", City: "Melbourne", Region: ptr("VIC"), PostalCode: ptr("3000")}
	return &models.SellerApplication{
		Business: models.ApplicationBusiness{
			Country: "US", EntityType: "company", LegalName: "Bloom Pty Ltd",
			RegistrationStatus: "registered",
			Identifiers:        []models.BusinessIdentifier{{Type: "abn", Value: "51 824 753 556"}},
			TaxStatus:          "registered",
			TaxRegistrations:   []models.TaxRegistration{{Country: "au", Scheme: "GST", Number: "51824753556"}},
		},
		Representative: models.ApplicationRepresentative{FullName: "Ada Bloom", Role: "director", Language: "en", AuthorityConfirmed: true},
		Addresses:      models.ApplicationAddresses{Registered: addr, PickupSameAsRegistered: true, ReturnSameAsPickup: true},
		Shop: models.ApplicationShop{
			DisplayName: "Bloom", Slug: "bloom-gifts", Description: "Fresh flowers wrapped with care, delivered same day.",
			Categories: []string{"hampers", "flowers"}, Currency: "aud", TimeZone: "Australia/Melbourne", PublicLocation: "city_country",
		},
		Fulfilment: models.ApplicationFulfilment{
			DeliveryEnabled: true, Bands: []models.DeliveryBand{{UpToKm: 5, Fee: 8, Days: 1}, {UpToKm: 20, Fee: 15, Days: 2}},
			OrderCutoff: ptr("14:00"), WorkingDays: []string{"fri", "mon"}, ReturnsPolicy: "Contact us within 7 days.",
		},
		Payout:   models.ApplicationPayout{BankCountry: "AU", Currency: "AUD"},
		Consents: models.ApplicationConsents{DetailsConfirmed: true, TermsAccepted: true},
	}
}

func TestNormalizeApplicationAcceptsAndTidies(t *testing.T) {
	a := validApplication()
	reasons, err := normalizeApplication(a, "AU")
	if err != nil {
		t.Fatal(err)
	}
	if a.Business.Country != "AU" {
		t.Fatal("the business country must come from the account, not the client")
	}
	if a.Business.Identifiers[0].Value != "51824753556" || a.Business.Identifiers[0].Type != "ABN" {
		t.Fatalf("identifier not normalised: %+v", a.Business.Identifiers[0])
	}
	if a.Addresses.Return.Line1 != "1 Collins St" || a.Addresses.Pickup.City != "Melbourne" {
		t.Fatalf("same-as addresses not copied: %+v", a.Addresses)
	}
	if a.Shop.Categories[0] != "flowers" || a.Fulfilment.WorkingDays[0] != "mon" || a.Shop.Currency != "AUD" {
		t.Fatal("lists not put in order")
	}
	if len(reasons) != 0 {
		t.Fatalf("unexpected review reasons: %v", reasons)
	}
}

func TestNormalizeApplicationRefuses(t *testing.T) {
	cases := map[string]func(a *models.SellerApplication){
		"short ABN":          func(a *models.SellerApplication) { a.Business.Identifiers[0].Value = "123" },
		"no identifier":      func(a *models.SellerApplication) { a.Business.Identifiers = nil },
		"pending, no note":   func(a *models.SellerApplication) { a.Business.RegistrationStatus = "pending" },
		"tax without rows":   func(a *models.SellerApplication) { a.Business.TaxRegistrations = nil },
		"not authorised":     func(a *models.SellerApplication) { a.Representative.AuthorityConfirmed = false },
		"bad slug":           func(a *models.SellerApplication) { a.Shop.Slug = "Bloom Gifts" },
		"short description":  func(a *models.SellerApplication) { a.Shop.Description = "Nice" },
		"unknown category":   func(a *models.SellerApplication) { a.Shop.Categories = []string{"weapons"} },
		"bad zone":           func(a *models.SellerApplication) { a.Shop.TimeZone = "Mars/Base" },
		"bands not rising":   func(a *models.SellerApplication) { a.Fulfilment.Bands[1].UpToKm = 5 },
		"no fulfilment":      func(a *models.SellerApplication) { a.Fulfilment.DeliveryEnabled = false },
		"pickup no steps":    func(a *models.SellerApplication) { a.Fulfilment.PickupEnabled = true },
		"no returns policy":  func(a *models.SellerApplication) { a.Fulfilment.ReturnsPolicy = " " },
		"terms not accepted": func(a *models.SellerApplication) { a.Consents.TermsAccepted = false },
		"website scheme":     func(a *models.SellerApplication) { a.Shop.Website = ptr("javascript:alert(1)") },
	}
	for name, mutate := range cases {
		a := validApplication()
		mutate(a)
		var appErr *ApplicationError
		if _, err := normalizeApplication(a, "AU"); !errors.As(err, &appErr) {
			t.Errorf("%s: want an application error, got %v", name, err)
		}
	}
}

func TestReviewReasonsFlagWhatNeedsACloseLook(t *testing.T) {
	a := validApplication()
	a.Business.RegistrationStatus, a.Business.RegistrationNote = "pending", ptr("Lodged last week, waiting on the number.")
	a.Payout = models.ApplicationPayout{BankCountry: "NZ", Currency: "NZD"}
	reasons, err := normalizeApplication(a, "AU")
	if err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 3 || len(a.Business.Identifiers) != 0 {
		t.Fatalf("reasons = %v, identifiers = %v", reasons, a.Business.Identifiers)
	}
}

func TestApplyApplicationFillsTheAccount(t *testing.T) {
	in := SellerRegisterInput{Phone: ptr("+61 (400) 000-000"), Application: validApplication()}
	in.Application.Addresses.ReturnSameAsPickup = false
	in.Application.Addresses.Return = models.ApplicationAddress{Country: "NZ", Line1: "2 Queen St", City: "Auckland"}
	if err := applyApplication(&in, [16]byte{1}, "AU"); err != nil {
		t.Fatal(err)
	}
	if *in.Phone != "+61400000000" || in.LegalName != "Bloom Pty Ltd" || in.SellerType != "company" || *in.TradingName != "Bloom" {
		t.Fatalf("account fields: %+v", in)
	}
	if len(in.Addresses) != 1 || in.Addresses[0].AddressType != "pickup" {
		t.Fatalf("only the AU pickup address becomes an account address: %+v", in.Addresses)
	}
	in = SellerRegisterInput{Phone: ptr("0400 000 000"), Application: validApplication()}
	if err := applyApplication(&in, [16]byte{1}, "AU"); err == nil {
		t.Fatal("a phone without a country code must be refused")
	}
}
