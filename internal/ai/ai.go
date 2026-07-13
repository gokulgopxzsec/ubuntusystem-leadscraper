package ai

import "context"

type Provider interface {
	AuditWebsite(ctx context.Context, req AuditRequest) (*AuditResponse, error)
	Name() string
}

type AuditRequest struct {
	URL         string
	HTMLContent string
	Title       string
	MetaTags    map[string]string
	StatusCode  int
}

type AuditResponse struct {
	QualityScore    int      `json:"quality_score"`
	SEOScore        int      `json:"seo_score"`
	MobileScore     int      `json:"mobile_score"`
	Issues          []string `json:"issues"`
	Recommendations []string `json:"recommendations"`
	Summary         string   `json:"summary"`
	ServicesToOffer []string `json:"services_to_offer"`
}
