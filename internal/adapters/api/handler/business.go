package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/makeforme/leadscraper/internal/domain"
	"github.com/makeforme/leadscraper/internal/ports"
)

type BusinessHandler struct {
	businesses ports.BusinessRepository
	websites   ports.WebsiteRepository
	contacts   ports.ContactRepository
	socials    ports.SocialProfileRepository
	scores     ports.LeadScoreRepository
	audits     ports.AuditRepository
}

func NewBusinessHandler(
	businesses ports.BusinessRepository,
	websites ports.WebsiteRepository,
	contacts ports.ContactRepository,
	socials ports.SocialProfileRepository,
	scores ports.LeadScoreRepository,
	audits ports.AuditRepository,
) *BusinessHandler {
	return &BusinessHandler{
		businesses: businesses,
		websites:   websites,
		contacts:   contacts,
		socials:    socials,
		scores:     scores,
		audits:     audits,
	}
}

func (h *BusinessHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := domain.BusinessFilter{
		Category:   q.Get("category"),
		Source:     q.Get("source"),
		Location:   q.Get("location"),
		Search:     q.Get("search"),
		HasWebsite: queryBool(r, "has_website"),
		SortBy:     q.Get("sort_by"),
		SortOrder:  q.Get("sort_order"),
		Page:       queryInt(r, "page", 1),
		Limit:      queryInt(r, "limit", 50),
	}

	items, total, err := h.businesses.List(r.Context(), filter)
	if err != nil {
		writeRepoError(w, err, "businesses")
		return
	}

	writeJSON(w, http.StatusOK, listResponse{
		Data:  nonNilBusinesses(items),
		Total: total,
		Page:  max(filter.Page, 1),
		Limit: filter.Limit,
	})
}

// businessDetail is the whole picture of one lead: everything the pipeline
// gathered, in the single response a salesperson actually wants.
type businessDetail struct {
	*domain.Business
	Website  *domain.Website         `json:"website,omitempty"`
	Contacts []*domain.Contact       `json:"contacts"`
	Socials  []*domain.SocialProfile `json:"socials"`
	Score    *domain.LeadScore       `json:"score,omitempty"`
	Audit    *domain.AuditReport     `json:"audit,omitempty"`
}

func (h *BusinessHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	business, err := h.businesses.GetByID(ctx, id)
	if err != nil {
		writeRepoError(w, err, "business")
		return
	}

	detail := businessDetail{
		Business: business,
		Contacts: []*domain.Contact{},
		Socials:  []*domain.SocialProfile{},
	}

	// The related records are all optional: a business that has not been
	// crawled yet simply has none of them, which is not an error.
	if site, err := h.websites.GetByBusinessID(ctx, id); err == nil {
		detail.Website = site
	}
	if contacts, err := h.contacts.GetByBusinessID(ctx, id); err == nil && contacts != nil {
		detail.Contacts = contacts
	}
	if socials, err := h.socials.GetByBusinessID(ctx, id); err == nil && socials != nil {
		detail.Socials = socials
	}
	if score, err := h.scores.GetByBusinessID(ctx, id); err == nil {
		detail.Score = score
	}
	if audit, err := h.audits.GetByBusinessID(ctx, id); err == nil {
		detail.Audit = audit
	}

	writeJSON(w, http.StatusOK, detail)
}

func (h *BusinessHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.businesses.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeRepoError(w, err, "business")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// Leads returns the scored, ranked list. This is the endpoint the sales team
// lives in.
func (h *BusinessHandler) Leads(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)

	scores, err := h.scores.ListHighPriority(r.Context(), limit)
	if err != nil {
		writeRepoError(w, err, "leads")
		return
	}

	type lead struct {
		*domain.LeadScore
		Business *domain.Business `json:"business,omitempty"`
	}

	out := make([]lead, 0, len(scores))
	for _, s := range scores {
		item := lead{LeadScore: s}
		if b, err := h.businesses.GetByID(r.Context(), s.BusinessID); err == nil {
			item.Business = b
		}
		out = append(out, item)
	}

	writeJSON(w, http.StatusOK, listResponse{
		Data:  out,
		Total: int64(len(out)),
		Page:  1,
		Limit: limit,
	})
}

// nonNilBusinesses keeps the JSON shape stable: an empty result should encode
// as [] rather than null, or every client has to special-case it.
func nonNilBusinesses(in []*domain.Business) []*domain.Business {
	if in == nil {
		return []*domain.Business{}
	}
	return in
}
