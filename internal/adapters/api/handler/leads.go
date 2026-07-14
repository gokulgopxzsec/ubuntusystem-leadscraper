package handler

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/makeforme/leadscraper/internal/adapters/db/postgres"
	"github.com/makeforme/leadscraper/internal/domain"
	"github.com/makeforme/leadscraper/internal/ports"
	"github.com/makeforme/leadscraper/internal/queue"
)

// LeadHandler is the dashboard's CRUD surface over the lead list.
type LeadHandler struct {
	leads      *postgres.LeadRepo
	businesses ports.BusinessRepository
	queue      queue.Queue
}

func NewLeadHandler(leads *postgres.LeadRepo, businesses ports.BusinessRepository, q queue.Queue) *LeadHandler {
	return &LeadHandler{leads: leads, businesses: businesses, queue: q}
}

func (h *LeadHandler) filter(r *http.Request) postgres.LeadFilter {
	q := r.URL.Query()

	return postgres.LeadFilter{
		Search:    q.Get("search"),
		Priority:  q.Get("priority"),
		Category:  q.Get("category"),
		Source:    q.Get("source"),
		Web:       postgres.WebPresence(q.Get("web")),
		MinScore:  queryInt(r, "min_score", 0),
		HasPhone:  q.Get("has_phone") == "true",
		HasEmail:  q.Get("has_email") == "true",
		SortBy:    q.Get("sort"),
		SortOrder: q.Get("order"),
		Page:      queryInt(r, "page", 1),
		Limit:     queryInt(r, "limit", 50),
	}
}

// List is the dashboard's main query. Unlike the old endpoint it returns every
// priority, including low: you cannot manage rows you cannot see.
func (h *LeadHandler) List(w http.ResponseWriter, r *http.Request) {
	f := h.filter(r)

	leads, total, err := h.leads.List(r.Context(), f)
	if err != nil {
		writeRepoError(w, err, "leads")
		return
	}
	if leads == nil {
		leads = []*postgres.Lead{}
	}

	writeJSON(w, http.StatusOK, listResponse{
		Data:  leads,
		Total: total,
		Page:  max(f.Page, 1),
		Limit: f.Limit,
	})
}

