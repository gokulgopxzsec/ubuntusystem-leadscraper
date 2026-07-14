package scoring

import (
	"strings"
	"testing"
)

func TestNoWebsiteIsTheStrongestLead(t *testing.T) {
	e := NewDefaultEngine()

	res := e.Evaluate(&EvalContext{
		HasPhone: true,
		// Everything else absent: no website at all.
	})

	if res.Priority() != "high" {
		t.Errorf("a trading business with no website should be a high priority lead, got %q (%d%%)",
			res.Priority(), res.Percent())
	}
	if res.Breakdown[RuleNoWebsite] == 0 {
		t.Error("the no_website rule should have fired")
	}
	// Rules gated on a reachable website must not fire when there is no website.
	if res.Breakdown[RuleBrokenWebsite] != 0 {
		t.Error("broken_website should not fire when there is no website to break")
	}
	if res.Breakdown[RuleNotMobile] != 0 {
		t.Error("not_mobile_friendly should not fire when there is no website")
	}
}

func TestHealthySiteIsALowPriorityLead(t *testing.T) {
	e := NewDefaultEngine()

	res := e.Evaluate(&EvalContext{
		HasWebsite:       true,
		IsReachable:      true,
		HasSSL:           true,
		HasPhone:         true,
		HasEmail:         true,
		HasContactForm:   true,
		HasMetaTags:      true,
		HasSocialLinks:   true,
		IsMobileFriendly: true,
		HasBooking:       true,
	})

	if res.TotalScore != 0 {
		t.Errorf("a site with no gaps should score 0, got %d (%v)", res.TotalScore, res.Reasons)
	}
	if res.Priority() != "low" {
		t.Errorf("expected low priority, got %q", res.Priority())
	}
}

func TestBrokenSiteBeatsAMerelyDatedSite(t *testing.T) {
	e := NewDefaultEngine()

	broken := e.Evaluate(&EvalContext{
		HasWebsite: true, HasPhone: true, IsReachable: false,
	})
	dated := e.Evaluate(&EvalContext{
		HasWebsite: true, HasPhone: true, IsReachable: true,
		HasSSL: true, HasMetaTags: true, IsMobileFriendly: true,
	})

	if broken.TotalScore <= dated.TotalScore {
		t.Errorf("a dead website (%d) should outrank a merely dated one (%d)",
			broken.TotalScore, dated.TotalScore)
	}
}

func TestNewEngineFallsBackToDefaultRules(t *testing.T) {
	// An engine built with no rules used to score everything zero, silently.
	res := NewEngine(nil).Evaluate(&EvalContext{})

	if res.MaxScore == 0 {
		t.Fatal("NewEngine(nil) should fall back to the default rules, not score nothing")
	}
}

func TestPercentIsSafeWithNoRules(t *testing.T) {
	// Guard the division in Percent().
	res := ScoreResult{}
	if got := res.Percent(); got != 0 {
		t.Errorf("Percent() on an empty result = %d, want 0", got)
	}
}

// A bakery selling through Instagram DMs is the single best lead there is: it is
// trading, and it has no storefront. It must outrank a business with no web
// presence at all, and must never be scored as though its website were broken.
func TestSocialOnlyIsTheStrongestLead(t *testing.T) {
	e := NewDefaultEngine()

	social := e.Evaluate(&EvalContext{
		SocialOnly: true, SocialPlatform: "instagram",
		HasPhone: true, HasSocialLinks: true,
	})

	if social.Breakdown[RuleSocialOnly] == 0 {
		t.Error("the social_only rule should have fired")
	}
	if social.Priority() != "high" {
		t.Errorf("priority = %q (%d%%), want high", social.Priority(), social.Percent())
	}

	// Website rules must not fire: they have no website to be broken. This is
	// the actual bug — a bakery whose "website" was a Facebook page was being
	// reported as having a site that "does not load".
	for _, r := range []Rule{RuleBrokenWebsite, RuleNoWebsite, RuleSSLMissing, RuleNotMobile} {
		if social.Breakdown[r] != 0 {
			t.Errorf("%s fired on a social-only business; it has no website at all", r)
		}
	}

	// Compare like for like. Both have a social presence and a phone; the only
	// difference is that one is actively selling on Instagram. Holding the other
	// signals equal is the only way the percentages share a denominator.
	noWeb := e.Evaluate(&EvalContext{HasPhone: true, HasSocialLinks: true})

	if social.Percent() <= noWeb.Percent() {
		t.Errorf("a business selling on Instagram (%d%%) should outrank one that is simply absent (%d%%)",
			social.Percent(), noWeb.Percent())
	}
}

func TestSocialOnlySuggestionNamesThePlatform(t *testing.T) {
	ctx := &EvalContext{SocialOnly: true, SocialPlatform: "instagram", HasPhone: true}
	got := NewDefaultEngine().Evaluate(ctx).SalesSuggestion("Bake Saga", ctx)

	if !strings.Contains(got, "instagram") {
		t.Errorf("the pitch should name the platform, got: %s", got)
	}
	if strings.Contains(strings.ToLower(got), "does not load") {
		t.Errorf("must not tell them their website is broken; they have no website: %s", got)
	}
}
