package scoring

import "testing"

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
