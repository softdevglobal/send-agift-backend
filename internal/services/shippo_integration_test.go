package services

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

func TestShippoInternationalCustomsFlow(t *testing.T) {
	_ = godotenv.Load("../../.env")
	apiKey := os.Getenv("SHIPPO_API_KEY")
	if apiKey == "" {
		t.Skip("SHIPPO_API_KEY not set")
	}
	client := NewShippoClient(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	from := ShippoAddressInput{
		Name: "Shippo Test Seller", Street1: "731 Market St", City: "San Francisco",
		State: "CA", Zip: "94103", Country: "US", Phone: "+14155550100", Email: "seller@test.com",
	}
	to := ShippoAddressInput{
		Name: "Sydney Recipient", Street1: "100 George St", City: "Sydney",
		State: "NSW", Zip: "2000", Country: "AU", Phone: "+61290001234", Email: "recipient@test.com",
		IsResidential: true,
	}
	parcel := ParcelInput{Length: "20", Width: "15", Height: "10", DistanceUnit: "cm", Weight: "1.200", MassUnit: "kg"}
	customsInput := CustomsDeclarationInput{
		ContentsType: "MERCHANDISE", NonDeliveryOption: "RETURN", CertifySigner: "Shippo Test Seller",
		Items: []CustomsItemInput{{
			Description: "Gift Box USA", Quantity: 1, NetWeight: "1.200", MassUnit: "kg",
			ValueAmount: "25.00", ValueCurrency: "USD", OriginCountry: "US",
		}},
	}
	customs, err := client.CreateCustomsDeclaration(ctx, customsToShippo(customsInput))
	if err != nil {
		t.Fatalf("create customs: %v", err)
	}

	shipment, err := client.CreateShipment(ctx, from, to, parcelToShippo(parcel), customs.ObjectID)
	if err != nil {
		t.Fatalf("create shipment: %v", err)
	}

	got, err := client.GetShipment(ctx, shipment.ObjectID)
	if err != nil {
		t.Fatalf("get shipment: %v", err)
	}
	if !hasCustomsDeclaration(got.CustomsDeclaration) {
		t.Fatalf("shipment missing customs_declaration: %s", string(got.Raw))
	}

	var uspsRate string
	for _, r := range shipment.Rates {
		if r.Provider == "USPS" {
			uspsRate = r.ObjectID
			break
		}
	}
	if uspsRate == "" && len(shipment.Rates) > 0 {
		uspsRate = shipment.Rates[0].ObjectID
	}
	if uspsRate == "" {
		t.Fatal("no rates")
	}

	txn, err := client.CreateTransaction(ctx, uspsRate)
	if err != nil {
		t.Fatalf("create transaction: %v", err)
	}
	if txn.TrackingNumber == "" {
		t.Fatalf("no tracking: %s", string(txn.Raw))
	}
	t.Logf("international label ok tracking=%s", txn.TrackingNumber)
}
