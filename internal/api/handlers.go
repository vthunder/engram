package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
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

// --- Response helpers ---

// engramCard returns the minimal engram representation: {id, summary}.
func engramCard(e *graph.Engram) map[string]any {
	return map[string]any{"id": e.ID, "summary": e.Summary}
}

// entityCard returns the minimal entity representation: {id, name}.
func entityCard(e *graph.Entity) map[string]any {
	return map[string]any{"id": e.ID, "name": e.Name}
}

// episodeCard returns the minimal episode representation: {id, content}.
func episodeCard(e *graph.Episode) map[string]any {
	return map[string]any{"id": e.ID, "content": e.Content}
}

// parseDetail returns true if ?detail=full is set.
func parseDetail(r *http.Request) bool {
	return r.URL.Query().Get("detail") == "full"
}

// applyEngramLevels replaces each engram's Summary with the pyramid summary at the
// requested level (batch query). A no-op when level == 0.
func applyEngramLevels(g *graph.DB, engrams []*graph.Engram, level int) {
	if level == 0 || len(engrams) == 0 {
		return
	}
	ids := make([]string, len(engrams))
	for i, e := range engrams {
		ids[i] = e.ID
	}
	summaries, err := g.GetEngramSummariesBatch(ids, level)
	if err != nil {
		return
	}
	for _, e := range engrams {
		if s, ok := summaries[e.ID]; ok {
			e.Summary = s.Summary
		}
	}
}

// applyEntityLevels replaces each entity's Summary with the pyramid summary at the
// requested level (batch query). A no-op when level == 0.
func applyEntityLevels(g *graph.DB, entities []*graph.Entity, level int) {
	if level == 0 || len(entities) == 0 {
		return
	}
	ids := make([]string, len(entities))
	for i, e := range entities {
		ids[i] = e.ID
	}
	summaries, err := g.GetEntitySummariesBatch(ids, level)
	if err != nil {
		return
	}
	for _, e := range entities {
		if s, ok := summaries[e.ID]; ok {
			e.Summary = s.Summary
		}
	}
}