// Stats and Facets keep the dashboard's counters and dropdowns honest: they only
// ever offer values that exist in the data.
func (h *LeadHandler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.leads.Stats(r.Context())
	if err != nil {
		writeRepoError(w, err, "stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *LeadHandler) Facets(w http.ResponseWriter, r *http.Request) {
	categories, err := h.leads.Categories(r.Context())
	if err != nil {
		writeRepoError(w, err, "categories")
		return
	}
	sources, err := h.leads.Sources(r.Context())
	if err != nil {
		writeRepoError(w, err, "sources")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"categories": categories,
		"sources":    sources,
	})
}

// ---------------------------------------------------------------- create

type businessInput struct {
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Address  string   `json:"address"`
	Phone    string   `json:"phone"`
	Website  string   `json:"website"`
	Rating   *float64 `json:"rating"`
}

func (in businessInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if in.Rating != nil && (*in.Rating < 0 || *in.Rating > 5) {
		return fmt.Errorf("rating must be between 0 and 5")
	}
	return nil
}

// Create adds a lead by hand — one someone found off-platform, or a referral.
func (h *LeadHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in businessInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	b := &domain.Business{
		Name:     strings.TrimSpace(in.Name),
		Category: strings.TrimSpace(in.Category),
		Address:  strings.TrimSpace(in.Address),
		Phone:    strings.TrimSpace(in.Phone),
		Website:  normalizeURL(in.Website),
		Source:   "manual",
	}
	if in.Rating != nil {
		b.Rating = *in.Rating
	}
	// A manual lead still needs a dedup key, or re-adding the same shop makes a
	// second row.
	b.SourceKey = strings.ToLower(b.Name + "|" + b.Phone)

	if err := h.businesses.Create(r.Context(), b); err != nil {
		writeRepoError(w, err, "business")
		return
	}

	// Put it through the same pipeline as a scraped lead, so it gets crawled and
	// scored like everything else rather than sitting there unranked.
	h.enqueuePipeline(r, b)

	writeJSON(w, http.StatusCreated, b)
}

// ---------------------------------------------------------------- update

func (h *LeadHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	existing, err := h.businesses.GetByID(r.Context(), id)
	if err != nil {
		writeRepoError(w, err, "business")
		return
	}

	var in businessInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	websiteChanged := normalizeURL(in.Website) != existing.Website

	existing.Name = strings.TrimSpace(in.Name)
	existing.Category = strings.TrimSpace(in.Category)
	existing.Address = strings.TrimSpace(in.Address)
	existing.Phone = strings.TrimSpace(in.Phone)
	existing.Website = normalizeURL(in.Website)
	if in.Rating != nil {
		existing.Rating = *in.Rating
	}

	if err := h.businesses.Update(r.Context(), existing); err != nil {
		writeRepoError(w, err, "business")
		return
	}

	// Correcting the website is the main reason anyone edits a lead, and the old
	// score was derived from the old URL. Re-run the pipeline so the score is not
	// left describing a site that is no longer theirs.
	if websiteChanged {
		h.enqueuePipeline(r, existing)
	}

	writeJSON(w, http.StatusOK, existing)
}

// ---------------------------------------------------------------- delete

func (h *LeadHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.businesses.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeRepoError(w, err, "business")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

type bulkRequest struct {
	IDs []string `json:"ids"`
}

func (h *LeadHandler) BulkDelete(w http.ResponseWriter, r *http.Request) {
	var req bulkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids is required")
		return
	}

	n, err := h.leads.BulkDelete(r.Context(), req.IDs)
	if err != nil {
		writeRepoError(w, err, "leads")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// ---------------------------------------------------------------- rescan

// Rescan pushes leads back through the pipeline: re-crawl, re-score, re-embed.
// Websites change, and a score from last month may describe a site that no
// longer exists.
func (h *LeadHandler) Rescan(w http.ResponseWriter, r *http.Request) {
	var req bulkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids is required")
		return
	}

	queued := 0
	for _, id := range req.IDs {
		b, err := h.businesses.GetByID(r.Context(), id)
		if err != nil {
			continue
		}
		if h.enqueuePipeline(r, b) {
			queued++
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"queued": queued})
}

// enqueuePipeline sends a business to the crawl stage, or straight to scoring if
// there is nothing to crawl.
func (h *LeadHandler) enqueuePipeline(r *http.Request, b *domain.Business) bool {
	job := queue.Job{Type: queue.JobRuleScoring, BusinessID: b.ID}
	if b.Website != "" {
		job = queue.Job{Type: queue.JobWebsiteCrawl, BusinessID: b.ID, URL: b.Website}
	}

	if err := h.queue.Enqueue(r.Context(), job); err != nil {
		// The lead is saved; it simply will not be re-scored right now. Worth a
		// log, not worth failing the write the user just made.
		return false
	}
	return true
}

// ---------------------------------------------------------------- export

// Export writes the current filtered view as CSV, so a salesperson can take the
// list into whatever they actually work from.
func (h *LeadHandler) Export(w http.ResponseWriter, r *http.Request) {
	f := h.filter(r)
	f.Page, f.Limit = 1, 10000 // an export is the whole list, not a page of it

	leads, _, err := h.leads.List(r.Context(), f)
	if err != nil {
		writeRepoError(w, err, "leads")
		return
	}

	filename := fmt.Sprintf("leads-%s.csv", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{
		"name", "category", "address", "phone", "email", "website",
		"rating", "reviews", "score", "priority", "gaps", "pitch", "source",
	})

	for _, l := range leads {
		b := l.Business

		var meta struct {
			ReviewCount int `json:"review_count"`
		}
		if len(b.Metadata) > 0 {
			_ = json.Unmarshal(b.Metadata, &meta)
		}

		score, priority, gaps, pitch := "", "", "", ""
		if l.Score != nil {
			score = strconv.Itoa(l.Score.TotalScore)
			priority = l.Score.Priority
			pitch = l.Score.SalesSuggestion

			var fired []string
			for gap, weight := range l.Score.Breakdown {
				if weight > 0 {
					fired = append(fired, gap)
				}
			}
			gaps = strings.Join(fired, "; ")
		}

		_ = cw.Write([]string{
			b.Name, b.Category, b.Address, b.Phone, l.Email, b.Website,
			formatRating(b.Rating), strconv.Itoa(meta.ReviewCount),
			score, priority, gaps, pitch, b.Source,
		})
	}
}

func formatRating(r float64) string {
	if r == 0 {
		return ""
	}
	return strconv.FormatFloat(r, 'f', 1, 64)
}

// normalizeURL gives a bare host a scheme, so the crawler can actually fetch it.
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}
