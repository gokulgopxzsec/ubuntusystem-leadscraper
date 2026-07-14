package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makeforme/leadscraper/internal/adapters/api/handler"
	"github.com/makeforme/leadscraper/internal/adapters/api/middleware"
	"github.com/makeforme/leadscraper/internal/adapters/api/web"
	"github.com/makeforme/leadscraper/internal/adapters/db/postgres"
	"github.com/makeforme/leadscraper/internal/embed"
	"github.com/makeforme/leadscraper/internal/ports"
	"github.com/makeforme/leadscraper/internal/queue"
)

// RouterDeps is what the API needs in order to serve real data.
type RouterDeps struct {
	Version string
	APIKey  string

	Pool  *pgxpool.Pool
	Queue queue.Queue

	Businesses ports.BusinessRepository
	Websites   ports.WebsiteRepository
	Contacts   ports.ContactRepository
	Socials    ports.SocialProfileRepository
	Scores     ports.LeadScoreRepository
	Audits     ports.AuditRepository
	Jobs       ports.ScrapeJobRepository

	Leads         *postgres.LeadRepo
	Embeddings    *postgres.EmbeddingRepo
	Embedder      embed.Provider
	MinSimilarity float64

	Sources map[string]ports.SourceAdapter
}

func NewRouter(deps RouterDeps) *chi.Mux {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	// A request that outlives this is not going to succeed, and it pins a
	// database connection for the whole time it hangs around.
	r.Use(chimw.Timeout(30 * time.Second))
	r.Use(chimw.Heartbeat("/healthz"))

	business := handler.NewBusinessHandler(
		deps.Businesses, deps.Websites, deps.Contacts,
		deps.Socials, deps.Scores, deps.Audits,
	)
	scrape := handler.NewScrapeHandler(deps.Jobs, deps.Queue, deps.Sources)
	search := handler.NewSearchHandler(deps.Embeddings, deps.Embedder, deps.MinSimilarity)
	leads := handler.NewLeadHandler(deps.Leads, deps.Businesses, deps.Queue)

	r.Route("/api/v1", func(r chi.Router) {
		// Health and readiness stay unauthenticated so an orchestrator can
		// probe them without holding a credential.
		r.Get("/health", handler.Health(deps.Version))
		r.Get("/ready", handler.Readiness(deps.Version, deps.Pool, deps.Queue))

		r.Group(func(r chi.Router) {
			r.Use(middleware.APIKey(deps.APIKey))

			r.Get("/businesses", business.List)
			r.Get("/businesses/{id}", business.Get)

			// CRUD over the lead list.
			r.Post("/businesses", leads.Create)
			r.Patch("/businesses/{id}", leads.Update)
			r.Delete("/businesses/{id}", leads.Delete)
			r.Post("/businesses/bulk-delete", leads.BulkDelete)
			r.Post("/businesses/rescan", leads.Rescan)

			// The dashboard's main view: every priority, filtered and sorted.
			r.Get("/leads", leads.List)
			r.Get("/leads/stats", leads.Stats)
			r.Get("/leads/facets", leads.Facets)
			r.Get("/leads/export", leads.Export)

			// Ask the lead corpus questions in plain English.
			r.Post("/search", search.Search)
			r.Get("/search/status", search.Status)

			r.Post("/scrape", scrape.Create)
			r.Get("/scrape", scrape.List)
			r.Get("/scrape/sources", scrape.Sources)
			r.Get("/scrape/{id}", scrape.Get)
		})
	})

	// The dashboard is embedded in the binary and served from the root. It is a
	// client of the same /api/v1 endpoints as any other consumer.
	r.Handle("/", web.Handler())

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	})

	return r
}
