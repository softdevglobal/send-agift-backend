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

func (c *ShippoClient) Enabled() bool {
	return c.apiKey != ""
}

type ShippoAddressInput struct {
	Name          string `json:"name"`
	Company       string `json:"company,omitempty"`
	Street1       string `json:"street1"`
	Street2       string `json:"street2,omitempty"`
	City          string `json:"city"`
	State         string `json:"state,omitempty"`
	Zip           string `json:"zip"`
	Country       string `json:"country"`
	Phone         string `json:"phone,omitempty"`
	Email         string `json:"email,omitempty"`
	IsResidential bool   `json:"is_residential,omitempty"`
}

type shippoParcelInput struct {
	Length       string `json:"length"`
	Width        string `json:"width"`
	Height       string `json:"height"`
	DistanceUnit string `json:"distance_unit"`
	Weight       string `json:"weight"`
	MassUnit     string `json:"mass_unit"`
}

type shippoShipmentRequest struct {
	AddressFrom ShippoAddressInput  `json:"address_from"`
	AddressTo   ShippoAddressInput  `json:"address_to"`
	Parcels     []shippoParcelInput `json:"parcels"`
	Async       bool                `json:"async"`
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
	ObjectID string          `json:"object_id"`
	Status   string          `json:"status"`
	Messages []shippoMessage `json:"messages"`
	Rates    []shippoRateRaw `json:"rates"`
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

func (c *ShippoClient) CreateShipment(ctx context.Context, from, to ShippoAddressInput, parcel shippoParcelInput) (*shippoShipmentResponse, error) {
	req := shippoShipmentRequest{
		AddressFrom: from,
		AddressTo:   to,
		Parcels:     []shippoParcelInput{parcel},
		Async:       false,
	}
	var out shippoShipmentResponse
	if err := c.post(ctx, "/shipments/", req, &out); err != nil {
		return nil, err
	}
	if strings.EqualFold(out.Status, "ERROR") {
		return nil, fmt.Errorf("shipment error: %s", formatShippoMessages(out.Messages))
	}
	return &out, nil
}

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
