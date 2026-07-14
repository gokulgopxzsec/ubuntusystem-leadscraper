package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/makeforme/leadscraper/internal/ai"
)

const defaultBaseURL = "https://api.openai.com/v1"

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

// NewProvider also serves any OpenAI-compatible endpoint (Ollama, vLLM,
// OpenRouter) by pointing Options.BaseURL at it.
func NewProvider(apiKey, model string, opts Options) *Provider {
	if model == "" {
		model = "gpt-4o-mini"
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

func (p *Provider) Name() string { return "openai" }

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []message      `json:"messages"`
	Temperature    float64        `json:"temperature"`
	MaxTokens      int            `json:"max_tokens"`
	ResponseFormat responseFormat `json:"response_format"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (p *Provider) AuditWebsite(ctx context.Context, req ai.AuditRequest) (*ai.AuditResponse, error) {
	if p.apiKey == "" {
		return nil, ai.ErrNotConfigured
	}

	req.HTMLContent = ai.TruncateHTML(req.HTMLContent, p.maxHTMLChars)

	body, err := json.Marshal(chatRequest{
		Model: p.model,
		Messages: []message{
			{Role: "system", Content: ai.SystemPrompt},
			{Role: "user", Content: ai.BuildPrompt(req)},
		},
		Temperature: 0.2,
		MaxTokens:   1024,
		// Enforce JSON at the API rather than trusting the prompt.
		ResponseFormat: responseFormat{Type: "json_object"},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build openai request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read openai response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: http %d: %s", resp.StatusCode, ai.Truncate(string(raw), 300))
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("openai: %s: %s", out.Error.Type, out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openai returned no choices")
	}

	return ai.ParseAuditJSON(out.Choices[0].Message.Content)
}
