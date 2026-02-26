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
// Supports ?level=N to return a precomputed summary instead of full content.
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

	if level := parseLevel(r); level > 0 {
		if summary, err2 := s.Graph.GetSchemaSummary(sc.ID, level); err2 == nil && summary != "" {
			sc.Content = summary
		}
	}

	sc.Embedding = nil
	writeJSON(w, http.StatusOK, sc)
}

// handleSearchSchemas handles POST /v1/schemas/search.
// Supports two modes:
//   - ID lookup: {"ids": [...], "level": 32} — returns schema summaries for given IDs
//   - Text search: {"query": "..."} — not yet implemented
func (s *Services) handleSearchSchemas(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs   []string `json:"ids,omitempty"`
		Query string   `json:"query,omitempty"`
		Level int      `json:"level,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.IDs) == 0 && req.Query == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "ids or query is required")
		return
	}

	level := req.Level
	if level <= 0 {
		level = 32
	}

	if len(req.IDs) > 0 {
		schemas, err := s.Graph.GetSchemasByIDs(req.IDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		summaries, err := s.Graph.GetSchemaSummariesBatch(req.IDs, level)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}

		// Build name lookup for ordered response
		nameByID := make(map[string]string, len(schemas))
		for _, sc := range schemas {
			nameByID[sc.ID] = sc.Name
		}

		type schemaSummaryCard struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Summary string `json:"summary"`
			Level   int    `json:"level"`
		}

		cards := make([]schemaSummaryCard, 0, len(req.IDs))
		for _, id := range req.IDs {
			name, ok := nameByID[id]
			if !ok {
				continue // skip unknown IDs
			}
			card := schemaSummaryCard{ID: id, Name: name, Level: level}
			if summary, ok := summaries[id]; ok {
				card.Summary = summary
			} else {
				card.Summary = name
			}
			cards = append(cards, card)
		}
		writeJSON(w, http.StatusOK, cards)
		return
	}

	writeError(w, http.StatusNotImplemented, "not_implemented", "text search for schemas not yet supported; use ids")
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

