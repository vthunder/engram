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

		// Engrams
		r.Get("/v1/engrams", svc.handleListEngrams)
		r.Get("/v1/engrams/{id}", svc.handleGetEngram)
		r.Get("/v1/engrams/{id}/context", svc.handleGetEngramContext)
		r.Post("/v1/engrams/{id}/reinforce", svc.handleReinforceEngram)
		r.Post("/v1/engrams/boost", svc.handleBoostEngrams)

		// Episodes
		r.Get("/v1/episodes", svc.handleListEpisodes)
		r.Get("/v1/episodes/{id}", svc.handleGetEpisode)
		r.Post("/v1/episodes/summaries", svc.handleBatchEpisodeSummaries)

		// Entities
		r.Get("/v1/entities", svc.handleListEntities)
		r.Get("/v1/entities/{id}", svc.handleGetEntity)

		// Activation
		r.Post("/v1/activation/decay", svc.handleDecayActivation)

		// Management
		r.Post("/v1/memory/flush", svc.handleFlush)
		r.Delete("/v1/memory/reset", svc.handleReset)
	})

	return r
}
