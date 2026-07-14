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

func TestSocialOnlyDetectsAStorefrontThatIsNotOne(t *testing.T) {
	// Google Maps lets a business put anything in its website field, and small
	// sellers routinely put their Instagram there.
	socials := []string{
		"https://instagram.com/beurredevanille",
		"https://www.facebook.com/bakersnest/",
		"https://linktr.ee/chaicafe",
		"https://wa.me/919876543210",
	}
	for _, u := range socials {
		if _, ok := SocialOnly(u); !ok {
			t.Errorf("SocialOnly(%q) = false, want true: this is not a storefront", u)
		}
	}

	realSites := []string{
		"https://supremebakers.in/",
		"http://www.jamrolls.com/",
		"https://chaicafe.in/shop",
		"",
	}
	for _, u := range realSites {
		if _, ok := SocialOnly(u); ok {
			t.Errorf("SocialOnly(%q) = true, want false: this IS a real website", u)
		}
	}
}

func TestSocialOnlyNamesThePlatform(t *testing.T) {
	// The pitch says "they sell through <platform>", so the name has to be right.
	platform, ok := SocialOnly("https://www.instagram.com/beurredevanille")
	if !ok || platform != "instagram" {
		t.Errorf("SocialOnly() = (%q, %v), want (instagram, true)", platform, ok)
	}
}
