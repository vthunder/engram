package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/vthunder/engram/internal/consolidate"
	"github.com/vthunder/engram/internal/embed"
	"github.com/vthunder/engram/internal/graph"
	"github.com/vthunder/engram/internal/ner"
)

// Services holds all the dependencies wired into handlers.
type Services struct {
	Graph        *graph.DB
	EmbedClient  *embed.Client
	NERClient    *ner.Client
	Consolidator *consolidate.Consolidator
	Logger       *slog.Logger
}

// --- Ingest ---

type ingestEpisodeRequest struct {
	Content        string    `json:"content"`
	Source         string    `json:"source"`
	Author         string    `json:"author,omitempty"`
	AuthorID       string    `json:"author_id,omitempty"`
	Channel        string    `json:"channel,omitempty"`
	TimestampEvent time.Time `json:"timestamp_event,omitempty"`
	ReplyTo        string    `json:"reply_to,omitempty"`
	Embedding      []float64 `json:"embedding,omitempty"`
}

func (s *Services) handleIngestEpisode(w http.ResponseWriter, r *http.Request) {
	var req ingestEpisodeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "content is required")
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "source is required")
		return
	}
	if req.TimestampEvent.IsZero() {
		req.TimestampEvent = time.Now()
	}

	// Compute embedding if not provided
	emb := req.Embedding
	if len(emb) == 0 && s.EmbedClient != nil {
		var err error
		emb, err = s.EmbedClient.Embed(req.Content)
		if err != nil {
			s.Logger.Warn("embedding failed", "err", err)
		}
	}

	id := fmt.Sprintf("ep-%s", uuid.New().String())
	ep := &graph.Episode{
		ID:             id,
		Content:        req.Content,
		Source:         req.Source,
		Author:         req.Author,
		AuthorID:       req.AuthorID,
		Channel:        req.Channel,
		TimestampEvent: req.TimestampEvent,
		ReplyTo:        req.ReplyTo,
		Embedding:      emb,
	}

	if err := s.Graph.AddEpisode(ep); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	// Extract and link entities in background if NER is available
	if s.NERClient != nil {
		go func() {
			resp, err := s.NERClient.Extract(req.Content)
			if err != nil || resp == nil {
				return
			}
			for _, e := range resp.Entities {
				entity := &graph.Entity{
					ID:   fmt.Sprintf("ent-%s", uuid.New().String()),
					Name: e.Text,
					Type: graph.EntityType(e.Label),
				}
				if addErr := s.Graph.AddEntity(entity); addErr != nil {
					continue
				}
				_ = s.Graph.LinkEpisodeToEntity(id, entity.ID)
			}
		}()
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":       ep.ID,
		"short_id": ep.ShortID,
	})
}

type ingestThoughtRequest struct {
	Content string `json:"content"`
}

func (s *Services) handleIngestThought(w http.ResponseWriter, r *http.Request) {
	var req ingestThoughtRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "content is required")
		return
	}

	id := fmt.Sprintf("ep-%s", uuid.New().String())
	ep := &graph.Episode{
		ID:             id,
		Content:        req.Content,
		Source:         "thought",
		TimestampEvent: time.Now(),
	}
	if err := s.Graph.AddEpisode(ep); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": ep.ID})
}

// --- Consolidation ---

func (s *Services) handleConsolidate(w http.ResponseWriter, r *http.Request) {
	if s.Consolidator == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "consolidation not configured")
		return
	}

	start := time.Now()
	created, err := s.Consolidator.Run()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "consolidation_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"traces_created": created,
		"duration_ms":    time.Since(start).Milliseconds(),
	})
}

// --- Search ---

type searchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

func (s *Services) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "query is required")
		return
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}

	var queryEmb []float64
	if s.EmbedClient != nil {
		var err error
		queryEmb, err = s.EmbedClient.Embed(req.Query)
		if err != nil {
			s.Logger.Warn("query embedding failed", "err", err)
		}
	}

	result, err := s.Graph.Retrieve(queryEmb, req.Query, req.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "retrieval_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// --- Traces ---

func (s *Services) handleListTraces(w http.ResponseWriter, r *http.Request) {
	// Optional threshold filter: ?threshold=0.1&limit=20
	thresholdStr := r.URL.Query().Get("threshold")
	limitStr := r.URL.Query().Get("limit")

	if thresholdStr != "" {
		threshold, err := strconv.ParseFloat(thresholdStr, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_param", "threshold must be a float")
			return
		}
		limit := 50
		if limitStr != "" {
			if n, err2 := strconv.Atoi(limitStr); err2 == nil && n > 0 {
				limit = n
			}
		}
		traces, err := s.Graph.GetActivatedTraces(threshold, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, traces)
		return
	}

	traces, err := s.Graph.GetAllTraces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, traces)
}

