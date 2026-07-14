package handler

import (
	"encoding/json"
	"testing"

	"github.com/makeforme/leadscraper/internal/domain"
)

// businessDetail embeds *domain.Business, which already carries a `website`
// string. Naming the nested crawl result `website` too made the outer field win
// the collision, silently dropping the business's URL from the response. The
// nested object is called `site` for that reason; this guards the rename.
func TestBusinessDetailDoesNotShadowTheWebsiteURL(t *testing.T) {
	detail := businessDetail{
		Business: &domain.Business{
			ID:      "b1",
			Name:    "Chai Corner",
			Website: "https://chaicafe.in",
		},
		Site: &domain.Website{
			ID:         "w1",
			URL:        "https://chaicafe.in",
			StatusCode: 200,
		},
		Contacts: []*domain.Contact{},
		Socials:  []*domain.SocialProfile{},
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	website, ok := got["website"].(string)
	if !ok {
		t.Fatalf("`website` should be the business's URL string, got %T (%v)", got["website"], got["website"])
	}
	if website != "https://chaicafe.in" {
		t.Errorf("website = %q, want the business URL", website)
	}

	site, ok := got["site"].(map[string]any)
	if !ok {
		t.Fatalf("`site` should be the crawled website object, got %T", got["site"])
	}
	if site["status_code"] != float64(200) {
		t.Errorf("site.status_code = %v, want 200", site["status_code"])
	}
}

// A business that has never been crawled has no site, no score, and no audit.
// Those must be omitted rather than serialised as null.
func TestBusinessDetailOmitsMissingRelations(t *testing.T) {
	raw, err := json.Marshal(businessDetail{
		Business: &domain.Business{ID: "b1", Name: "Chai Corner"},
		Contacts: []*domain.Contact{},
		Socials:  []*domain.SocialProfile{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"site", "score", "audit"} {
		if _, present := got[key]; present {
			t.Errorf("%q should be omitted when absent, not serialised as null", key)
		}
	}

	// Collections stay as [] so clients never have to null-check them.
	if _, ok := got["contacts"].([]any); !ok {
		t.Errorf("contacts should encode as [], got %T", got["contacts"])
	}
}
