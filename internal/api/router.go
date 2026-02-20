package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// NewRouter builds and returns the chi router with all routes registered.
func NewRouter(svc *Services, apiKey string) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)

	// Public health endpoint (no auth)
	r.Get("/health", svc.handleHealth)

	// All v1 routes require authentication
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware(apiKey))

		// Ingest
		r.Post("/v1/episodes", svc.handleIngestEpisode)
		r.Post("/v1/thoughts", svc.handleIngestThought)

		// Consolidation
		r.Post("/v1/consolidate", svc.handleConsolidate)

		// Search + retrieval
		r.Post("/v1/search", svc.handleSearch)

		// Traces
		r.Get("/v1/traces", svc.handleListTraces)
		r.Get("/v1/traces/{id}", svc.handleGetTrace)
		r.Get("/v1/traces/{id}/context", svc.handleGetTraceContext)
		r.Post("/v1/traces/{id}/reinforce", svc.handleReinforceTrace)

		// Episodes
		r.Get("/v1/episodes/{id}", svc.handleGetEpisode)

		// Entities
		r.Get("/v1/entities", svc.handleListEntities)

		// Activation
		r.Post("/v1/activation/decay", svc.handleDecayActivation)

		// Management
		r.Post("/v1/memory/flush", svc.handleFlush)
		r.Delete("/v1/memory/reset", svc.handleReset)
	})

	return r
}