// handleListEpisodes returns recent episodes for a channel, or unconsolidated episode IDs.
// GET /v1/episodes?channel=X&limit=N&unconsolidated=true
func (s *Services) handleListEpisodes(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	unconsolidated := r.URL.Query().Get("unconsolidated") == "true"
	limit := 50
	if lv := r.URL.Query().Get("limit"); lv != "" {
		if n, err := strconv.Atoi(lv); err == nil && n > 0 {
			limit = n
		}
	}

	if unconsolidated {
		ids, err := s.Graph.GetUnconsolidatedEpisodeIDsForChannel(channel)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		// Return as a list of IDs
		result := make([]string, 0, len(ids))
		for id := range ids {
			result = append(result, id)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ids": result})
		return
	}

	episodes, err := s.Graph.GetRecentEpisodes(channel, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, episodes)
}

type batchSummariesRequest struct {
	EpisodeIDs []string `json:"episode_ids"`
	Level      int      `json:"level"`
}

// handleBatchEpisodeSummaries returns summaries for a set of episodes at a compression level.
// POST /v1/episodes/summaries
func (s *Services) handleBatchEpisodeSummaries(w http.ResponseWriter, r *http.Request) {
	var req batchSummariesRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.EpisodeIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if req.Level <= 0 {
		req.Level = 1
	}

	summaries, err := s.Graph.GetEpisodeSummariesBatch(req.EpisodeIDs, req.Level)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	// Convert to a map[episodeID]summary string
	result := make(map[string]string, len(summaries))
	for id, s := range summaries {
		result[id] = s.Summary
	}
	writeJSON(w, http.StatusOK, result)
}

type boostTracesRequest struct {
	TraceIDs  []string `json:"trace_ids"`
	Boost     float64  `json:"boost"`
	Threshold float64  `json:"threshold,omitempty"`
}

// handleBoostTraces boosts activation for a set of traces.
// POST /v1/traces/boost
func (s *Services) handleBoostTraces(w http.ResponseWriter, r *http.Request) {
	var req boostTracesRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.TraceIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if req.Boost == 0 {
		req.Boost = 0.1
	}

	if err := s.Graph.BoostActivation(req.TraceIDs, req.Boost, req.Threshold); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Services) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	trace, err := s.Graph.GetTrace(id)
	if err != nil || trace == nil {
		trace, err = s.Graph.GetTraceByShortID(id)
		if err != nil || trace == nil {
			writeError(w, http.StatusNotFound, "not_found", "trace not found")
			return
		}
	}

	level := 1
	if lv := r.URL.Query().Get("level"); lv != "" {
		if n, err2 := strconv.Atoi(lv); err2 == nil {
			level = n
		}
	}
	if level > 0 {
		if summary, err2 := s.Graph.GetTraceSummary(trace.ID, level); err2 == nil && summary != nil {
			trace.Summary = summary.Summary
		}
	}

	writeJSON(w, http.StatusOK, trace)
}

type traceContextResponse struct {
	Trace    *graph.Trace    `json:"trace"`
	Sources  []graph.Episode `json:"source_episodes"`
	Entities []*graph.Entity `json:"linked_entities"`
}

func (s *Services) handleGetTraceContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	trace, err := s.Graph.GetTrace(id)
	if err != nil || trace == nil {
		trace, err = s.Graph.GetTraceByShortID(id)
		if err != nil || trace == nil {
			writeError(w, http.StatusNotFound, "not_found", "trace not found")
			return
		}
	}

	sources, _ := s.Graph.GetTraceSourceEpisodes(trace.ID)
	entityIDs, _ := s.Graph.GetTraceEntities(trace.ID)
	var entities []*graph.Entity
	for _, eid := range entityIDs {
		if e, err2 := s.Graph.GetEntity(eid); err2 == nil {
			entities = append(entities, e)
		}
	}

	writeJSON(w, http.StatusOK, traceContextResponse{
		Trace:    trace,
		Sources:  sources,
		Entities: entities,
	})
}

// --- Episodes ---

func (s *Services) handleGetEpisode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ep, err := s.Graph.GetEpisode(id)
	if err != nil || ep == nil {
		ep, err = s.Graph.GetEpisodeByShortID(id)
		if err != nil || ep == nil {
			writeError(w, http.StatusNotFound, "not_found", "episode not found")
			return
		}
	}
	writeJSON(w, http.StatusOK, ep)
}

// --- Entities ---

func (s *Services) handleListEntities(w http.ResponseWriter, r *http.Request) {
	entityType := r.URL.Query().Get("type")
	limit := 100
	if lv := r.URL.Query().Get("limit"); lv != "" {
		if n, err := strconv.Atoi(lv); err == nil {
			limit = n
		}
	}

	var entities []*graph.Entity
	var err error
	if entityType != "" {
		entities, err = s.Graph.GetEntitiesByType(graph.EntityType(entityType), limit)
	} else {
		entities, err = s.Graph.GetAllEntities(limit)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entities)
}

// --- Activation ---

type decayRequest struct {
	Lambda float64 `json:"lambda,omitempty"`
	Floor  float64 `json:"floor,omitempty"`
}

func (s *Services) handleDecayActivation(w http.ResponseWriter, r *http.Request) {
	var req decayRequest
	_ = decode(r, &req) // optional body

	updated, err := s.Graph.DecayActivationByAge(req.Lambda, req.Floor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}

type reinforceRequest struct {
	Embedding []float64 `json:"embedding,omitempty"`
	Alpha     float64   `json:"alpha,omitempty"`
}

func (s *Services) handleReinforceTrace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req reinforceRequest
	_ = decode(r, &req)
	if req.Alpha == 0 {
		req.Alpha = 0.3
	}

	if err := s.Graph.ReinforceTrace(id, req.Embedding, req.Alpha); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- Management ---

func (s *Services) handleFlush(w http.ResponseWriter, r *http.Request) {
	if s.Consolidator != nil {
		if _, err := s.Consolidator.Run(); err != nil {
			s.Logger.Warn("flush consolidation error", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Services) handleReset(w http.ResponseWriter, r *http.Request) {
	if err := s.Graph.Clear(); err != nil {
		writeError(w, http.StatusInternalServerError, "reset_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- Health ---

func (s *Services) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}
