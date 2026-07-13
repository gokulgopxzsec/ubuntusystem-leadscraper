package gemini

import (
	"context"

	"github.com/makeforme/leadscraper/internal/ai"
)

type Provider struct {
	apiKey string
	model  string
}

func NewProvider(apiKey, model string) *Provider {
	return &Provider{apiKey: apiKey, model: model}
}

func (p *Provider) Name() string { return "gemini" }

func (p *Provider) AuditWebsite(ctx context.Context, req ai.AuditRequest) (*ai.AuditResponse, error) {
	return &ai.AuditResponse{
		QualityScore: 5,
		SEOScore:     5,
		MobileScore:  5,
		Summary:      "AI analysis not yet implemented",
	}, nil
}
