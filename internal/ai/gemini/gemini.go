package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/makeforme/leadscraper/internal/ai"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

type Provider struct {
	apiKey       string
	model        string
	baseURL      string
	maxHTMLChars int
	client       *http.Client
}

type Options struct {
	BaseURL      string
	Timeout      time.Duration
	MaxHTMLChars int
}

func NewProvider(apiKey, model string, opts Options) *Provider {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	if opts.BaseURL == "" {
		opts.BaseURL = defaultBaseURL
	}
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.MaxHTMLChars == 0 {
		opts.MaxHTMLChars = 12000
	}

	return &Provider{
		apiKey:       apiKey,
		model:        model,
		baseURL:      opts.BaseURL,
		maxHTMLChars: opts.MaxHTMLChars,
		client:       &http.Client{Timeout: opts.Timeout},
	}
}

func (p *Provider) Name() string { return "gemini" }

type generateRequest struct {
	Contents          []content        `json:"contents"`
	SystemInstruction *content         `json:"systemInstruction,omitempty"`
	GenerationConfig  generationConfig `json:"generationConfig"`
}

type content struct {
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text"`
}

type generationConfig struct {
	Temperature      float64 `json:"temperature"`
	MaxOutputTokens  int     `json:"maxOutputTokens"`
	ResponseMIMEType string  `json:"responseMimeType"`
}

type generateResponse struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (p *Provider) AuditWebsite(ctx context.Context, req ai.AuditRequest) (*ai.AuditResponse, error) {
	if p.apiKey == "" {
		return nil, ai.ErrNotConfigured
	}

	req.HTMLContent = ai.TruncateHTML(req.HTMLContent, p.maxHTMLChars)

	body, err := json.Marshal(generateRequest{
		Contents:          []content{{Parts: []part{{Text: ai.BuildPrompt(req)}}}},
		SystemInstruction: &content{Parts: []part{{Text: ai.SystemPrompt}}},
		GenerationConfig: generationConfig{
			Temperature:     0.2,
			MaxOutputTokens: 1024,
			// Ask the API to enforce JSON rather than trusting the prompt.
			ResponseMIMEType: "application/json",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent", p.baseURL, url.PathEscape(p.model))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build gemini request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// The key goes in a header, not the query string, so it stays out of logs.
	httpReq.Header.Set("x-goog-api-key", p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read gemini response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: http %d: %s", resp.StatusCode, ai.Truncate(string(raw), 300))
	}

	var out generateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode gemini response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("gemini: %s: %s", out.Error.Status, out.Error.Message)
	}
	if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
		return nil, fmt.Errorf("gemini blocked the prompt: %s", out.PromptFeedback.BlockReason)
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini returned no candidates")
	}

	return ai.ParseAuditJSON(out.Candidates[0].Content.Parts[0].Text)
}
