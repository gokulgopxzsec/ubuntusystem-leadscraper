package scoring

type Rule string

const (
	RuleNoWebsite      Rule = "no_website"
	RuleSSLMissing     Rule = "ssl_missing"
	RulePhoneMissing   Rule = "phone_missing"
	RuleEmailMissing   Rule = "email_missing"
	RuleNoContactForm  Rule = "no_contact_form"
	RuleMetaMissing    Rule = "meta_missing"
	RuleNoSocialLinks  Rule = "no_social_links"
	RuleBrokenWebsite  Rule = "broken_website"
)

type RuleDef struct {
	Name   Rule
	Weight int
	Eval   func(ctx *EvalContext) bool
}

type EvalContext struct {
	HasWebsite   bool
	HasSSL       bool
	HasPhone     bool
	HasEmail     bool
	HasContactForm bool
	HasMetaTags  bool
	HasSocialLinks bool
	IsReachable  bool
}

type ScoreResult struct {
	TotalScore int
	Breakdown  map[Rule]int
	MaxScore   int
}

type Engine struct {
	rules []RuleDef
}

func NewEngine(rules []RuleDef) *Engine {
	return &Engine{rules: rules}
}

func (e *Engine) Evaluate(ctx *EvalContext) ScoreResult {
	breakdown := make(map[Rule]int)
	total := 0
	maxScore := 0

	for _, rule := range e.rules {
		maxScore += rule.Weight
		if rule.Eval(ctx) {
			breakdown[rule.Name] = rule.Weight
			total += rule.Weight
		} else {
			breakdown[rule.Name] = 0
		}
	}

	return ScoreResult{
		TotalScore: total,
		Breakdown:  breakdown,
		MaxScore:   maxScore,
	}
}
