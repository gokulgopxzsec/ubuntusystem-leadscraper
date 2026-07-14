package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

const openaiBaseURL = "https://api.openai.com/v1"

// OpenAI uses text-embedding-3-small, asked for 768 dimensions so it matches the
// schema. That model is Matryoshka-trained, so a truncated 768-vector is still a
// good vector — this is not lossy in the way truncating an ordinary embedding
// would be.
//
// Works against any OpenAI-compatible endpoint (OpenRouter, vLLM, Ollama) via
// BaseURL, though a local model must actually support the dimensions parameter.
type OpenAI struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewOpenAI(apiKey, model, baseURL string, timeout time.Duration) *OpenAI {
	if model == "" {
		model = "text-embedding-3-small"
	}
	if baseURL == "" {
		baseURL = openaiBaseURL
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &OpenAI{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

func (o *OpenAI) Name() string  { return "openai" }
func (o *OpenAI) Model() string { return o.model }

type openaiRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type openaiResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// OpenAI's embedding model is symmetric, so queries and documents are embedded
// the same way. There is no task type to get wrong.
func (o *OpenAI) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := o.EmbedDocuments(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("openai returned %d embeddings for one input", len(vecs))
	}
	return vecs[0], nil
}

func (o *OpenAI) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if o.apiKey == "" {
		return nil, ErrNotConfigured
	}
	if len(texts) == 0 {
		return nil, nil
	}

	payload, err := json.Marshal(openaiRequest{
		Model:      o.model,
		Input:      texts,
		Dimensions: Dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build openai request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embeddings request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read openai response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai embeddings: http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var out openaiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode openai embeddings: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("openai embeddings: %s: %s", out.Error.Type, out.Error.Message)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("openai returned %d embeddings for %d inputs", len(out.Data), len(texts))
	}

	// The API does not promise the results come back in request order, and it
	// gives an index precisely so we do not have to assume they do. Mixing up
	// which vector belongs to which lead would be silent and near-impossible to
	// debug from the search results.
	sort.Slice(out.Data, func(i, j int) bool { return out.Data[i].Index < out.Data[j].Index })

	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		if len(d.Embedding) != Dimensions {
			return nil, fmt.Errorf("openai returned %d dimensions, but the schema is fixed at %d "+
				"(does %q support the dimensions parameter?)", len(d.Embedding), Dimensions, o.model)
		}
		vecs[i] = d.Embedding
	}
	return vecs, nil
}
