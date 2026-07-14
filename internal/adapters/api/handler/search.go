package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/makeforme/leadscraper/internal/adapters/db/postgres"
	"github.com/makeforme/leadscraper/internal/embed"
)

// SearchHandler answers natural-language questions about the lead corpus:
// "bakeries with no website", "tailors selling through Instagram".
type SearchHandler struct {
	embeddings    *postgres.EmbeddingRepo
	embedder      embed.Provider
	minSimilarity float64
}

func NewSearchHandler(embeddings *postgres.EmbeddingRepo, embedder embed.Provider, minSimilarity float64) *SearchHandler {
	return &SearchHandler{
		embeddings:    embeddings,
		embedder:      embedder,
		minSimilarity: minSimilarity,
	}
}

type searchRequest struct {
	Query    string  `json:"query"`
	Priority string  `json:"priority"`
	Category string  `json:"category"`
	Source   string  `json:"source"`
	Limit    int     `json:"limit"`
	MinScore float64 `json:"min_similarity"`
}

type searchResponse struct {
	// Mode says which search actually ran. Semantic and keyword results are not
	// comparable, and a caller that cannot tell them apart will misread a
	// keyword fallback as a poor semantic match.
	Mode    string               `json:"mode"`
	Query   string               `json:"query"`
	Results []postgres.SearchHit `json:"results"`
	Total   int                  `json:"total"`
	Note    string               `json:"note,omitempty"`
}

func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	filter := postgres.SearchFilter{
		Priority:      req.Priority,
		Category:      req.Category,
		Source:        req.Source,
		Limit:         req.Limit,
		MinSimilarity: h.minSimilarity,
	}
	if req.MinScore > 0 {
		filter.MinSimilarity = req.MinScore
	}

	// Without an embedding provider, fall back to keyword matching rather than
	// returning an error. A search box that says "not configured" is useless; one
	// that finds fewer things is still a search box.
	if !embed.Enabled(h.embedder) {
		h.keyword(w, r, req, filter, "No embedding provider configured, so this was a keyword search. Set EMBED_PROVIDER for semantic search.")
		return
	}

	vec, err := h.embedder.EmbedQuery(r.Context(), req.Query)
	if err != nil {
		if errors.Is(err, embed.ErrNotConfigured) {
			h.keyword(w, r, req, filter, "No embedding provider configured, so this was a keyword search.")
			return
		}
		// The provider is down or rate limited. Degrade rather than fail.
		h.keyword(w, r, req, filter, "The embedding provider failed, so this fell back to a keyword search: "+err.Error())
		return
	}

	hits, err := h.embeddings.SemanticSearch(r.Context(), vec, filter)
	if err != nil {
		writeRepoError(w, err, "search")
		return
	}

	note := ""
	if len(hits) == 0 {
		// The most likely explanation by far, and otherwise a baffling result.
		note = "No matches. Leads are only searchable once they have been embedded, which happens at the end of the pipeline."
	}

	writeJSON(w, http.StatusOK, searchResponse{
		Mode:    "semantic",
		Query:   req.Query,
		Results: nonNilHits(hits),
		Total:   len(hits),
		Note:    note,
	})
}

func (h *SearchHandler) keyword(w http.ResponseWriter, r *http.Request, req searchRequest, filter postgres.SearchFilter, note string) {
	hits, err := h.embeddings.KeywordSearch(r.Context(), req.Query, filter)
	if err != nil {
		writeRepoError(w, err, "search")
		return
	}

	writeJSON(w, http.StatusOK, searchResponse{
		Mode:    "keyword",
		Query:   req.Query,
		Results: nonNilHits(hits),
		Total:   len(hits),
		Note:    note,
	})
}

// Status tells the UI whether semantic search is actually usable, so it can say
// so instead of silently behaving like a worse search box.
func (h *SearchHandler) Status(w http.ResponseWriter, r *http.Request) {
	embedded, err := h.embeddings.Count(r.Context())
	if err != nil {
		writeRepoError(w, err, "search status")
		return
	}

	mode := "keyword"
	model := ""
	if embed.Enabled(h.embedder) {
		mode = "semantic"
		model = h.embedder.Model()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":           mode,
		"model":          model,
		"embedded_leads": embedded,
	})
}

func nonNilHits(h []postgres.SearchHit) []postgres.SearchHit {
	if h == nil {
		return []postgres.SearchHit{}
	}
	return h
}
