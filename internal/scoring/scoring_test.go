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

	// SiteDown, not merely !IsReachable: a site that blocked our crawler is also
	// "not reachable" and is not broken at all.
	broken := e.Evaluate(&EvalContext{
		HasWebsite: true, HasPhone: true, SiteDown: true,
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

// A site that answered our crawler with 403 is running perfectly well. Scoring it
// as "your website does not load" put that sentence in front of businesses whose
// website was fine -- the fastest way to end a sales call.
func TestBlockedSiteIsNotScoredAsBroken(t *testing.T) {
	e := NewDefaultEngine()

	blocked := e.Evaluate(&EvalContext{
		HasWebsite: true, HasSSL: true, HasPhone: true,
		SiteOpaque: true, // 403 / bot wall: up, but we saw nothing
	})

	if blocked.Breakdown[RuleBrokenWebsite] != 0 {
		t.Error("broken_website fired on a site that merely blocked the crawler; it is up")
	}
	// Nor may we judge content we never saw.
	for _, r := range []Rule{RuleNotMobile, RuleMetaMissing, RuleNoContactForm, RuleNoBooking} {
		if blocked.Breakdown[r] != 0 {
			t.Errorf("%s fired on a page we were never able to read", r)
		}
	}
}

func TestGenuinelyDeadSiteStillScores(t *testing.T) {
	// The real signal must survive the fix.
	dead := NewDefaultEngine().Evaluate(&EvalContext{
		HasWebsite: true, HasPhone: true, SiteDown: true,
	})

	if dead.Breakdown[RuleBrokenWebsite] == 0 {
		t.Fatal("a site that does not load must still fire broken_website")
	}
	if dead.Priority() != "high" {
		t.Errorf("a dead website is a strong lead, got %q (%d%%)", dead.Priority(), dead.Percent())
	}
}

func TestRobotsDisallowedSiteIsNotABrokenSite(t *testing.T) {
	// We chose not to look. That says nothing about the site.
	res := NewDefaultEngine().Evaluate(&EvalContext{
		HasWebsite: true, HasSSL: true, HasPhone: true, SiteOpaque: true,
	})

	if res.Breakdown[RuleBrokenWebsite] != 0 {
		t.Error("a site we declined to crawl must not be reported as broken")
	}
}

// Fixing the false "site down" created a second, subtler lie. A blocked site has
// almost no applicable rules, so the denominator collapses and the two or three
// that do fire give a near-perfect percentage: a bot-walled site scored 91% and
// outranked businesses whose website was genuinely dead.
func TestBlockedSiteIsNotPresentedAsAStrongLead(t *testing.T) {
	e := NewDefaultEngine()

	blocked := e.Evaluate(&EvalContext{
		HasWebsite: true, HasPhone: true,
		SiteOpaque: true, // 403: up, and we saw nothing
	})
	dead := e.Evaluate(&EvalContext{
		HasWebsite: true, HasPhone: true, SiteDown: true,
	})

	if !blocked.Unassessed {
		t.Fatal("a site we could not read must be marked unassessed")
	}
	if blocked.Priority() == "high" {
		t.Errorf("a site we could not even read is not a high-priority lead (scored %d%%)",
			blocked.Percent())
	}
	if dead.Priority() != "high" {
		t.Errorf("a genuinely dead website is still a strong lead, got %q", dead.Priority())
	}
}

func TestUnassessedSuggestionAdmitsWeCouldNotLook(t *testing.T) {
	ctx := &EvalContext{HasWebsite: true, HasPhone: true, SiteOpaque: true}
	got := NewDefaultEngine().Evaluate(ctx).SalesSuggestion("Al Naboodah", ctx)

	if strings.Contains(strings.ToLower(got), "does not load") {
		t.Errorf("must not claim a working website is down: %s", got)
	}
	if !strings.Contains(strings.ToLower(got), "could not assess") {
		t.Errorf("should say plainly that we could not look, got: %s", got)
	}
}

// Capping the priority band was not enough on its own: the dashboard sorts by
// score, so an unassessed lead with an inflated percentage still floated above
// leads we actually knew something about.
func TestUnassessedLeadDoesNotOutrankAKnownOne(t *testing.T) {
	e := NewDefaultEngine()

	blocked := e.Evaluate(&EvalContext{HasWebsite: true, HasPhone: true, SiteOpaque: true})
	dead := e.Evaluate(&EvalContext{HasWebsite: true, HasPhone: true, SiteDown: true})

	if blocked.RankedScore() >= dead.RankedScore() {
		t.Errorf("a lead we could not assess (%d) must not outrank one we know is dead (%d)",
			blocked.RankedScore(), dead.RankedScore())
	}
	// The raw percentage is still reported honestly; only the ranking is damped.
	if blocked.Percent() <= dead.Percent() {
		t.Skip("this test is only meaningful while the collapsed denominator inflates the percentage")
	}
}
