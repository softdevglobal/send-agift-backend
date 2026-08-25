package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"myapp/internal/config"
)

// Google Places API (New) endpoints. We call these server-side so the API key
// never reaches the browser bundle.
const (
	placesAutocompleteURL = "https://places.googleapis.com/v1/places:autocomplete"
	placesDetailsURL      = "https://places.googleapis.com/v1/places/"
)

// detailsFieldMask limits the Place Details response to the fields we map onto
// an address. Google bills Place Details by the field tiers requested, so
// asking for less keeps the call cheap.
const detailsFieldMask = "id,types,formattedAddress,addressComponents,location,displayName,shortFormattedAddress"

// ErrPlaceNotFound is returned when Google has no place for the given id.
var ErrPlaceNotFound = errors.New("place not found")

// PlacesService talks to the Google Places API on behalf of the frontend.
type PlacesService struct {
	apiKey string
	client *http.Client
}

func NewPlacesService(cfg *config.Config) *PlacesService {
	return &PlacesService{
		apiKey: cfg.GoogleMapsKey,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// PlaceSuggestion is one autocomplete row shown in the dropdown.
type PlaceSuggestion struct {
	PlaceID       string `json:"place_id"`
	Description   string `json:"description"`
	MainText      string `json:"main_text"`
	SecondaryText string `json:"secondary_text"`
}

// PlaceDetails is a resolved place already split into address fields, ready to
// drop straight into an AddressInput on the client.
type PlaceDetails struct {
	PlaceID          string   `json:"place_id"`
	Name             string   `json:"name,omitempty"`
	FormattedAddress string   `json:"formatted_address"`
	Line1            string   `json:"line1"`
	Line2            string   `json:"line2,omitempty"`
	City             string   `json:"city"`
	Region           string   `json:"region,omitempty"`
	PostalCode       string   `json:"postal_code,omitempty"`
	CountryCode      string   `json:"country_code,omitempty"`
	CountryName      string   `json:"country_name,omitempty"`
	Latitude         *float64 `json:"latitude,omitempty"`
	Longitude        *float64 `json:"longitude,omitempty"`
}

// AutocompleteParams are the knobs the handler exposes on the search endpoint.
type AutocompleteParams struct {
	Input string
	// SessionToken groups keystrokes with the follow-up Details call into one
	// billable session. The client generates it and reuses it until it picks.
	SessionToken string
	// CountryCode is an ISO-3166-1 alpha-2 code that restricts results.
	CountryCode string
	Language    string
	// Types narrows what kind of place is suggested: "address", "cities",
	// "regions" or "establishment". Empty means everything.
	Types string
}

// includedPrimaryTypes maps our simple type names onto the primary-type filters
// the Places API accepts.
var includedPrimaryTypes = map[string][]string{
	"address":       {"street_address", "premise", "subpremise", "route"},
	"cities":        {"locality", "administrative_area_level_3", "postal_town"},
	"regions":       {"locality", "administrative_area_level_1", "administrative_area_level_2", "postal_code", "country"},
	"establishment": {"establishment"},
}

type autocompleteRequest struct {
	Input                string   `json:"input"`
	SessionToken         string   `json:"sessionToken,omitempty"`
	IncludedRegionCodes  []string `json:"includedRegionCodes,omitempty"`
	IncludedPrimaryTypes []string `json:"includedPrimaryTypes,omitempty"`
	LanguageCode         string   `json:"languageCode,omitempty"`
}

type autocompleteResponse struct {
	Suggestions []struct {
		PlacePrediction struct {
			PlaceID string `json:"placeId"`
			Text    struct {
				Text string `json:"text"`
			} `json:"text"`
			StructuredFormat struct {
				MainText struct {
					Text string `json:"text"`
				} `json:"mainText"`
				SecondaryText struct {
					Text string `json:"text"`
				} `json:"secondaryText"`
			} `json:"structuredFormat"`
		} `json:"placePrediction"`
	} `json:"suggestions"`
}

type placeDetailsResponse struct {
	ID                    string   `json:"id"`
	Types                 []string `json:"types"`
	FormattedAddress      string   `json:"formattedAddress"`
	ShortFormattedAddress string   `json:"shortFormattedAddress"`
	DisplayName           struct {
		Text string `json:"text"`
	} `json:"displayName"`
	Location *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location"`
	AddressComponents []struct {
		LongText  string   `json:"longText"`
		ShortText string   `json:"shortText"`
		Types     []string `json:"types"`
	} `json:"addressComponents"`
}

// Autocomplete returns place predictions for a partial address query.
func (s *PlacesService) Autocomplete(ctx context.Context, params AutocompleteParams) ([]PlaceSuggestion, error) {
	input := strings.TrimSpace(params.Input)
	if input == "" {
		return []PlaceSuggestion{}, nil
	}

	body := autocompleteRequest{
		Input:        input,
		SessionToken: strings.TrimSpace(params.SessionToken),
		LanguageCode: strings.TrimSpace(params.Language),
	}
	if code := strings.TrimSpace(params.CountryCode); code != "" {
		body.IncludedRegionCodes = []string{strings.ToLower(code)}
	}
	if types, ok := includedPrimaryTypes[strings.ToLower(strings.TrimSpace(params.Types))]; ok {
		body.IncludedPrimaryTypes = types
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, placesAutocompleteURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", s.apiKey)

	raw, err := s.do(req)
	if err != nil {
		return nil, err
	}

	var parsed autocompleteResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("places: bad autocomplete response: %w", err)
	}

	suggestions := make([]PlaceSuggestion, 0, len(parsed.Suggestions))
	for _, item := range parsed.Suggestions {
		p := item.PlacePrediction
		if p.PlaceID == "" {
			continue // query predictions carry no place id — nothing to resolve
		}
		suggestions = append(suggestions, PlaceSuggestion{
			PlaceID:       p.PlaceID,
			Description:   p.Text.Text,
			MainText:      p.StructuredFormat.MainText.Text,
			SecondaryText: p.StructuredFormat.SecondaryText.Text,
		})
	}
	return suggestions, nil
}

// Details resolves a place id into structured address fields.
func (s *PlacesService) Details(ctx context.Context, placeID, sessionToken, language string) (*PlaceDetails, error) {
	placeID = strings.TrimSpace(placeID)
	if placeID == "" {
		return nil, ErrPlaceNotFound
	}

	endpoint := placesDetailsURL + url.PathEscape(placeID)
	query := url.Values{}
	if token := strings.TrimSpace(sessionToken); token != "" {
		query.Set("sessionToken", token)
	}
	if lang := strings.TrimSpace(language); lang != "" {
		query.Set("languageCode", lang)
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Goog-Api-Key", s.apiKey)
	req.Header.Set("X-Goog-FieldMask", detailsFieldMask)

	raw, err := s.do(req)
	if err != nil {
		return nil, err
	}

	var parsed placeDetailsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("places: bad details response: %w", err)
	}
	return toPlaceDetails(parsed), nil
}

// do sends the request and turns non-2xx replies into readable errors.
func (s *PlacesService) do(req *http.Request) ([]byte, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("places: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("places: could not read response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrPlaceNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("places: google returned %d: %s", resp.StatusCode, googleErrorMessage(raw))
	}
	return raw, nil
}

// googleErrorMessage pulls the human-readable message out of a Google error body.
func googleErrorMessage(raw []byte) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Error.Message != "" {
		return body.Error.Message
	}
	return strings.TrimSpace(string(raw))
}

// toPlaceDetails folds Google's address components into our address shape.
func toPlaceDetails(resp placeDetailsResponse) *PlaceDetails {
	// component holds both the long and short form of one address component.
	type component struct{ long, short string }
	byType := map[string]component{}
	for _, c := range resp.AddressComponents {
		for _, t := range c.Types {
			if _, seen := byType[t]; !seen {
				byType[t] = component{long: c.LongText, short: c.ShortText}
			}
		}
	}
	long := func(types ...string) string {
		for _, t := range types {
			if c, ok := byType[t]; ok && c.long != "" {
				return c.long
			}
		}
		return ""
	}

	details := &PlaceDetails{
		PlaceID:          resp.ID,
		Name:             resp.DisplayName.Text,
		FormattedAddress: resp.FormattedAddress,
		City:             long("locality", "postal_town", "sublocality_level_1", "sublocality", "administrative_area_level_2"),
		Region:           long("administrative_area_level_1"),
		PostalCode:       long("postal_code"),
		CountryName:      long("country"),
	}
	if c, ok := byType["country"]; ok {
		details.CountryCode = c.short
	}

	// Line 1 is "<street number> <route>"; fall back to the place name or the
	// leading chunk of the formatted address for places with no street data.
	streetNumber := long("street_number")
	route := long("route")
	line1 := strings.TrimSpace(strings.Join([]string{streetNumber, route}, " "))
	if line1 == "" {
		line1 = firstAddressSegment(resp.FormattedAddress)
	}
	if line1 == "" {
		line1 = resp.DisplayName.Text
	}
	details.Line1 = line1

	// A named business keeps its name on line 1 and the street on line 2.
	// Plain street addresses skip this: their displayName is just the street
	// again, abbreviated, which would duplicate line 1.
	name := strings.TrimSpace(resp.DisplayName.Text)
	if name != "" && route != "" && isEstablishment(resp.Types) {
		details.Line1 = name
		details.Line2 = strings.TrimSpace(streetNumber + " " + route)
	} else {
		details.Line2 = long("subpremise")
	}

	if resp.Location != nil {
		lat, lng := resp.Location.Latitude, resp.Location.Longitude
		details.Latitude = &lat
		details.Longitude = &lng
	}
	return details
}

// isEstablishment reports whether a place is a named business or landmark
// rather than a bare street address.
func isEstablishment(types []string) bool {
	for _, t := range types {
		if t == "establishment" || t == "point_of_interest" || t == "premise" {
			return true
		}
	}
	return false
}

// firstAddressSegment returns the text before the first comma of an address.
func firstAddressSegment(formatted string) string {
	parts := strings.SplitN(formatted, ",", 2)
	return strings.TrimSpace(parts[0])
}
