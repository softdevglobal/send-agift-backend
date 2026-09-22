package services

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrShippingCustomsRequired is returned when ship-from and ship-to countries differ
// but the rates request has no customs_declaration.
var ErrShippingCustomsRequired = errors.New("customs_declaration is required for international shipments")

// ParcelInput is package size/weight posted on /shipping/rates and sent to Shippo.
// Domestic: optional (defaultDomesticParcel is used). International: required.
type ParcelInput struct {
	Length       string `json:"length"`        // e.g. "20"
	Width        string `json:"width"`         // e.g. "15"
	Height       string `json:"height"`        // e.g. "10"
	DistanceUnit string `json:"distance_unit"` // "cm" or "in"
	Weight       string `json:"weight"`        // e.g. "1.200"
	MassUnit     string `json:"mass_unit"`     // "kg" or "lb"
}

// CustomsItemInput is one line item on the international customs form.
type CustomsItemInput struct {
	Description   string `json:"description"`              // what is in the box
	Quantity      int    `json:"quantity"`                 // must be >= 1
	NetWeight     string `json:"net_weight"`               // item weight
	MassUnit      string `json:"mass_unit"`                // "kg" or "lb"
	ValueAmount   string `json:"value_amount"`             // declared value, e.g. "25.00"
	ValueCurrency string `json:"value_currency"`           // ISO currency, e.g. "USD"
	OriginCountry string `json:"origin_country"`           // ISO2 country of manufacture, e.g. "US"
	TariffNumber  string `json:"tariff_number,omitempty"`  // optional HS code (seller looks this up)
}

// CustomsDeclarationInput is seller-posted customs data for international shipments.
// Required on POST /shipping/rates when ship-from country != ship-to country.
// Stored on marketplace.shipments.customs_declaration.
type CustomsDeclarationInput struct {
	ContentsType        string             `json:"contents_type"`                   // MERCHANDISE, GIFT, DOCUMENTS, etc.
	ContentsExplanation string             `json:"contents_explanation,omitempty"`  // optional detail
	NonDeliveryOption   string             `json:"non_delivery_option"`             // RETURN or ABANDON
	CertifySigner       string             `json:"certify_signer"`                  // person/shop signing the form
	EelPfc              string             `json:"eel_pfc,omitempty"`               // US export exemption; auto-filled if empty
	Incoterm            string             `json:"incoterm,omitempty"`              // who pays duties; defaults to DDU
	Items               []CustomsItemInput `json:"items"`                           // at least one item required
}

// ShippingShipmentInput is the JSON body for POST /shipping/rates.
type ShippingShipmentInput struct {
	Parcel             *ParcelInput             `json:"parcel"`
	CustomsDeclaration *CustomsDeclarationInput `json:"customs_declaration"`
}

// MarshalStored encodes customs for the pending shipment JSONB column.
// Parcel is not stored on shipments — it comes from seller.products.parcel_*.
func (in ShippingShipmentInput) MarshalStored() (customs json.RawMessage) {
	if in.CustomsDeclaration != nil {
		customs, _ = json.Marshal(in.CustomsDeclaration)
	}
	return customs
}

// mergeShippingInput prefers the posted body; falls back to product parcel JSON
// and customs already stored on a pending shipment (so a second rates call can omit them).
func mergeShippingInput(posted ShippingShipmentInput, productOrStoredParcel, storedCustoms json.RawMessage) (ShippingShipmentInput, error) {
	out := posted
	if out.Parcel == nil && len(productOrStoredParcel) > 0 && string(productOrStoredParcel) != "null" {
		var p ParcelInput
		if err := json.Unmarshal(productOrStoredParcel, &p); err != nil {
			return ShippingShipmentInput{}, err
		}
		out.Parcel = &p
	}
	if out.CustomsDeclaration == nil && len(storedCustoms) > 0 {
		var c CustomsDeclarationInput
		if err := json.Unmarshal(storedCustoms, &c); err != nil {
			return ShippingShipmentInput{}, err
		}
		out.CustomsDeclaration = &c
	}
	return out, nil
}

// validateParcelInput checks required dims/weight.
// International: parcel is required. Domestic: nil is allowed (caller uses default).
func validateParcelInput(in *ParcelInput, international bool) error {
	if in == nil {
		if international {
			return ErrInvalidInput
		}
		return nil
	}
	in.Length = strings.TrimSpace(in.Length)
	in.Width = strings.TrimSpace(in.Width)
	in.Height = strings.TrimSpace(in.Height)
	in.Weight = strings.TrimSpace(in.Weight)
	in.DistanceUnit = strings.ToLower(strings.TrimSpace(in.DistanceUnit))
	in.MassUnit = strings.ToLower(strings.TrimSpace(in.MassUnit))
	if in.Length == "" || in.Width == "" || in.Height == "" || in.Weight == "" {
		return ErrInvalidInput
	}
	if in.DistanceUnit == "" {
		in.DistanceUnit = "cm"
	}
	if in.MassUnit == "" {
		in.MassUnit = "kg"
	}
	switch in.DistanceUnit {
	case "cm", "in":
	default:
		return ErrInvalidInput
	}
	switch in.MassUnit {
	case "kg", "lb":
	default:
		return ErrInvalidInput
	}
	return nil
}

