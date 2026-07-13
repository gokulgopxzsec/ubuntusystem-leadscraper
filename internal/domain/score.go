package domain

import "time"

type LeadScore struct {
	ID              string            `json:"id"`
	BusinessID      string            `json:"business_id"`
	TotalScore      int               `json:"total_score"`
	RuleScore       int               `json:"rule_score"`
	AIScore         int               `json:"ai_score"`
	Priority        string            `json:"priority"`
	Breakdown       map[string]int    `json:"breakdown"`
	SalesSuggestion string            `json:"sales_suggestion,omitempty"`
	ScoredAt        time.Time         `json:"scored_at"`
	CreatedAt       time.Time         `json:"created_at"`
}
