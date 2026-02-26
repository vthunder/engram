package api

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vthunder/engram/internal/consolidate"
	"github.com/vthunder/engram/internal/embed"
	"github.com/vthunder/engram/internal/graph"
	"github.com/vthunder/engram/internal/ner"
	engramschema "github.com/vthunder/engram/internal/schema"
)

// Services holds all the dependencies wired into handlers.
type Services struct {
	Graph          *graph.DB
	EmbedClient    *embed.Client
	NERClient      *ner.Client
	Consolidator   *consolidate.Consolidator
	CompressQueue  *graph.EpisodeCompressQueue
	SchemaInductor *engramschema.SchemaInductor
	Logger         *slog.Logger
	BotName        string // from identity config; empty = no identity configured
	BotAuthorID    string // from identity config
}

// nonNil returns s unchanged when non-nil, or an empty (non-nil) slice of the
// same type. This prevents encoding/json from serialising nil slices as JSON
// null instead of [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// --- Response helpers ---

// engramCard returns the default engram representation.
func engramCard(e *graph.Engram) map[string]any {
	card := map[string]any{
		"id":         e.ID,
		"summary":    e.Summary,
		"event_time": e.EventTime,
	}
	if e.Level > 0 {
		card["level"] = e.Level
	}
	if len(e.SchemaIDs) > 0 {
		card["schema_ids"] = e.SchemaIDs
	}
	return card
}

// entityCard returns the minimal entity representation: {id, name}.
func entityCard(e *graph.Entity) map[string]any {
	card := map[string]any{"id": e.ID, "name": e.Name}
	if e.Level > 0 {
		card["level"] = e.Level
	}
	return card
}

// episodeCard returns the default episode representation.
func episodeCard(e *graph.Episode) map[string]any {
	card := map[string]any{
		"id":              e.ID,
		"content":         e.Content,
		"timestamp_event": e.TimestampEvent,
		"author":          e.Author,
	}
	if e.Level > 0 {
		card["level"] = e.Level
	}
	return card
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
			e.Level = s.CompressionLevel
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
			e.Level = s.CompressionLevel
		}
	}
}

