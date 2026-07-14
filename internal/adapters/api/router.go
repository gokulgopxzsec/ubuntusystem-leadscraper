package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/makeforme/leadscraper/internal/adapters/api/handler"
	"github.com/makeforme/leadscraper/internal/adapters/api/middleware"
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

	r.Route("/api/v1", func(r chi.Router) {
		// Health and readiness stay unauthenticated so an orchestrator can
		// probe them without holding a credential.
		r.Get("/health", handler.Health(deps.Version))
		r.Get("/ready", handler.Readiness(deps.Version, deps.Pool, deps.Queue))

		r.Group(func(r chi.Router) {
			r.Use(middleware.APIKey(deps.APIKey))

			r.Get("/businesses", business.List)
			r.Get("/businesses/{id}", business.Get)
			r.Delete("/businesses/{id}", business.Delete)

			r.Get("/leads", business.Leads)

			r.Post("/scrape", scrape.Create)
			r.Get("/scrape", scrape.List)
			r.Get("/scrape/sources", scrape.Sources)
			r.Get("/scrape/{id}", scrape.Get)
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	})

	return r
}
