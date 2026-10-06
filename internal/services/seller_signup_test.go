package services

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
)

func strp(v string) *string { return &v }
func boolp(v bool) *bool    { return &v }

func TestApplySignupDetailsOlderClientSendsNothingNew(t *testing.T) {
	seller := &models.Seller{CountryID: uuid.New()}
	ids, taxes, err := applySignupDetails(SellerRegisterInput{}, seller, time.Now())
	if err != nil {
		t.Fatalf("empty onboarding fields must stay valid: %v", err)
	}
	if len(ids) != 0 || len(taxes) != 0 || seller.RegistrationStatus != nil || seller.TermsAcceptedAt != nil {
		t.Fatalf("nothing should be set: %+v %v %v", seller, ids, taxes)
	}
}

func TestApplySignupDetailsRegisteredNeedsIdentifier(t *testing.T) {
	seller := &models.Seller{CountryID: uuid.New()}
	_, _, err := applySignupDetails(SellerRegisterInput{RegistrationStatus: "registered"}, seller, time.Now())
	if !errors.Is(err, ErrInvalidSellerSignup) {
		t.Fatalf("want sign-up error, got %v", err)
	}

	ids, _, err := applySignupDetails(SellerRegisterInput{
		RegistrationStatus: "registered",
		Identifiers:        []SellerIdentifierInput{{Type: "ABN", Value: " 51 824 753-556 ", Authority: strp("  ")}},
	}, seller, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0].Value != "51 824 753-556" || ids[0].ValueNormalised != "51824753556" {
		t.Fatalf("identifier not normalised: %+v", ids)
	}
	if ids[0].Authority != nil {
		t.Fatalf("blank authority should be nil")
	}
	if ids[0].CountryID != seller.CountryID {
		t.Fatalf("identifier must use the business country")
	}
}