// applyEpisodeLevels replaces each episode's Content with the pyramid summary at the
// requested level (batch query). A no-op when level == 0.
// Episodes with no pre-generated summary at the requested level retain their original content.
func applyEpisodeLevels(g *graph.DB, episodes []*graph.Episode, level int) {
	if level == 0 || len(episodes) == 0 {
		return
	}
	ids := make([]string, len(episodes))
	for i, e := range episodes {
		ids[i] = e.ID
	}
	summaries, err := g.GetEpisodeSummariesBatch(ids, level)
	if err != nil {
		return
	}
	for _, e := range episodes {
		if s, ok := summaries[e.ID]; ok {
			e.Content = s.Summary
			e.Level = s.CompressionLevel
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

// parseDepth parses ?depth= (-1 = no filter / all depths).
func parseDepth(r *http.Request) int {
	if d := r.URL.Query().Get("depth"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n >= 0 {
			return n
		}
	}
	return -1
}

// filterByDepth returns only engrams matching the given depth. No-op when depth < 0.
func filterByDepth(engrams []*graph.Engram, depth int) []*graph.Engram {
	if depth < 0 {
		return engrams
	}
	out := make([]*graph.Engram, 0, len(engrams))
	for _, e := range engrams {
		if e.Depth == depth {
			out = append(out, e)
		}
	}
	return out
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

	if s.CompressQueue != nil {
		s.CompressQueue.Enqueue(ep.ID)
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
		Author:         s.BotName,
		AuthorID:       s.BotAuthorID,
		TimestampEvent: now,
	}
	if err := s.Graph.AddEpisode(ep); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	if s.CompressQueue != nil {
		s.CompressQueue.Enqueue(ep.ID)
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

	// Run recursive consolidation (L2+) asynchronously to avoid HTTP timeout on large batches.
	rStarted := false
	if ok, _ := s.Consolidator.ShouldRunRecursive(10, 24); ok {
		rStarted = true
		go func() {
			if n, err := s.Consolidator.RunRecursive(context.Background()); err != nil {
				log.Printf("[consolidate] async RunRecursive error: %v", err)
			} else {
				log.Printf("[consolidate] async RunRecursive: %d higher-depth engrams created", n)
			}
		}()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"engrams_created":   created,
		"recursive_started": rStarted,
		"duration_ms":       time.Since(start).Milliseconds(),
	})
}

// nerEntityEngrams extracts named entities from queryStr via NER and returns engram IDs
// linked to those entities, for extra-seeding spreading activation at retrieval time.
func (s *Services) nerEntityEngrams(queryStr string) []string {
	if s.NERClient == nil || s.Graph == nil {
		return nil
	}
	resp, err := s.NERClient.Extract(queryStr)
	if err != nil || !resp.HasEntities {
		return nil
	}
	seen := make(map[string]bool)
	var engramIDs []string
	for _, e := range resp.Entities {
		entity, err := s.Graph.FindEntityByName(e.Text)
		if err != nil || entity == nil {
			continue
		}
		ids, err := s.Graph.GetEngramsForEntitiesBatch([]string{entity.ID}, 3)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				engramIDs = append(engramIDs, id)
			}
		}
	}
	return engramIDs
}

// --- Engrams ---

type searchEngramsRequest struct {
	Query  string   `json:"query"`
	IDs    []string `json:"ids,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Detail string   `json:"detail,omitempty"`
	Level  int      `json:"level,omitempty"`
}

// handleSearchEngrams handles POST /v1/engrams/search.
// Accepts a JSON body to support arbitrarily large query strings.
// Supports two modes:
//   - ID lookup: {"ids": [...], "level": 32} — returns engrams for given IDs
//   - Text search: {"query": "...", "level": 32} — semantic search
func (s *Services) handleSearchEngrams(w http.ResponseWriter, r *http.Request) {
	var req searchEngramsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Query == "" && len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "query or ids is required")
		return
	}

	full := req.Detail == "full"

	// ID lookup mode
	if len(req.IDs) > 0 {
		engramMap, err := s.Graph.GetEngramsBatch(req.IDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		// Preserve request order
		engrams := make([]*graph.Engram, 0, len(req.IDs))
		for _, id := range req.IDs {
			if en, ok := engramMap[id]; ok {
				engrams = append(engrams, en)
			}
		}
		applyEngramLevels(s.Graph, engrams, req.Level)
		populateSchemaIDs(s.Graph, engrams)
		if full {
			for _, e := range engrams {
				e.Embedding = nil
			}
			writeJSON(w, http.StatusOK, nonNil(engrams))
		} else {
			cards := make([]map[string]any, 0, len(engrams))
			for _, e := range engrams {
				cards = append(cards, engramCard(e))
			}
			writeJSON(w, http.StatusOK, cards)
		}
		return
	}

	// Text search mode
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}

	// Run embedding and NER concurrently — both are network calls.
	embCh := make(chan []float64, 1)
	seedCh := make(chan []string, 1)

	if s.EmbedClient != nil {
		go func() {
			emb, err := s.EmbedClient.Embed(req.Query)
			if err != nil {
				s.Logger.Warn("query embedding failed", "err", err)
				embCh <- nil
				return
			}
			embCh <- emb
		}()
	} else {
		embCh <- nil
	}

	go func() { seedCh <- s.nerEntityEngrams(req.Query) }()

	queryEmb := <-embCh
	extraSeeds := <-seedCh

	result, err := s.Graph.Retrieve(queryEmb, req.Query, limit, extraSeeds...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "retrieval_error", err.Error())
		return
	}
	applyEngramLevels(s.Graph, result.Engrams, req.Level)
	populateSchemaIDs(s.Graph, result.Engrams)
	if full {
		for _, e := range result.Engrams {
			e.Embedding = nil
		}
		writeJSON(w, http.StatusOK, nonNil(result.Engrams))
	} else {
		cards := make([]map[string]any, 0, len(result.Engrams))
		for _, e := range result.Engrams {
			cards = append(cards, engramCard(e))
		}
		writeJSON(w, http.StatusOK, cards)
	}
}

// populateSchemaIDs batch-fetches schema annotations and sets SchemaIDs on each engram.
func populateSchemaIDs(g *graph.DB, engrams []*graph.Engram) {
	if len(engrams) == 0 {
		return
	}
	ids := make([]string, len(engrams))
	for i, e := range engrams {
		ids[i] = e.ID
	}
	schemaMap, err := g.GetSchemaIDsForEngrams(ids)
	if err != nil {
		return
	}
	for _, e := range engrams {
		if sids, ok := schemaMap[e.ID]; ok {
			e.SchemaIDs = sids
		}
	}
}

func (s *Services) handleListEngrams(w http.ResponseWriter, r *http.Request) {
	full := parseDetail(r)
	level := parseLevel(r)
	depth := parseDepth(r)

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
		engrams = filterByDepth(engrams, depth)
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
	engrams = filterByDepth(engrams, depth)
	applyEngramLevels(s.Graph, engrams, level)
	writeEngramList(w, engrams, full)
}

func writeEngramList(w http.ResponseWriter, engrams []*graph.Engram, full bool) {
	if full {
		for _, e := range engrams {
			e.Embedding = nil
		}
		writeJSON(w, http.StatusOK, nonNil(engrams))
	} else {
		cards := make([]map[string]any, 0, len(engrams))
		for _, e := range engrams {
			cards = append(cards, engramCard(e))
		}
		writeJSON(w, http.StatusOK, cards)
	}
}

func (s *Services) handleDeleteEngram(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Try prefix resolution if exact ID not found
	engram, err := s.Graph.GetEngram(id)
	if err != nil || engram == nil {
		fullID, resolveErr := s.Graph.ResolveEngramID(id)
		if resolveErr != nil {
			writeError(w, http.StatusNotFound, "not_found", "engram not found")
			return
		}
		id = fullID
	}

	if err := s.Graph.DeleteEngram(id); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// handleGetEngramChildren returns the source nodes for an engram.
// For L2+ engrams (depth > 0): returns source engrams via CONSOLIDATED_FROM edges.
// For L1 engrams (depth = 0): returns source episodes.
func (s *Services) handleGetEngramChildren(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	level := parseLevel(r)

	engram, err := s.Graph.GetEngram(id)
	if err != nil || engram == nil {
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

	if engram.Depth > 0 {
		// L2+ engram: children are source engrams
		children, err := s.Graph.GetEngramChildren(engram.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		applyEngramLevels(s.Graph, children, level)
		cards := make([]map[string]any, 0, len(children))
		for _, c := range children {
			card := engramCard(c)
			card["depth"] = c.Depth
			cards = append(cards, card)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"engram_id": engram.ID,
			"depth":     engram.Depth,
			"type":      "engrams",
			"children":  nonNil(cards),
		})
	} else {
		// L1 engram: children are source episodes
		sources, _ := s.Graph.GetEngramSourceEpisodes(engram.ID)
		sourcePtrs := make([]*graph.Episode, len(sources))
		for i := range sources {
			sourcePtrs[i] = &sources[i]
		}
		applyEpisodeLevels(s.Graph, sourcePtrs, level)
		cards := make([]map[string]any, 0, len(sourcePtrs))
		for _, ep := range sourcePtrs {
			cards = append(cards, episodeCard(ep))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"engram_id": engram.ID,
			"depth":     0,
			"type":      "episodes",
			"children":  nonNil(cards),
		})
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
			"source_episodes": nonNil(sources),
			"linked_entities": nonNil(entities),
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

type searchEpisodesRequest struct {
	Query  string   `json:"query"`
	IDs    []string `json:"ids,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Detail string   `json:"detail,omitempty"`
	Level  int      `json:"level,omitempty"`
}

// handleSearchEpisodes handles POST /v1/episodes/search.
// Supports two modes:
//   - ID lookup: {"ids": [...], "level": 32} — returns episodes for given IDs
//   - Text search: {"query": "...", "level": 32} — full-text search
func (s *Services) handleSearchEpisodes(w http.ResponseWriter, r *http.Request) {
	var req searchEpisodesRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Query == "" && len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "query or ids is required")
		return
	}

	// ID lookup mode
	if len(req.IDs) > 0 {
		episodes, err := s.Graph.GetEpisodes(req.IDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		applyEpisodeLevels(s.Graph, episodes, req.Level)
		writeEpisodeList(w, episodes, req.Detail == "full")
		return
	}

	// Text search mode
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	episodes, err := s.Graph.SearchEpisodesByText(req.Query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	applyEpisodeLevels(s.Graph, episodes, req.Level)
	writeEpisodeList(w, episodes, req.Detail == "full")
}

func (s *Services) handleListEpisodes(w http.ResponseWriter, r *http.Request) {
	full := parseDetail(r)
	level := parseLevel(r)
	limit := parseLimit(r, 50)

	channel := r.URL.Query().Get("channel")
	unconsolidated := r.URL.Query().Get("unconsolidated") == "true"

	// Resolve ?before={id} to a timestamp cursor
	var beforeTimestamp *time.Time
	if beforeID := r.URL.Query().Get("before"); beforeID != "" {
		ref, err := s.Graph.GetEpisode(beforeID)
		if err != nil || ref == nil {
			// Try prefix resolution
			fullID, resolveErr := s.Graph.ResolveEpisodeID(beforeID)
			if resolveErr != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "before: episode not found")
				return
			}
			ref, err = s.Graph.GetEpisode(fullID)
			if err != nil || ref == nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "before: episode not found")
				return
			}
		}
		t := ref.TimestampEvent
		beforeTimestamp = &t
	}

	episodes, err := s.Graph.GetEpisodesFiltered(channel, beforeTimestamp, unconsolidated, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	applyEpisodeLevels(s.Graph, episodes, level)
	writeEpisodeList(w, episodes, full)
}

