package googlemaps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/makeforme/leadscraper/internal/domain"
	"github.com/makeforme/leadscraper/internal/ports"
)

// ErrNoAPIKey means the source is configured but unusable. Scraping the Maps
// UI directly violates Google's terms, so the official Places API is the only
// path this adapter takes.
var ErrNoAPIKey = errors.New("google_maps source requires GOOGLE_PLACES_API_KEY")

const (
	searchTextURL = "https://places.googleapis.com/v1/places:searchText"

	// The API returns at most 20 places per page and caps a query at 3 pages.
	pageSize = 20
	maxPages = 3
)

// fieldMask is mandatory on the Places API (New): you are billed for the
// fields you ask for, so request exactly what the pipeline consumes.
const fieldMask = "places.id,places.displayName,places.formattedAddress," +
	"places.nationalPhoneNumber,places.internationalPhoneNumber,places.websiteUri," +
	"places.rating,places.userRatingCount,places.primaryType,places.types," +
	"places.location,places.businessStatus,nextPageToken"

type Adapter struct {
	apiKey string
	client *http.Client
}

func NewAdapter(apiKey string) *Adapter {
	return &Adapter{
		apiKey: apiKey,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (a *Adapter) Name() string { return "google_maps" }

type searchRequest struct {
	TextQuery    string `json:"textQuery"`
	PageSize     int    `json:"pageSize,omitempty"`
	PageToken    string `json:"pageToken,omitempty"`
	LanguageCode string `json:"languageCode,omitempty"`
	RegionCode   string `json:"regionCode,omitempty"`
}

type searchResponse struct {
	Places        []place `json:"places"`
	NextPageToken string  `json:"nextPageToken"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

type place struct {
	ID          string `json:"id"`
	DisplayName struct {
		Text string `json:"text"`
	} `json:"displayName"`
	FormattedAddress      string   `json:"formattedAddress"`
	NationalPhoneNumber   string   `json:"nationalPhoneNumber"`
	InternationalPhone    string   `json:"internationalPhoneNumber"`
	WebsiteURI            string   `json:"websiteUri"`
	Rating                float64  `json:"rating"`
	UserRatingCount       int      `json:"userRatingCount"`
	PrimaryType           string   `json:"primaryType"`
	Types                 []string `json:"types"`
	BusinessStatus        string   `json:"businessStatus"`
	Location              struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location"`
}

func (a *Adapter) Scrape(ctx context.Context, params ports.ScrapeParams, results chan<- ports.BusinessResult) error {
	defer close(results)

	if a.apiKey == "" {
		return ErrNoAPIKey
	}

	query := params.SearchQuery()
	if strings.TrimSpace(query) == "" {
		return errors.New("google_maps source requires a category or query")
	}

	sent, token := 0, ""
	for page := 0; page < maxPages; page++ {
		if params.Limit > 0 && sent >= params.Limit {
			return nil
		}

		resp, err := a.search(ctx, query, token)
		if err != nil {
			return err
		}

		for _, p := range resp.Places {
			if params.Limit > 0 && sent >= params.Limit {
				return nil
			}

			// A permanently closed business is not a lead.
			if p.BusinessStatus == "CLOSED_PERMANENTLY" {
				continue
			}

			select {
			case results <- ports.BusinessResult{Business: *toBusiness(p, params.Category)}:
				sent++
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if resp.NextPageToken == "" {
			return nil
		}
		token = resp.NextPageToken

		// The next page token needs a moment to become valid server-side.
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

func (a *Adapter) search(ctx context.Context, query, pageToken string) (*searchResponse, error) {
	body, err := json.Marshal(searchRequest{
		TextQuery:    query,
		PageSize:     pageSize,
		PageToken:    pageToken,
		LanguageCode: "en",
		RegionCode:   "IN",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal places request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchTextURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build places request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", a.apiKey)
	req.Header.Set("X-Goog-FieldMask", fieldMask)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("places search: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read places response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Surface the API's own message: "billing not enabled" and "key
		// restricted" are the two failures that actually happen, and a bare
		// status code sends you hunting in the wrong place.
		return nil, fmt.Errorf("places search: http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var out searchResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode places response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("places search: %s: %s", out.Error.Status, out.Error.Message)
	}

	return &out, nil
}

func toBusiness(p place, fallbackCategory string) *domain.Business {
	phone := p.InternationalPhone
	if phone == "" {
		phone = p.NationalPhoneNumber
	}

	category := prettyType(p.PrimaryType)
	if category == "" {
		category = fallbackCategory
	}

	b := &domain.Business{
		Name:      p.DisplayName.Text,
		Address:   p.FormattedAddress,
		Phone:     phone,
		Website:   p.WebsiteURI,
		Rating:    p.Rating,
		Category:  category,
		Source:    "google_maps",
		SourceKey: p.ID,
	}

	if p.Location.Latitude != 0 || p.Location.Longitude != 0 {
		b.Coordinates = &domain.Coordinates{
			Lat: p.Location.Latitude,
			Lng: p.Location.Longitude,
		}
	}

	// Keep the signals the pipeline does not model as columns but a salesperson
	// still wants: review volume tells you whether the business is actually alive.
	meta, err := json.Marshal(map[string]any{
		"user_rating_count": p.UserRatingCount,
		"business_status":   p.BusinessStatus,
		"types":             p.Types,
	})
	if err == nil {
		b.Metadata = meta
	}

	return b
}

// prettyType turns "beauty_salon" into "beauty salon".
func prettyType(t string) string {
	return strings.ReplaceAll(t, "_", " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