func TestApplySignupDetailsPendingNeedsNoteAndNoIdentifiers(t *testing.T) {
	seller := &models.Seller{}
	cases := []SellerRegisterInput{
		{RegistrationStatus: "pending"},
		{RegistrationStatus: "pending", RegistrationNote: strp("too short")},
		{RegistrationStatus: "no_number", RegistrationNote: strp("Sole traders here get no number"),
			Identifiers: []SellerIdentifierInput{{Type: "X", Value: "1"}}},
		{RegistrationStatus: "maybe"},
	}
	for i, in := range cases {
		if _, _, err := applySignupDetails(in, seller, time.Now()); !errors.Is(err, ErrInvalidSellerSignup) {
			t.Fatalf("case %d: want sign-up error, got %v", i, err)
		}
	}
	if _, _, err := applySignupDetails(SellerRegisterInput{
		RegistrationStatus: "no_number", RegistrationNote: strp("Sole traders here get no number"),
	}, seller, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestApplySignupDetailsTaxRules(t *testing.T) {
	seller := &models.Seller{}
	if _, _, err := applySignupDetails(SellerRegisterInput{TaxStatus: "registered"}, seller, time.Now()); !errors.Is(err, ErrInvalidSellerSignup) {
		t.Fatalf("registered tax needs a record, got %v", err)
	}
	if _, _, err := applySignupDetails(SellerRegisterInput{
		TaxStatus:        "unsure",
		TaxRegistrations: []SellerTaxRegistrationInput{{CountryID: uuid.NewString(), Scheme: "GST", Number: "1"}},
	}, seller, time.Now()); !errors.Is(err, ErrInvalidSellerSignup) {
		t.Fatalf("records only allowed when registered, got %v", err)
	}
	_, taxes, err := applySignupDetails(SellerRegisterInput{
		TaxStatus:        "registered",
		TaxRegistrations: []SellerTaxRegistrationInput{{CountryID: uuid.NewString(), Scheme: " GST ", Number: " 123 "}},
	}, seller, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if taxes[0].Scheme != "GST" || taxes[0].Number != "123" {
		t.Fatalf("tax not trimmed: %+v", taxes[0])
	}
}

func TestApplySignupDetailsConsentTimestamps(t *testing.T) {
	now := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	seller := &models.Seller{}
	if _, _, err := applySignupDetails(SellerRegisterInput{
		ContactRole: "owner", AuthorityConfirmed: true, TermsAccepted: true, MarketingOptIn: true,
	}, seller, now); err != nil {
		t.Fatal(err)
	}
	if seller.AuthorityConfirmedAt == nil || !seller.AuthorityConfirmedAt.Equal(now) ||
		seller.TermsAcceptedAt == nil || !seller.MarketingOptIn || *seller.ContactRole != "owner" {
		t.Fatalf("consents not recorded: %+v", seller)
	}
	if _, _, err := applySignupDetails(SellerRegisterInput{ContactRole: "boss"}, seller, now); !errors.Is(err, ErrInvalidSellerSignup) {
		t.Fatalf("unknown role must fail, got %v", err)
	}
}

func TestSignupShopAddressesSkipsRegistered(t *testing.T) {
	addrs := []models.SellerAddress{
		{AddressType: "registered"},
		{AddressType: "pickup"},
		{AddressType: "return"},
	}
	pickup, ret := signupShopAddresses(addrs)
	if pickup == nil || *pickup != 1 || ret == nil || *ret != 2 {
		t.Fatalf("got pickup=%v return=%v", pickup, ret)
	}

	pickup, ret = signupShopAddresses([]models.SellerAddress{{AddressType: "registered"}, {AddressType: "both"}})
	if pickup == nil || *pickup != 1 || ret == nil || *ret != 1 {
		t.Fatalf("both should serve as pickup and return: %v %v", pickup, ret)
	}

	pickup, ret = signupShopAddresses([]models.SellerAddress{{AddressType: "registered"}})
	if pickup != nil || ret != nil {
		t.Fatalf("registered address must never be a delivery origin")
	}
}

func TestKeepOneDefaultAddress(t *testing.T) {
	addrs := []models.SellerAddress{{IsDefault: false}, {IsDefault: false}}
	one := 1
	keepOneDefaultAddress(addrs, &one)
	if addrs[0].IsDefault || !addrs[1].IsDefault {
		t.Fatalf("pickup should become default: %+v", addrs)
	}

	addrs = []models.SellerAddress{{IsDefault: true}, {IsDefault: true}}
	keepOneDefaultAddress(addrs, nil)
	if !addrs[0].IsDefault || addrs[1].IsDefault {
		t.Fatalf("only the first flagged default should stay: %+v", addrs)
	}
}

func TestApplyShopExtras(t *testing.T) {
	shop := &models.Shop{Website: strp("https://keep.example"), Categories: []string{"art"}}
	if err := applyShopExtras(shop, ShopInput{}); err != nil {
		t.Fatal(err)
	}
	if shop.Website == nil || len(shop.Categories) != 1 {
		t.Fatalf("nil fields must keep current values: %+v", shop)
	}

	err := applyShopExtras(shop, ShopInput{
		Website:       strp(" https://gifts.example/shop "),
		SupportEmail:  strp("help@gifts.example"),
		Categories:    []string{"Flowers", "food", "flowers"},
		GiftOptions:   []string{"wrapping"},
		WorkingDays:   []string{"fri", "mon", "wed", "mon"},
		PickupEnabled: boolp(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if *shop.Website != "https://gifts.example/shop" || !shop.PickupEnabled {
		t.Fatalf("extras not applied: %+v", shop)
	}
	if got := shop.Categories; len(got) != 2 || got[0] != "flowers" || got[1] != "food" {
		t.Fatalf("categories not deduplicated: %v", got)
	}
	if got := shop.WorkingDays; len(got) != 3 || got[0] != "mon" || got[1] != "wed" || got[2] != "fri" {
		t.Fatalf("working days not ordered: %v", got)
	}

	bad := []ShopInput{
		{Website: strp("javascript:alert(1)")},
		{Website: strp("gifts.example")},
		{SupportEmail: strp("Help <help@gifts.example>")},
		{Categories: []string{"weapons"}},
		{GiftOptions: []string{"engraving"}},
		{WorkingDays: []string{"monday"}},
	}
	for i, in := range bad {
		if err := applyShopExtras(&models.Shop{}, in); !errors.Is(err, ErrInvalidSellerSignup) {
			t.Fatalf("case %d: want sign-up error, got %v", i, err)
		}
	}

	if err := applyShopExtras(shop, ShopInput{Website: strp("  ")}); err != nil || shop.Website != nil {
		t.Fatalf("blank website should clear it: %v %v", err, shop.Website)
	}
}
