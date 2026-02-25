package api

import (
	"context"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleListSchemas handles GET /v1/schemas.
// Returns all schemas ordered by updated_at descending.
func (s *Services) handleListSchemas(w http.ResponseWriter, r *http.Request) {
	schemas, err := s.Graph.ListSchemas()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if schemas == nil {
		schemas = nil // handled by nonNil-like logic below
	}

	// Build response cards (omit large embeddings)
	type schemaCard struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Content   string `json:"content"`
		IsLabile  bool   `json:"is_labile,omitempty"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}

	cards := make([]schemaCard, len(schemas))
	for i, sc := range schemas {
		cards[i] = schemaCard{
			ID:        sc.ID,
			Name:      sc.Name,
			Content:   sc.Content,
			IsLabile:  sc.IsLabile,
			CreatedAt: sc.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: sc.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}
	writeJSON(w, http.StatusOK, cards)
}

// handleGetSchema handles GET /v1/schemas/{id}.
// Returns a single schema with its instance list.
func (s *Services) handleGetSchema(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if len(id) < 5 {
		writeError(w, http.StatusBadRequest, "invalid_id", "schema ID too short")
		return
	}

	sc, err := s.Graph.GetSchema(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if sc == nil {
		writeError(w, http.StatusNotFound, "not_found", "schema not found")
		return
	}

	writeJSON(w, http.StatusOK, sc)
}

// handleDeleteSchema handles DELETE /v1/schemas/{id}.
func (s *Services) handleDeleteSchema(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if len(id) < 5 {
		writeError(w, http.StatusBadRequest, "invalid_id", "schema ID too short")
		return
	}

	if err := s.Graph.DeleteSchema(id); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleInduceSchemas handles POST /v1/schemas/induce.
// Triggers schema induction asynchronously from L2+ engrams.
func (s *Services) handleInduceSchemas(w http.ResponseWriter, r *http.Request) {
	if s.SchemaInductor == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "schema induction not configured (no LLM)")
		return
	}

	// Check if we have enough L2+ engrams
	ok, err := s.SchemaInductor.ShouldRun()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"started": false,
			"reason":  "not enough L2+ engrams (need at least 3)",
		})
		return
	}

	// Run asynchronously to avoid HTTP timeout
	go func() {
		n, err := s.SchemaInductor.InduceSchemas(context.Background())
		if err != nil {
			log.Printf("[schema] async InduceSchemas error: %v", err)
		} else {
			log.Printf("[schema] async InduceSchemas: %d schemas created/updated", n)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"started": true,
	})
}

