package ai

import "testing"

func TestParseAuditJSONHandlesModelWrapping(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"bare json", `{"quality_score":3,"seo_score":2,"mobile_score":1,"summary":"weak"}`},
		{"fenced json", "```json\n{\"quality_score\":3,\"seo_score\":2,\"mobile_score\":1,\"summary\":\"weak\"}\n```"},
		{"unlabelled fence", "```\n{\"quality_score\":3,\"seo_score\":2,\"mobile_score\":1,\"summary\":\"weak\"}\n```"},
		{"chatty preamble", "Here is the audit:\n{\"quality_score\":3,\"seo_score\":2,\"mobile_score\":1,\"summary\":\"weak\"}"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAuditJSON(tc.raw)
			if err != nil {
				t.Fatalf("ParseAuditJSON() error = %v", err)
			}
			if got.QualityScore != 3 || got.SEOScore != 2 || got.MobileScore != 1 {
				t.Errorf("scores not parsed: %+v", got)
			}
		})
	}
}

func TestParseAuditJSONClampsOutOfRangeScores(t *testing.T) {
	// Models do not reliably honour the 0-10 range they are given.
	got, err := ParseAuditJSON(`{"quality_score":95,"seo_score":-4,"mobile_score":7}`)
	if err != nil {
		t.Fatalf("ParseAuditJSON() error = %v", err)
	}

	if got.QualityScore != 10 {
		t.Errorf("quality_score = %d, want it clamped to 10", got.QualityScore)
	}
	if got.SEOScore != 0 {
		t.Errorf("seo_score = %d, want it clamped to 0", got.SEOScore)
	}
	if got.MobileScore != 7 {
		t.Errorf("mobile_score = %d, want 7 unchanged", got.MobileScore)
	}
}

func TestParseAuditJSONRejectsProse(t *testing.T) {
	if _, err := ParseAuditJSON("I could not analyse that website."); err == nil {
		t.Fatal("expected an error when the model returns no JSON at all")
	}
}

func TestTruncateHTMLCutsOnRuneBoundary(t *testing.T) {
	// Slicing a multi-byte string by bytes would emit invalid UTF-8 and the API
	// would reject the request.
	got := TruncateHTML("चाय कॉफी शॉप", 4)

	if len([]rune(got)) != 4 {
		t.Errorf("expected 4 runes, got %d (%q)", len([]rune(got)), got)
	}
}

func TestNoopProviderReportsItIsUnconfigured(t *testing.T) {
	// The old stub returned invented scores of 5/5/5, which polluted the data.
	_, err := Noop{}.AuditWebsite(t.Context(), AuditRequest{})
	if err == nil {
		t.Fatal("the noop provider must not return a fabricated audit")
	}
}