// validateCustomsInput normalizes and validates international customs.
// Fills defaults: contents_type=MERCHANDISE, non_delivery=RETURN, eel_pfc, incoterm=DDU.
func validateCustomsInput(in *CustomsDeclarationInput, fromISO, toISO, signerFallback string) error {
	if in == nil {
		return ErrShippingCustomsRequired
	}
	in.ContentsType = strings.ToUpper(strings.TrimSpace(in.ContentsType))
	in.NonDeliveryOption = strings.ToUpper(strings.TrimSpace(in.NonDeliveryOption))
	in.CertifySigner = strings.TrimSpace(in.CertifySigner)
	if in.ContentsType == "" {
		in.ContentsType = "MERCHANDISE"
	}
	if in.NonDeliveryOption == "" {
		in.NonDeliveryOption = "RETURN"
	}
	if in.CertifySigner == "" {
		in.CertifySigner = strings.TrimSpace(signerFallback)
	}
	if in.CertifySigner == "" {
		return ErrInvalidInput
	}
	if len(in.Items) == 0 {
		return ErrInvalidInput
	}
	// US export exemption codes required by Shippo for many US-origin shipments.
	if strings.TrimSpace(in.EelPfc) == "" {
		if normalizeCountryISO(fromISO) == "US" && normalizeCountryISO(toISO) == "CA" {
			in.EelPfc = "NOEEI_30_36" // US → Canada
		} else {
			in.EelPfc = "NOEEI_30_37_a" // other destinations under $2500
		}
	}
	if strings.TrimSpace(in.Incoterm) == "" {
		in.Incoterm = "DDU" // Delivered Duty Unpaid — recipient pays duties
	}
	for i := range in.Items {
		item := &in.Items[i]
		item.Description = strings.TrimSpace(item.Description)
		item.NetWeight = strings.TrimSpace(item.NetWeight)
		item.ValueAmount = strings.TrimSpace(item.ValueAmount)
		item.ValueCurrency = strings.ToUpper(strings.TrimSpace(item.ValueCurrency))
		item.OriginCountry = normalizeCountryISO(item.OriginCountry)
		item.MassUnit = strings.ToLower(strings.TrimSpace(item.MassUnit))
		if item.Description == "" || item.NetWeight == "" || item.ValueAmount == "" ||
			item.ValueCurrency == "" || item.OriginCountry == "" || item.Quantity < 1 {
			return ErrInvalidInput
		}
		if item.MassUnit == "" {
			item.MassUnit = "kg"
		}
	}
	return nil
}

// defaultDomesticParcel is used when domestic rates are requested with no parcel body.
func defaultDomesticParcel() ParcelInput {
	return ParcelInput{
		Length: "20", Width: "15", Height: "10",
		DistanceUnit: "cm", Weight: "1.200", MassUnit: "kg",
	}
}

// parcelToShippo maps our API parcel DTO into the Shippo parcel payload shape.
func parcelToShippo(in ParcelInput) shippoParcelInput {
	return shippoParcelInput{
		Length:       in.Length,
		Width:        in.Width,
		Height:       in.Height,
		DistanceUnit: in.DistanceUnit,
		Weight:       in.Weight,
		MassUnit:     in.MassUnit,
	}
}

// customsToShippo maps our customs DTO into Shippo's customs_declaration request.
func customsToShippo(in CustomsDeclarationInput) shippoCustomsDeclarationRequest {
	items := make([]shippoCustomsItemInput, 0, len(in.Items))
	for _, item := range in.Items {
		items = append(items, shippoCustomsItemInput{
			Description:   item.Description,
			Quantity:      item.Quantity,
			NetWeight:     item.NetWeight,
			MassUnit:      item.MassUnit,
			ValueAmount:   item.ValueAmount,
			ValueCurrency: item.ValueCurrency,
			OriginCountry: item.OriginCountry,
			TariffNumber:  item.TariffNumber,
		})
	}
	return shippoCustomsDeclarationRequest{
		ContentsType:        in.ContentsType,
		ContentsExplanation: in.ContentsExplanation,
		NonDeliveryOption:   in.NonDeliveryOption,
		Certify:             true,
		CertifySigner:       in.CertifySigner,
		EelPfc:              in.EelPfc,
		Incoterm:            in.Incoterm,
		Items:               items,
	}
}
