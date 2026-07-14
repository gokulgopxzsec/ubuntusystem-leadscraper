package embed

import (
	"strings"
	"testing"
)

// The embedded text is what retrieval actually matches against, so it has to
// state the web-presence gaps in words. "bakeries with no website" is the whole
// point of this feature, and a boolean flag cannot be matched by a query.
func TestLeadDocumentStatesGapsInWords(t *testing.T) {
	doc := LeadDocument{
		Name:     "Jaya Bakery",
		Category: "bakery",
		Address:  "MG Road, Kochi",
		Reviews:  88,
		Rating:   4.4,
		Gaps:     []string{"no_website", "email_missing"},
		Priority: "high",
	}

	text := doc.Text()

	for _, want := range []string{"Jaya Bakery", "bakery", "Kochi", "no website", "no email address"} {
		if !strings.Contains(text, want) {
			t.Errorf("embedded text should mention %q, got:\n%s", want, text)
		}
	}
	// Rule names are not English and would not match a natural-language query.
	if strings.Contains(text, "no_website") {
		t.Errorf("raw rule names must not leak into the embedded text:\n%s", text)
	}
}

func TestSocialOnlyIsDescribedForRetrieval(t *testing.T) {
	// "selling through Instagram" is a thing a salesperson would actually search
	// for, so the document has to say it in those words.
	text := LeadDocument{
		Name:       "Beurre De Vanille",
		Website:    "https://instagram.com/beurredevanille",
		SocialOnly: true,
	}.Text()

	lower := strings.ToLower(text)
	if !strings.Contains(lower, "social media") {
		t.Errorf("a social-only lead should say so in words, got:\n%s", text)
	}
	if !strings.Contains(lower, "no real website") {
		t.Errorf("a social-only lead has no storefront and the text should say so:\n%s", text)
	}
}

func TestNoWebsiteIsStatedExplicitly(t *testing.T) {
	text := LeadDocument{Name: "Society Bakery", Category: "bakery"}.Text()

	if !strings.Contains(strings.ToLower(text), "no website at all") {
		t.Errorf("expected the absence of a website to be stated, got:\n%s", text)
	}
}

// The hash is what stops us re-embedding an unchanged lead on every rescore, so
// identical documents must hash identically and different ones must not.
func TestHashIsStableAndDiscriminating(t *testing.T) {
	a := LeadDocument{Name: "Jaya Bakery", Gaps: []string{"no_website"}}
	b := LeadDocument{Name: "Jaya Bakery", Gaps: []string{"no_website"}}
	c := LeadDocument{Name: "Jaya Bakery", Gaps: []string{"no_website", "ssl_missing"}}

	if Hash(a.Text()) != Hash(b.Text()) {
		t.Error("identical leads must hash identically, or every rescore re-embeds")
	}
	if Hash(a.Text()) == Hash(c.Text()) {
		t.Error("a changed lead must hash differently, or it would never be re-embedded")
	}
}

func TestNoopReportsThatItIsUnconfigured(t *testing.T) {
	if Enabled(Noop{}) {
		t.Error("the noop provider must not report itself as enabled")
	}
	// The parens are required: a bare composite literal in an if header parses
	// as the start of the block.
	if _, err := (Noop{}).EmbedQuery(t.Context(), "x"); err == nil {
		t.Error("the noop provider must not return a fabricated vector")
	}
}