func writeEpisodeList(w http.ResponseWriter, episodes []*graph.Episode, full bool) {
	if full {
		for _, e := range episodes {
			e.Embedding = nil
		}
		writeJSON(w, http.StatusOK, nonNil(episodes))
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
	if level := parseLevel(r); level > 0 {
		if summary, err2 := s.Graph.GetEpisodeSummary(ep.ID, level); err2 == nil && summary != nil {
			ep.Content = summary.Summary
		}
	}

	if parseDetail(r) {
		ep.Embedding = nil
		writeJSON(w, http.StatusOK, ep)
	} else {
		writeJSON(w, http.StatusOK, episodeCard(ep))
	}
}

// handleEpisodeCount returns the count of episodes matching optional filters.
// GET /v1/episodes/count
// GET /v1/episodes/count?unconsolidated=true
// GET /v1/episodes/count?channel=X
// GET /v1/episodes/count?channel=X&unconsolidated=true
func (s *Services) handleEpisodeCount(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	unconsolidated := r.URL.Query().Get("unconsolidated") == "true"
	count, err := s.Graph.CountEpisodesFiltered(channel, unconsolidated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

type addEpisodeEdgeRequest struct {
	ToID       string  `json:"to_id"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence"`
}

// handleAddEpisodeEdge creates a directed edge between two episodes.
// POST /v1/episodes/{id}/edges
func (s *Services) handleAddEpisodeEdge(w http.ResponseWriter, r *http.Request) {
	fromID := chi.URLParam(r, "id")
	var req addEpisodeEdgeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.ToID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "to_id is required")
		return
	}
	if req.EdgeType == "" {
		req.EdgeType = string(graph.EdgeFollows)
	}
	if req.Confidence <= 0 {
		req.Confidence = 1.0
	}
	if err := s.Graph.AddEpisodeEdge(fromID, req.ToID, graph.EdgeType(req.EdgeType), req.Confidence); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// --- Entities ---

type searchEntitiesRequest struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit,omitempty"`
	Detail string `json:"detail,omitempty"`
	Level  int    `json:"level,omitempty"`
}

// handleSearchEntities handles POST /v1/entities/search.
func (s *Services) handleSearchEntities(w http.ResponseWriter, r *http.Request) {
	var req searchEntitiesRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "query is required")
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	entities, err := s.Graph.FindEntitiesByText(req.Query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	applyEntityLevels(s.Graph, entities, req.Level)
	writeEntityList(w, entities, req.Detail == "full")
}

func (s *Services) handleListEntities(w http.ResponseWriter, r *http.Request) {
	full := parseDetail(r)
	level := parseLevel(r)
	limit := parseLimit(r, 100)

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
		writeJSON(w, http.StatusOK, nonNil(entities))
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

func (s *Services) handleGetEntityEngrams(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	engramIDs, err := s.Graph.GetEngramsForEntity(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	if len(engramIDs) == 0 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	engramMap, err := s.Graph.GetEngramsBatch(engramIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	engrams := make([]*graph.Engram, 0, len(engramMap))
	for _, e := range engramMap {
		engrams = append(engrams, e)
	}

	writeEngramList(w, engrams, parseDetail(r))
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

// handleRegenerateEngramPyramids regenerates stored pyramid summaries (L4–L64) for all
// engrams (or a depth-filtered subset) using the current compression prompts.
// The operation runs in the background; the endpoint returns immediately with a count of
// engrams queued. Optional query param: ?depth=0 to process only L1 engrams.
func (s *Services) handleRegenerateEngramPyramids(w http.ResponseWriter, r *http.Request) {
	if s.CompressQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "compression not configured")
		return
	}
	compressor := s.CompressQueue.Compressor()

	depth := parseDepth(r)

	engrams, err := s.Graph.GetAllEngrams()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	engrams = filterByDepth(engrams, depth)

	count := len(engrams)
	botName := s.BotName
	logger := s.Logger

	go func() {
		done, failed := 0, 0
		for _, e := range engrams {
			if err := s.Graph.RegenerateEngramPyramid(e.ID, compressor, botName); err != nil {
				logger.Warn("pyramid regen failed", "id", e.ID, "err", err)
				failed++
				continue
			}
			done++
		}
		logger.Info("engram pyramid regeneration complete", "done", done, "failed", failed, "total", count)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"started": count,
		"depth":   depth,
	})
}

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
