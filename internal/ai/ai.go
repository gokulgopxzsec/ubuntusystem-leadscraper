package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Provider interface {
	AuditWebsite(ctx context.Context, req AuditRequest) (*AuditResponse, error)
	Name() string
}

type AuditRequest struct {
	URL         string
	HTMLContent string
	Title       string
	MetaTags    map[string]string
	StatusCode  int

	BusinessName string
	Category     string
	Technologies []string
	HasSSL       bool
	HasBooking   bool
	IsMobile     bool
}

type AuditResponse struct {
	QualityScore    int      `json:"quality_score"`
	SEOScore        int      `json:"seo_score"`
	MobileScore     int      `json:"mobile_score"`
	Issues          []string `json:"issues"`
	Recommendations []string `json:"recommendations"`
	Summary         string   `json:"summary"`
	ServicesToOffer []string `json:"services_to_offer"`
}

var ErrNotConfigured = errors.New("no AI provider configured")

// Noop is the provider used when AI_PROVIDER is unset. It reports that it did
// nothing rather than returning invented scores, which is what the old stub did.
type Noop struct{}

func (Noop) Name() string { return "none" }

func (Noop) AuditWebsite(context.Context, AuditRequest) (*AuditResponse, error) {
	return nil, ErrNotConfigured
}

const SystemPrompt = `You audit small-business websites in India for a sales team selling makeforme.in, an online store builder (₹99/month: sell products, take bookings, accept UPI payments).

Score the site on three axes from 0 to 10, where 0 is terrible and 10 is excellent:
- quality_score: overall design, trust, and completeness
- seo_score: title, meta description, headings, discoverability
- mobile_score: whether this works on a phone, which is how most Indian buyers arrive

Then list concrete issues, concrete recommendations, and which makeforme services would help this business most (choose from: online store, digital products, appointment bookings, event ticketing, payment collection).

Respond with ONLY a JSON object. No markdown fence, no prose:
{"quality_score":0,"seo_score":0,"mobile_score":0,"issues":["..."],"recommendations":["..."],"summary":"one or two sentences","services_to_offer":["..."]}`

// BuildPrompt renders the user-side prompt. The caller truncates the HTML.
func BuildPrompt(req AuditRequest) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Business: %s\n", orUnknown(req.BusinessName))
	fmt.Fprintf(&b, "Category: %s\n", orUnknown(req.Category))
	fmt.Fprintf(&b, "URL: %s\n", req.URL)
	fmt.Fprintf(&b, "HTTP status: %d\n", req.StatusCode)
	fmt.Fprintf(&b, "Page title: %s\n", orUnknown(req.Title))
	fmt.Fprintf(&b, "Meta description: %s\n", orUnknown(req.MetaTags["description"]))
	fmt.Fprintf(&b, "Has viewport meta (mobile): %t\n", req.IsMobile)
	fmt.Fprintf(&b, "Has HTTPS: %t\n", req.HasSSL)
	fmt.Fprintf(&b, "Has booking or checkout signal: %t\n", req.HasBooking)

	if len(req.Technologies) > 0 {
		fmt.Fprintf(&b, "Detected technologies: %s\n", strings.Join(req.Technologies, ", "))
	}

	b.WriteString("\nPage content:\n")
	b.WriteString(req.HTMLContent)

	return b.String()
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// jsonFence matches a ```json ... ``` wrapper. Models add it despite being told
// not to, and json.Unmarshal will not see past it.
var jsonFence = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)\\s*```")

// ParseAuditJSON is tolerant about what the model wraps around its JSON, then
// strict about the result.
func ParseAuditJSON(raw string) (*AuditResponse, error) {
	raw = strings.TrimSpace(raw)

	if m := jsonFence.FindStringSubmatch(raw); len(m) > 1 {
		raw = strings.TrimSpace(m[1])
	}

	// Fall back to the outermost braces if the model prefixed a sentence.
	if !strings.HasPrefix(raw, "{") {
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("no JSON object in model response: %q", Truncate(raw, 200))
		}
		raw = raw[start : end+1]
	}

	var out AuditResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse model response: %w (got %q)", err, Truncate(raw, 200))
	}

	// The model does not reliably honour the 0-10 range.
	out.QualityScore = clamp(out.QualityScore, 0, 10)
	out.SEOScore = clamp(out.SEOScore, 0, 10)
	out.MobileScore = clamp(out.MobileScore, 0, 10)

	return &out, nil
}

// TruncateHTML bounds what reaches the model. Cutting on a rune boundary avoids
// handing the API invalid UTF-8.
func TruncateHTML(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func clamp(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
