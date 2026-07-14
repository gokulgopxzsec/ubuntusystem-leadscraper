package extract

import "testing"

func TestSocialsKeepsProfilesAndDropsChrome(t *testing.T) {
	links := []string{
		"https://www.instagram.com/chaicafe.blr",
		"https://www.facebook.com/sharer/sharer.php?u=https://chaicafe.in",
		"https://twitter.com/intent/tweet?text=hi",
		"https://www.linkedin.com/company/chaicafe",
		"https://linkedin.com/legal/privacy-policy",
		"https://instagram.com/p/Cabcdef123",
		"https://example.com/about",
	}

	got := Socials("biz-1", links)

	found := map[string]string{}
	for _, s := range got {
		found[s.Platform] = s.URL
	}

	if len(found) != 2 {
		t.Fatalf("expected instagram + linkedin only, got %+v", found)
	}
	if _, ok := found["instagram"]; !ok {
		t.Error("expected the instagram profile to be kept")
	}
	if _, ok := found["linkedin"]; !ok {
		t.Error("expected the linkedin company page to be kept")
	}
	if _, ok := found["facebook"]; ok {
		t.Error("a share button is not a profile")
	}
	if _, ok := found["twitter"]; ok {
		t.Error("an intent link is not a profile")
	}
}

func TestSocialsDeduplicatesPerPlatform(t *testing.T) {
	links := []string{
		"https://instagram.com/chaicafe",
		"https://www.instagram.com/chaicafe/",
	}

	// The schema is UNIQUE(business_id, platform), so a second instagram row in
	// the same batch would collide on upsert.
	if got := Socials("biz-1", links); len(got) != 1 {
		t.Errorf("expected 1 profile after dedup, got %d", len(got))
	}
}
