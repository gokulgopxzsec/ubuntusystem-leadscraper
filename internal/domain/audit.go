package domain

import "time"

type AuditReport struct {
	ID              string    `json:"id"`
	WebsiteID       string    `json:"website_id"`
	BusinessID      string    `json:"business_id"`
	QualityScore    int       `json:"quality_score"`
	SEOScore        int       `json:"seo_score"`
	MobileScore     int       `json:"mobile_score"`
	Issues          []string  `json:"issues"`
	Recommendations []string  `json:"recommendations"`
	Summary         string    `json:"summary"`
	ServicesToOffer []string  `json:"services_to_offer"`
	AuditedAt       time.Time `json:"audited_at"`
	CreatedAt       time.Time `json:"created_at"`
}
