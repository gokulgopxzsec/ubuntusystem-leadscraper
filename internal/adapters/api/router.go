package api

import (
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/makeforme/leadscraper/internal/adapters/api/handler"
)

func NewRouter(version string) *chi.Mux {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Heartbeat("/healthz"))

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", handler.Health(version))
	})

	return r
}
