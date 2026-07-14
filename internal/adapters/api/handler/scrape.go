package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/makeforme/leadscraper/internal/domain"
	"github.com/makeforme/leadscraper/internal/ports"
	"github.com/makeforme/leadscraper/internal/queue"
)

type ScrapeHandler struct {
	jobs    ports.ScrapeJobRepository
	queue   queue.Queue
	sources map[string]ports.SourceAdapter
}

func NewScrapeHandler(jobs ports.ScrapeJobRepository, q queue.Queue, sources map[string]ports.SourceAdapter) *ScrapeHandler {
	return &ScrapeHandler{jobs: jobs, queue: q, sources: sources}
}

type scrapeRequest struct {
	Source   string `json:"source"`
	Category string `json:"category"`
	Location string `json:"location"`
	Query    string `json:"query"`
	File     string `json:"file"`
	Limit    int    `json:"limit"`
}

// Create records a scrape job and puts it on the queue. It returns 202: the
// work happens in the worker, and the caller polls the job for progress.
func (h *ScrapeHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req scrapeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required (one of: "+strings.Join(h.sourceNames(), ", ")+")")
		return
	}
	if _, ok := h.sources[req.Source]; !ok {
		writeError(w, http.StatusBadRequest, "unknown source "+req.Source+" (want one of: "+strings.Join(h.sourceNames(), ", ")+")")
		return
	}
	if req.Source == "csv" && req.File == "" {
		writeError(w, http.StatusBadRequest, "csv source requires a file")
		return
	}
	if req.Source != "csv" && req.Category == "" && req.Query == "" {
		writeError(w, http.StatusBadRequest, "a category or query is required")
		return
	}
	if req.Limit <= 0 || req.Limit > 500 {
		req.Limit = 60
	}

	params, err := json.Marshal(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	job := &domain.ScrapeJob{
		Source:   req.Source,
		Category: req.Category,
		Location: req.Location,
		Status:   "pending",
		Params:   params,
	}
	if err := h.jobs.Create(r.Context(), job); err != nil {
		writeRepoError(w, err, "scrape job")
		return
	}

	payload, err := json.Marshal(map[string]any{
		"scrape_job_id": job.ID,
		"source":        req.Source,
		"category":      req.Category,
		"location":      req.Location,
		"query":         req.Query,
		"file":          req.File,
		"limit":         req.Limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	err = h.queue.Enqueue(r.Context(), queue.Job{
		Type:    queue.JobCollectBusiness,
		Payload: payload,
	})
	if err != nil {
		// The row exists but nothing will ever run it. Say so rather than
		// reporting a success the worker will never deliver.
		job.Status = "failed"
		job.Error = "could not enqueue: " + err.Error()
		_ = h.jobs.Update(r.Context(), job)

		writeError(w, http.StatusServiceUnavailable, "could not enqueue job (is Redis up?)")
		return
	}

	writeJSON(w, http.StatusAccepted, job)
}

func (h *ScrapeHandler) Get(w http.ResponseWriter, r *http.Request) {
	job, err := h.jobs.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeRepoError(w, err, "scrape job")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *ScrapeHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := domain.JobFilter{
		Source:   q.Get("source"),
		Category: q.Get("category"),
		Status:   q.Get("status"),
		Page:     queryInt(r, "page", 1),
		Limit:    queryInt(r, "limit", 50),
	}

	jobs, total, err := h.jobs.List(r.Context(), filter)
	if err != nil {
		writeRepoError(w, err, "scrape jobs")
		return
	}
	if jobs == nil {
		jobs = []*domain.ScrapeJob{}
	}

	writeJSON(w, http.StatusOK, listResponse{
		Data:  jobs,
		Total: total,
		Page:  max(filter.Page, 1),
		Limit: filter.Limit,
	})
}

// Sources tells a client which source names Create will accept.
func (h *ScrapeHandler) Sources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sources": h.sourceNames()})
}

func (h *ScrapeHandler) sourceNames() []string {
	names := make([]string, 0, len(h.sources))
	for name := range h.sources {
		names = append(names, name)
	}
	return names
}
