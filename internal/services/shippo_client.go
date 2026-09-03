package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const shippoBaseURL = "https://api.goshippo.com"

// ShippoClient is a thin HTTP wrapper around the Shippo REST API.
// Used by ShippingService for customs, shipment/rates, and label transactions.
type ShippoClient struct {
	apiKey     string
	httpClient *http.Client
}

func NewShippoClient(apiKey string) *ShippoClient {
	return &ShippoClient{
		apiKey: strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Enabled is true when SHIPPO_API_KEY is set.
func (c *ShippoClient) Enabled() bool {
	return c.apiKey != ""
}

// ShippoAddressInput is a ship-from or ship-to address for Shippo shipments.
type ShippoAddressInput struct {
	Name          string `json:"name"`
	Company       string `json:"company,omitempty"`
	Street1       string `json:"street1"`
	Street2       string `json:"street2,omitempty"`
	City          string `json:"city"`
	State         string `json:"state,omitempty"`
	Zip           string `json:"zip"`
	Country       string `json:"country"` // ISO2, e.g. US
	Phone         string `json:"phone,omitempty"`
	Email         string `json:"email,omitempty"`
	IsResidential bool   `json:"is_residential,omitempty"`
}

// shippoParcelInput is package dimensions/weight in Shippo's expected shape.
type shippoParcelInput struct {
	Length       string `json:"length"`
	Width        string `json:"width"`
	Height       string `json:"height"`
	DistanceUnit string `json:"distance_unit"`
	Weight       string `json:"weight"`
	MassUnit     string `json:"mass_unit"`
}

// shippoShipmentRequest creates a Shippo shipment and returns carrier rates.
// CustomsDeclaration is the Shippo customs object_id (international only).
type shippoShipmentRequest struct {
	AddressFrom        ShippoAddressInput  `json:"address_from"`
	AddressTo          ShippoAddressInput  `json:"address_to"`
	Parcels            []shippoParcelInput `json:"parcels"`
	Async              bool                `json:"async"`
	CustomsDeclaration string              `json:"customs_declaration,omitempty"`
}

// shippoCustomsItemInput is one customs line item sent to Shippo.
type shippoCustomsItemInput struct {
	Description   string `json:"description"`
	Quantity      int    `json:"quantity"`
	NetWeight     string `json:"net_weight"`
	MassUnit      string `json:"mass_unit"`
	ValueAmount   string `json:"value_amount"`
	ValueCurrency string `json:"value_currency"`
	OriginCountry string `json:"origin_country"`
	TariffNumber  string `json:"tariff_number,omitempty"` // optional HS code
}

// shippoCustomsDeclarationRequest is POST /customs/declarations/ body.
type shippoCustomsDeclarationRequest struct {
	ContentsType        string                   `json:"contents_type"`
	ContentsExplanation string                   `json:"contents_explanation,omitempty"`
	NonDeliveryOption   string                   `json:"non_delivery_option"`
	Certify             bool                     `json:"certify"`
	CertifySigner       string                   `json:"certify_signer"`
	EelPfc              string                   `json:"eel_pfc,omitempty"`   // US export exemption
	Incoterm            string                   `json:"incoterm,omitempty"` // e.g. DDU
	Items               []shippoCustomsItemInput `json:"items"`
}

type shippoCustomsDeclarationResponse struct {
	ObjectID string          `json:"object_id"`
	Status   string          `json:"status"`
	Messages []shippoMessage `json:"messages"`
}

type ShippoRate struct {
	ObjectID      string `json:"object_id"`
	Provider      string `json:"provider"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	EstimatedDays int    `json:"estimated_days"`
	DurationTerms string `json:"duration_terms"`
	ServiceName   string `json:"service_name"`
}

type shippoRateRaw struct {
	ObjectID      string `json:"object_id"`
	Provider      string `json:"provider"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	EstimatedDays int    `json:"estimated_days"`
	DurationTerms string `json:"duration_terms"`
	ServiceLevel  struct {
		Name string `json:"name"`
	} `json:"servicelevel"`
}

type shippoShipmentResponse struct {
	ObjectID           string          `json:"object_id"`
	Status             string          `json:"status"`
	Messages           []shippoMessage `json:"messages"`
	Rates              []shippoRateRaw `json:"rates"`
	CustomsDeclaration json.RawMessage `json:"customs_declaration"`
	Raw                json.RawMessage `json:"-"`
}

type shippoMessage struct {
	Source string `json:"source"`
	Code   string `json:"code"`
	Text   string `json:"text"`
}

type shippoTransactionRequest struct {
	Rate          string `json:"rate"`
	LabelFileType string `json:"label_file_type"`
	Async         bool   `json:"async"`
}

type ShippoTransaction struct {
	ObjectID            string          `json:"object_id"`
	Status              string          `json:"status"`
	TrackingNumber      string          `json:"tracking_number"`
	TrackingURLProvider string          `json:"tracking_url_provider"`
	LabelURL            string          `json:"label_url"`
	Rate                string          `json:"rate"`
	Raw                 json.RawMessage `json:"-"`
}

// CreateCustomsDeclaration creates a Shippo customs form and returns its object_id
// to attach when creating an international shipment.
func (c *ShippoClient) CreateCustomsDeclaration(ctx context.Context, req shippoCustomsDeclarationRequest) (*shippoCustomsDeclarationResponse, error) {
	var out shippoCustomsDeclarationResponse
	body, err := c.do(ctx, http.MethodPost, "/customs/declarations/", req)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if strings.EqualFold(out.Status, "ERROR") {
		return nil, fmt.Errorf("customs declaration error: %s", formatShippoMessages(out.Messages))
	}
	if strings.TrimSpace(out.ObjectID) == "" {
		return nil, fmt.Errorf("customs declaration missing object_id: %s", string(body))
	}
	return &out, nil
}

// GetShipment fetches an existing Shippo shipment by object_id.
func (c *ShippoClient) GetShipment(ctx context.Context, objectID string) (*shippoShipmentResponse, error) {
	body, err := c.do(ctx, http.MethodGet, "/shipments/"+objectID+"/", nil)
	if err != nil {
		return nil, err
	}
	var out shippoShipmentResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	out.Raw = body
	return &out, nil
}

// CreateShipment builds a Shippo shipment and returns available carrier rates.
// Pass customsDeclarationID for international; leave empty for domestic.
func (c *ShippoClient) CreateShipment(ctx context.Context, from, to ShippoAddressInput, parcel shippoParcelInput, customsDeclarationID string) (*shippoShipmentResponse, error) {
	req := shippoShipmentRequest{
		AddressFrom: from,
		AddressTo:   to,
		Parcels:     []shippoParcelInput{parcel},
		Async:       false,
	}
	if strings.TrimSpace(customsDeclarationID) != "" {
		req.CustomsDeclaration = customsDeclarationID
	}
	var out shippoShipmentResponse
	body, err := c.do(ctx, http.MethodPost, "/shipments/", req)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	out.Raw = body
	if strings.EqualFold(out.Status, "ERROR") {
		return nil, fmt.Errorf("shipment error: %s", formatShippoMessages(out.Messages))
	}
	if strings.TrimSpace(customsDeclarationID) != "" && !hasCustomsDeclaration(out.CustomsDeclaration) {
		return nil, fmt.Errorf("shipment created without customs_declaration link: %s", string(body))
	}
	return &out, nil
}

// CreateTransaction buys a shipping label for the given rate_object_id.
// On success returns tracking_number and label_url (PDF).
func (c *ShippoClient) CreateTransaction(ctx context.Context, rateObjectID string) (*ShippoTransaction, error) {
	req := shippoTransactionRequest{
		Rate:          rateObjectID,
		LabelFileType: "PDF",
		Async:         false,
	}
	body, err := c.do(ctx, http.MethodPost, "/transactions/", req)
	if err != nil {
		return nil, err
	}
	var out ShippoTransaction
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	out.Raw = body
	if strings.ToUpper(out.Status) != "SUCCESS" {
		return nil, fmt.Errorf("shippo transaction failed: %s", string(body))
	}
	return &out, nil
}

func (c *ShippoClient) post(ctx context.Context, path string, payload any, out any) error {
	body, err := c.do(ctx, http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// do sends an authenticated request to Shippo (Authorization: ShippoToken <key>).
func (c *ShippoClient) do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("shippo is not configured")
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, shippoBaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "ShippoToken "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("shippo %s %s: %s", method, path, string(respBody))
	}
	return respBody, nil
}

func mapShippoRates(raw []shippoRateRaw) []ShippoRate {
	rates := make([]ShippoRate, 0, len(raw))
	for _, r := range raw {
		rates = append(rates, ShippoRate{
			ObjectID:      r.ObjectID,
			Provider:      r.Provider,
			Amount:        r.Amount,
			Currency:      r.Currency,
			EstimatedDays: r.EstimatedDays,
			DurationTerms: r.DurationTerms,
			ServiceName:   r.ServiceLevel.Name,
		})
	}
	return rates
}

func hasCustomsDeclaration(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}

func formatShippoMessages(msgs []shippoMessage) string {
	if len(msgs) == 0 {
		return "unknown error"
	}
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if strings.TrimSpace(m.Text) != "" {
			parts = append(parts, m.Text)
		}
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, "; ")
}
