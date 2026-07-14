// Package embed turns lead text into vectors for semantic search.
//
// Every provider here returns 768 dimensions, which is what the lead_embeddings
// column is fixed at. Gemini's text-embedding-004 is natively 768; OpenAI's
// text-embedding-3-small is asked for 768 through its dimensions parameter. That
// means you can switch providers without a migration, though you do have to
// re-embed, since vectors from different models are not comparable.
package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Dimensions is fixed by the lead_embeddings column. Changing it means a
// migration and a full re-embed.
const Dimensions = 768

var ErrNotConfigured = errors.New("no embedding provider configured")

// Provider turns text into vectors. Documents and queries are separate calls
// because some models want to know which it is: an asymmetric model embeds
// "bakeries with no website" (a question) differently from a business record (an
// answer), and using the wrong mode quietly degrades retrieval.
type Provider interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	Name() string
	Model() string
}

// Noop is used when no provider is configured. Search falls back to keyword
// matching rather than failing.
type Noop struct{}

func (Noop) Name() string  { return "none" }
func (Noop) Model() string { return "" }

func (Noop) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return nil, ErrNotConfigured
}

func (Noop) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, ErrNotConfigured
}

// Enabled reports whether p can actually produce vectors.
func Enabled(p Provider) bool {
	if p == nil {
		return false
	}
	_, noop := p.(Noop)
	return !noop
}

// Hash fingerprints the source text so an unchanged lead is not re-embedded.
// At a few thousand leads the saved API calls are the difference between a free
// tier and a bill.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// LeadDocument is the text we actually embed for a business.
//
// What goes in here is the whole ballgame for retrieval quality. It deliberately
// includes the human-meaningful signals a salesperson would search on — what the
// business does, where, how well reviewed, and crucially *what is missing from
// its web presence* — because "bakeries with no website" is exactly the kind of
// question this is for. Raw HTML and IDs are left out: they add tokens and no
// meaning.
type LeadDocument struct {
	Name       string
	Category   string
	Address    string
	Rating     float64
	Reviews    int
	Website    string
	SocialOnly bool
	Priority   string
	Gaps       []string
	SiteTitle  string
	SiteText   string
	Contacts   []string
}

// Text renders the document. Keep it stable: changing this changes every hash
// and forces a full re-embed.
func (d LeadDocument) Text() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s", d.Name)
	if d.Category != "" {
		fmt.Fprintf(&b, ", a %s", d.Category)
	}
	if d.Address != "" {
		fmt.Fprintf(&b, ", located at %s", d.Address)
	}
	b.WriteString(".\n")

	if d.Reviews > 0 {
		fmt.Fprintf(&b, "Rated %.1f from %d reviews.\n", d.Rating, d.Reviews)
	}

	// State the web presence in words, not flags. "has no website" is a phrase a
	// query can actually match against; a boolean is not.
	switch {
	case d.SocialOnly:
		b.WriteString("Has no real website. Sells through social media only, so orders arrive as direct messages.\n")
	case d.Website == "":
		b.WriteString("Has no website at all.\n")
	default:
		fmt.Fprintf(&b, "Website: %s\n", d.Website)
		if d.SiteTitle != "" {
			fmt.Fprintf(&b, "Site title: %s\n", d.SiteTitle)
		}
	}

	if len(d.Gaps) > 0 {
		fmt.Fprintf(&b, "Missing from their online presence: %s.\n",
			strings.Join(humanise(d.Gaps), ", "))
	}
	if d.Priority != "" {
		fmt.Fprintf(&b, "Lead priority: %s.\n", d.Priority)
	}
	if len(d.Contacts) > 0 {
		fmt.Fprintf(&b, "Contactable at: %s\n", strings.Join(d.Contacts, ", "))
	}

	if d.SiteText != "" {
		b.WriteString("\nFrom their website:\n")
		b.WriteString(truncateWords(d.SiteText, 220))
	}

	return strings.TrimSpace(b.String())
}

// humanise turns rule names into the words someone would actually search for.
func humanise(gaps []string) []string {
	phrases := map[string]string{
		"no_website":          "no website",
		"social_only":         "no storefront, only social media",
		"broken_website":      "a website that does not load",
		"not_mobile_friendly": "no mobile-friendly site",
		"no_booking":          "no online booking or checkout",
		"ssl_missing":         "no HTTPS",
		"no_contact_form":     "no contact form",
		"email_missing":       "no email address",
		"no_social_links":     "no social media presence",
		"meta_missing":        "no search engine description",
		"phone_missing":       "no phone number",
	}

	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		if p, ok := phrases[g]; ok {
			out = append(out, p)
		} else {
			out = append(out, strings.ReplaceAll(g, "_", " "))
		}
	}
	return out
}

func truncateWords(s string, max int) string {
	words := strings.Fields(s)
	if len(words) <= max {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:max], " ") + "..."
}
