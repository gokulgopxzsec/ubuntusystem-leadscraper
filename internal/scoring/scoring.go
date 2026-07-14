package scoring

import "fmt"

type Rule string

const (
	RuleNoWebsite     Rule = "no_website"
	RuleSSLMissing    Rule = "ssl_missing"
	RulePhoneMissing  Rule = "phone_missing"
	RuleEmailMissing  Rule = "email_missing"
	RuleNoContactForm Rule = "no_contact_form"
	RuleMetaMissing   Rule = "meta_missing"
	RuleNoSocialLinks Rule = "no_social_links"
	RuleBrokenWebsite Rule = "broken_website"
	RuleNotMobile     Rule = "not_mobile_friendly"
	RuleNoBooking     Rule = "no_booking"
)

type RuleDef struct {
	Name Rule
	// Weight is what the gap is worth as a sales signal. A higher score means a
	// better prospect, not a better website.
	Weight int
	Reason string
	Eval   func(ctx *EvalContext) bool
	// Applies gates whether the rule is even meaningful for this business. A
	// business with no website cannot have a broken one, so broken_website must
	// not count toward the maximum there. Without this the denominator includes
	// rules that are impossible to trigger, and a business with no website at
	// all (the strongest lead there is) scored a middling 35%.
	//
	// A nil Applies means the rule always applies.
	Applies func(ctx *EvalContext) bool
}

func (r RuleDef) applies(ctx *EvalContext) bool {
	return r.Applies == nil || r.Applies(ctx)
}

// hasLiveSite is the precondition for every rule that judges page content: we
// cannot fault a site's meta tags if we never managed to load it.
func hasLiveSite(c *EvalContext) bool { return c.HasWebsite && c.IsReachable }

func hasSite(c *EvalContext) bool { return c.HasWebsite }

type EvalContext struct {
	HasWebsite       bool
	HasSSL           bool
	HasPhone         bool
	HasEmail         bool
	HasContactForm   bool
	HasMetaTags      bool
	HasSocialLinks   bool
	IsReachable      bool
	IsMobileFriendly bool
	HasBooking       bool
}

type ScoreResult struct {
	TotalScore int
	Breakdown  map[Rule]int
	MaxScore   int
	Reasons    []string
}

// Percent is the score as a share of the maximum, which is what the priority
// bands and the API actually care about.
func (r ScoreResult) Percent() int {
	if r.MaxScore == 0 {
		return 0
	}
	return r.TotalScore * 100 / r.MaxScore
}

// Priority buckets the percentage into the three bands the schema allows.
func (r ScoreResult) Priority() string {
	switch p := r.Percent(); {
	case p >= 60:
		return "high"
	case p >= 30:
		return "medium"
	default:
		return "low"
	}
}

// DefaultRules encodes the pitch: a business scores high when it is clearly
// trading but has no usable online storefront. A rule firing means the gap is
// there, which is a reason to call them.
func DefaultRules() []RuleDef {
	return []RuleDef{
		{
			Name: RuleNoWebsite, Weight: 30,
			Reason:  "No website at all. Nothing to migrate, everything to gain.",
			Eval:    func(c *EvalContext) bool { return !c.HasWebsite },
			Applies: func(c *EvalContext) bool { return !c.HasWebsite },
		},
		{
			Name: RuleBrokenWebsite, Weight: 25,
			Reason:  "Website does not load. They are paying for something that is down.",
			Eval:    func(c *EvalContext) bool { return !c.IsReachable },
			Applies: hasSite,
		},
		{
			Name: RuleNotMobile, Weight: 15,
			Reason:  "Not mobile friendly. Most Indian buyers arrive from a phone.",
			Eval:    func(c *EvalContext) bool { return !c.IsMobileFriendly },
			Applies: hasLiveSite,
		},
		{
			Name: RuleNoBooking, Weight: 12,
			Reason:  "No way to book or buy online. Orders are stuck in DMs and phone calls.",
			Eval:    func(c *EvalContext) bool { return !c.HasBooking },
			Applies: hasLiveSite,
		},
		{
			Name: RuleSSLMissing, Weight: 10,
			Reason:  "No HTTPS. Browsers actively warn buyers away.",
			Eval:    func(c *EvalContext) bool { return !c.HasSSL },
			Applies: hasSite,
		},
		{
			Name: RuleNoContactForm, Weight: 8,
			Reason:  "No contact form. Every enquiry depends on the visitor making the first move.",
			Eval:    func(c *EvalContext) bool { return !c.HasContactForm },
			Applies: hasLiveSite,
		},
		{
			Name: RuleEmailMissing, Weight: 6,
			Reason: "No published email address.",
			Eval:   func(c *EvalContext) bool { return !c.HasEmail },
		},
		{
			Name: RuleNoSocialLinks, Weight: 5,
			Reason: "No social presence linked. No organic channel driving demand.",
			Eval:   func(c *EvalContext) bool { return !c.HasSocialLinks },
		},
		{
			Name: RuleMetaMissing, Weight: 4,
			Reason:  "Missing meta description. Invisible in search results.",
			Eval:    func(c *EvalContext) bool { return !c.HasMetaTags },
			Applies: hasLiveSite,
		},
		{
			Name: RulePhoneMissing, Weight: 2,
			Reason: "No phone number listed, so harder to reach for outreach.",
			Eval:   func(c *EvalContext) bool { return !c.HasPhone },
		},
	}
}

type Engine struct {
	rules []RuleDef
}

// NewEngine falls back to the default rule set rather than returning an engine
// that silently scores everything zero.
func NewEngine(rules []RuleDef) *Engine {
	if len(rules) == 0 {
		rules = DefaultRules()
	}
	return &Engine{rules: rules}
}

func NewDefaultEngine() *Engine {
	return NewEngine(DefaultRules())
}

func (e *Engine) Evaluate(ctx *EvalContext) ScoreResult {
	res := ScoreResult{Breakdown: make(map[Rule]int, len(e.rules))}

	for _, rule := range e.rules {
		// A rule that cannot apply contributes to neither the score nor the
		// maximum, so the percentage stays a share of what was achievable.
		if !rule.applies(ctx) {
			res.Breakdown[rule.Name] = 0
			continue
		}

		res.MaxScore += rule.Weight

		if rule.Eval(ctx) {
			res.Breakdown[rule.Name] = rule.Weight
			res.TotalScore += rule.Weight
			res.Reasons = append(res.Reasons, rule.Reason)
		} else {
			res.Breakdown[rule.Name] = 0
		}
	}

	return res
}

// SalesSuggestion turns the highest-weighted gap into a one-line opener.
func (r ScoreResult) SalesSuggestion(businessName string) string {
	if len(r.Reasons) == 0 {
		return fmt.Sprintf("%s already has a solid online presence. Low priority.", businessName)
	}

	lead := r.Reasons[0]
	switch r.Priority() {
	case "high":
		return fmt.Sprintf("%s is a strong lead. %s Lead with a live store link they can share on Instagram today.", businessName, lead)
	case "medium":
		return fmt.Sprintf("%s is worth a call. %s Offer to close that gap in under 10 minutes.", businessName, lead)
	default:
		return fmt.Sprintf("%s is a soft lead. %s Worth a light touch only.", businessName, lead)
	}
}