// parseLevel parses ?level= (0 = verbatim/default).
func parseLevel(r *http.Request) int {
	if lv := r.URL.Query().Get("level"); lv != "" {
		if n, err := strconv.Atoi(lv); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}

// parseLimit parses ?limit= with a given default.
func parseLimit(r *http.Request, def int) int {
	if lv := r.URL.Query().Get("limit"); lv != "" {
		if n, err := strconv.Atoi(lv); err == nil && n > 0 {
			return n
		}
	}
	return def
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

	id := graph.GenerateEpisodeID(req.Content, req.Source, req.TimestampEvent.UnixNano())
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
				entityID := "ent:" + e.Text
				entity := &graph.Entity{
					ID:   entityID,
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
		"id": ep.ID,
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

	now := time.Now()
	id := graph.GenerateEpisodeID(req.Content, "thought", now.UnixNano())
	ep := &graph.Episode{
		ID:             id,
		Content:        req.Content,
		Source:         "thought",
		TimestampEvent: now,
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
		"engrams_created": created,
		"duration_ms":     time.Since(start).Milliseconds(),
	})
}

// --- Engrams ---

func (s *Services) handleListEngrams(w http.ResponseWriter, r *http.Request) {
	full := parseDetail(r)
	level := parseLevel(r)
	queryStr := r.URL.Query().Get("query")

	// Semantic search path
	if queryStr != "" {
		limit := parseLimit(r, 10)
		var queryEmb []float64
		if s.EmbedClient != nil {
			var err error
			queryEmb, err = s.EmbedClient.Embed(queryStr)
			if err != nil {
				s.Logger.Warn("query embedding failed", "err", err)
			}
		}
		result, err := s.Graph.Retrieve(queryEmb, queryStr, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "retrieval_error", err.Error())
			return
		}
		applyEngramLevels(s.Graph, result.Engrams, level)
		if full {
			for _, e := range result.Engrams {
				e.Embedding = nil
			}
			writeJSON(w, http.StatusOK, result.Engrams)
		} else {
			cards := make([]map[string]any, 0, len(result.Engrams))
			for _, e := range result.Engrams {
				cards = append(cards, engramCard(e))
			}
			writeJSON(w, http.StatusOK, cards)
		}
		return
	}

	// Threshold filter path
	thresholdStr := r.URL.Query().Get("threshold")
	if thresholdStr != "" {
		threshold, err := strconv.ParseFloat(thresholdStr, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_param", "threshold must be a float")
			return
		}
		limit := parseLimit(r, 50)
		engrams, err := s.Graph.GetActivatedEngrams(threshold, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		applyEngramLevels(s.Graph, engrams, level)
		writeEngramList(w, engrams, full)
		return
	}

	// List all
	engrams, err := s.Graph.GetAllEngrams()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	applyEngramLevels(s.Graph, engrams, level)
	writeEngramList(w, engrams, full)
}

func writeEngramList(w http.ResponseWriter, engrams []*graph.Engram, full bool) {
	if full {
		for _, e := range engrams {
			e.Embedding = nil
		}
		writeJSON(w, http.StatusOK, engrams)
	} else {
		cards := make([]map[string]any, 0, len(engrams))
		for _, e := range engrams {
			cards = append(cards, engramCard(e))
		}
		writeJSON(w, http.StatusOK, cards)
	}
}

func (s *Services) handleGetEngram(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	engram, err := s.Graph.GetEngram(id)
	if err != nil || engram == nil {
		// Try prefix resolution
		fullID, resolveErr := s.Graph.ResolveEngramID(id)
		if resolveErr != nil {
			writeError(w, http.StatusNotFound, "not_found", "engram not found")
			return
		}
		engram, err = s.Graph.GetEngram(fullID)
		if err != nil || engram == nil {
			writeError(w, http.StatusNotFound, "not_found", "engram not found")
			return
		}
	}

	if level := parseLevel(r); level > 0 {
		if summary, err2 := s.Graph.GetEngramSummary(engram.ID, level); err2 == nil && summary != nil {
			engram.Summary = summary.Summary
		}
	}

	if parseDetail(r) {
		engram.Embedding = nil
		writeJSON(w, http.StatusOK, engram)
	} else {
		writeJSON(w, http.StatusOK, engramCard(engram))
	}
}

func (s *Services) handleGetEngramContext(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	engram, err := s.Graph.GetEngram(id)
	if err != nil || engram == nil {
		// Try prefix resolution
		fullID, resolveErr := s.Graph.ResolveEngramID(id)
		if resolveErr != nil {
			writeError(w, http.StatusNotFound, "not_found", "engram not found")
			return
		}
		engram, err = s.Graph.GetEngram(fullID)
		if err != nil || engram == nil {
			writeError(w, http.StatusNotFound, "not_found", "engram not found")
			return
		}
	}

	sources, _ := s.Graph.GetEngramSourceEpisodes(engram.ID)
	entityIDs, _ := s.Graph.GetEngramEntities(engram.ID)
	var entities []*graph.Entity
	for _, eid := range entityIDs {
		if e, err2 := s.Graph.GetEntity(eid); err2 == nil {
			entities = append(entities, e)
		}
	}

	full := parseDetail(r)
	if full {
		engram.Embedding = nil
		for i := range sources {
			sources[i].Embedding = nil
		}
		for _, e := range entities {
			e.Embedding = nil
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"engram":          engram,
			"source_episodes": sources,
			"linked_entities": entities,
		})
	} else {
		sourcCards := make([]map[string]any, 0, len(sources))
		for i := range sources {
			sourcCards = append(sourcCards, episodeCard(&sources[i]))
		}
		entCards := make([]map[string]any, 0, len(entities))
		for _, e := range entities {
			entCards = append(entCards, entityCard(e))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"engram":          engramCard(engram),
			"source_episodes": sourcCards,
			"linked_entities": entCards,
		})
	}
}

// --- Episodes ---

