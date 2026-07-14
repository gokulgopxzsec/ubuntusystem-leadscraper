package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// Gemini uses text-embedding-004, which is natively 768-dimensional — the size
// the lead_embeddings column is fixed at.
type Gemini struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewGemini(apiKey, model, baseURL string, timeout time.Duration) *Gemini {
	if model == "" {
		model = "text-embedding-004"
	}
	if baseURL == "" {
		baseURL = geminiBaseURL
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Gemini{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

func (g *Gemini) Name() string  { return "gemini" }
func (g *Gemini) Model() string { return g.model }

// taskType tells an asymmetric model which side of the pair it is embedding.
// Embedding a question as though it were a document measurably degrades recall.
const (
	taskDocument = "RETRIEVAL_DOCUMENT"
	taskQuery    = "RETRIEVAL_QUERY"
)

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiEmbedRequest struct {
	Model    string        `json:"model"`
	Content  geminiContent `json:"content"`
	TaskType string        `json:"taskType,omitempty"`
}

type geminiBatchRequest struct {
	Requests []geminiEmbedRequest `json:"requests"`
}

type geminiEmbedding struct {
	Values []float32 `json:"values"`
}

type geminiBatchResponse struct {
	Embeddings []geminiEmbedding `json:"embeddings"`
	Error      *geminiError      `json:"error"`
}

type geminiSingleResponse struct {
	Embedding geminiEmbedding `json:"embedding"`
	Error     *geminiError    `json:"error"`
}

type geminiError struct {
	Message string `json:"message"`
	Status  string `json:"status"`
}

func (g *Gemini) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if g.apiKey == "" {
		return nil, ErrNotConfigured
	}
	if len(texts) == 0 {
		return nil, nil
	}

	model := "models/" + g.model

	reqs := make([]geminiEmbedRequest, len(texts))
	for i, t := range texts {
		reqs[i] = geminiEmbedRequest{
			Model:    model,
			Content:  geminiContent{Parts: []geminiPart{{Text: t}}},
			TaskType: taskDocument,
		}
	}

	raw, err := g.post(ctx,
		fmt.Sprintf("%s/%s:batchEmbedContents", g.baseURL, url.PathEscape(model)),
		geminiBatchRequest{Requests: reqs})
	if err != nil {
		return nil, err
	}

	var resp geminiBatchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode gemini embeddings: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("gemini embeddings: %s: %s", resp.Error.Status, resp.Error.Message)
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("gemini returned %d embeddings for %d inputs",
			len(resp.Embeddings), len(texts))
	}

	out := make([][]float32, len(resp.Embeddings))
	for i, e := range resp.Embeddings {
		if len(e.Values) != Dimensions {
			return nil, fmt.Errorf("gemini returned %d dimensions, but the schema is fixed at %d "+
				"(is EMBED_MODEL still text-embedding-004?)", len(e.Values), Dimensions)
		}
		out[i] = e.Values
	}
	return out, nil
}

func (g *Gemini) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if g.apiKey == "" {
		return nil, ErrNotConfigured
	}

	model := "models/" + g.model

	raw, err := g.post(ctx,
		fmt.Sprintf("%s/%s:embedContent", g.baseURL, url.PathEscape(model)),
		geminiEmbedRequest{
			Model:    model,
			Content:  geminiContent{Parts: []geminiPart{{Text: text}}},
			TaskType: taskQuery,
		})
	if err != nil {
		return nil, err
	}

	var resp geminiSingleResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode gemini embedding: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("gemini embeddings: %s: %s", resp.Error.Status, resp.Error.Message)
	}
	if len(resp.Embedding.Values) != Dimensions {
		return nil, fmt.Errorf("gemini returned %d dimensions, want %d",
			len(resp.Embedding.Values), Dimensions)
	}
	return resp.Embedding.Values, nil
}

func (g *Gemini) post(ctx context.Context, endpoint string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The key travels in a header rather than the query string so it stays out
	// of logs and proxies.
	req.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini embeddings request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read gemini response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini embeddings: http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	return raw, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