func (s *Services) handleListEpisodes(w http.ResponseWriter, r *http.Request) {
	full := parseDetail(r)
	queryStr := r.URL.Query().Get("query")
	limit := parseLimit(r, 50)

	// Text search path
	if queryStr != "" {
		episodes, err := s.Graph.SearchEpisodesByText(queryStr, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		writeEpisodeList(w, episodes, full)
		return
	}

	channel := r.URL.Query().Get("channel")
	unconsolidated := r.URL.Query().Get("unconsolidated") == "true"

	if unconsolidated {
		ids, err := s.Graph.GetUnconsolidatedEpisodeIDsForChannel(channel)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
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
	writeEpisodeList(w, episodes, full)
}

func writeEpisodeList(w http.ResponseWriter, episodes []*graph.Episode, full bool) {
	if full {
		for _, e := range episodes {
			e.Embedding = nil
		}
		writeJSON(w, http.StatusOK, episodes)
	} else {
		cards := make([]map[string]any, 0, len(episodes))
		for _, e := range episodes {
			cards = append(cards, episodeCard(e))
		}
		writeJSON(w, http.StatusOK, cards)
	}
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

func (s *Services) handleGetEpisode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ep, err := s.Graph.GetEpisode(id)
	if err != nil || ep == nil {
		// Try prefix resolution
		fullID, resolveErr := s.Graph.ResolveEpisodeID(id)
		if resolveErr != nil {
			writeError(w, http.StatusNotFound, "not_found", "episode not found")
			return
		}
		ep, err = s.Graph.GetEpisode(fullID)
		if err != nil || ep == nil {
			writeError(w, http.StatusNotFound, "not_found", "episode not found")
			return
		}
	}
	if parseDetail(r) {
		ep.Embedding = nil
		writeJSON(w, http.StatusOK, ep)
	} else {
		writeJSON(w, http.StatusOK, episodeCard(ep))
	}
}

// --- Entities ---

func (s *Services) handleListEntities(w http.ResponseWriter, r *http.Request) {
	full := parseDetail(r)
	level := parseLevel(r)
	queryStr := r.URL.Query().Get("query")
	limit := parseLimit(r, 100)

	// Text search path
	if queryStr != "" {
		entities, err := s.Graph.FindEntitiesByText(queryStr, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		applyEntityLevels(s.Graph, entities, level)
		writeEntityList(w, entities, full)
		return
	}

	entityType := r.URL.Query().Get("type")
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
	applyEntityLevels(s.Graph, entities, level)
	writeEntityList(w, entities, full)
}

func writeEntityList(w http.ResponseWriter, entities []*graph.Entity, full bool) {
	if full {
		for _, e := range entities {
			e.Embedding = nil
		}
		writeJSON(w, http.StatusOK, entities)
	} else {
		cards := make([]map[string]any, 0, len(entities))
		for _, e := range entities {
			cards = append(cards, entityCard(e))
		}
		writeJSON(w, http.StatusOK, cards)
	}
}

func (s *Services) handleGetEntity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	entity, err := s.Graph.GetEntity(id)
	if err != nil || entity == nil {
		writeError(w, http.StatusNotFound, "not_found", "entity not found")
		return
	}

	if level := parseLevel(r); level > 0 {
		if summary, err2 := s.Graph.GetEntitySummary(entity.ID, level); err2 == nil && summary != nil {
			entity.Summary = summary.Summary
		}
	}

	if parseDetail(r) {
		entity.Embedding = nil
		writeJSON(w, http.StatusOK, entity)
	} else {
		writeJSON(w, http.StatusOK, entityCard(entity))
	}
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

func (s *Services) handleReinforceEngram(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req reinforceRequest
	_ = decode(r, &req)
	if req.Alpha == 0 {
		req.Alpha = 0.3
	}

	if err := s.Graph.ReinforceEngram(id, req.Embedding, req.Alpha); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type boostEngramsRequest struct {
	EngramIDs []string `json:"engram_ids"`
	Boost     float64  `json:"boost"`
	Threshold float64  `json:"threshold,omitempty"`
}

// handleBoostEngrams boosts activation for a set of engrams.
// POST /v1/engrams/boost
func (s *Services) handleBoostEngrams(w http.ResponseWriter, r *http.Request) {
	var req boostEngramsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.EngramIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if req.Boost == 0 {
		req.Boost = 0.1
	}

	if err := s.Graph.BoostActivation(req.EngramIDs, req.Boost, req.Threshold); err != nil {
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
