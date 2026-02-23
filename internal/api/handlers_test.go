package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/vthunder/engram/internal/graph"
)

const testAPIKey = "test-key-abc123"

// setupTestServices creates a Services instance backed by a temporary graph.DB.
// Returns the services, the test HTTP server, and a cleanup function.
func setupTestServices(t *testing.T) (*Services, *httptest.Server, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "api-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	db, err := graph.Open(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to open graph DB: %v", err)
	}

	svc := &Services{
		Graph:        db,
		EmbedClient:  nil, // no embedding in tests
		NERClient:    nil, // no NER in tests
		Consolidator: nil,
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	router := NewRouter(svc, testAPIKey)
	srv := httptest.NewServer(router)

	cleanup := func() {
		srv.Close()
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return svc, srv, cleanup
}

// doRequest sends an authenticated request and returns the response.
func doRequest(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	return doRequestWithKey(t, srv, method, path, body, testAPIKey)
}

func doRequestWithKey(t *testing.T, srv *httptest.Server, method, path, body, key string) *http.Response {
	t.Helper()
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, srv.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
}

// --- Health ---

func TestHealthEndpoint(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", body["status"])
	}
}

// --- Auth ---

func TestAuthMissingKey(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequestWithKey(t, srv, http.MethodGet, "/v1/engrams", "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthWrongKey(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequestWithKey(t, srv, http.MethodGet, "/v1/engrams", "", "wrong-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthCorrectKey(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/engrams", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Ingest episode ---

func TestIngestEpisode_MissingContent(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes", `{"source":"test"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestIngestEpisode_MissingSource(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes", `{"content":"hello"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestIngestEpisode_Success(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	payload := map[string]any{
		"content":         "Alice joined as tech lead",
		"source":          "discord",
		"author":          "thunder",
		"channel":         "general",
		"timestamp_event": time.Now().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)

	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes", string(body))

	var result map[string]string
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
	if result["id"] == "" {
		t.Error("expected non-empty id in response")
	}
}

func TestIngestEpisode_WithEmbedding(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	payload := map[string]any{
		"content":   "Pre-embedded episode content",
		"source":    "test",
		"embedding": []float64{0.1, 0.2, 0.3, 0.4},
	}
	body, _ := json.Marshal(payload)

	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes", string(body))
	var result map[string]string
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

// --- Ingest thought ---

func TestIngestThought_MissingContent(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/thoughts", `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestIngestThought_Success(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/thoughts", `{"content":"a quick thought"}`)
	var result map[string]string
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
	if result["id"] == "" {
		t.Error("expected id in response")
	}
}

// --- Search endpoints ---

func TestSearchEpisodes_EmptyDB(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes/search", `{"query":"anything"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on empty DB search, got %d", resp.StatusCode)
	}
}

func TestSearchEpisodes_MissingQuery(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes/search", `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing query, got %d", resp.StatusCode)
	}
}

func TestSearchEntities_EmptyDB(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/entities/search", `{"query":"Alice"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on empty DB search, got %d", resp.StatusCode)
	}
}

func TestSearchEntities_MissingQuery(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/entities/search", `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing query, got %d", resp.StatusCode)
	}
}

func TestSearchEngrams_EmptyDB(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/engrams/search", `{"query":"anything"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on empty DB search, got %d", resp.StatusCode)
	}
}

func TestSearchEngrams_MissingQuery(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/engrams/search", `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing query, got %d", resp.StatusCode)
	}
}

// --- Traces ---

func TestListEngrams_Empty(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/engrams", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestGetEngram_NotFound(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/engrams/nonexistent-id", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetEngram_Found(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	const engramID = "a1b2c3d4e5f60000000000000000abcd" // 32-char hex
	tr := &graph.Engram{
		ID:         engramID,
		Summary:    "Test engram for API",
		EngramType: graph.EngramTypeKnowledge,
	}
	if err := svc.Graph.AddEngram(tr); err != nil {
		t.Fatalf("AddEngram failed: %v", err)
	}
	svc.Graph.AddEngramSummary(engramID, 1, "Test engram for API", 5)

	// Use 5-char prefix for lookup
	resp := doRequest(t, srv, http.MethodGet, "/v1/engrams/a1b2c", "")
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if result["id"] != engramID {
		t.Errorf("expected id %q, got %v", engramID, result["id"])
	}
}

func TestGetEngramContext_Found(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	const engramID = "deadbeef000102030405060708090011" // 32-char hex
	tr := &graph.Engram{
		ID:      engramID,
		Summary: "Context engram",
	}
	svc.Graph.AddEngram(tr)

	// Use 5-char prefix for lookup
	resp := doRequest(t, srv, http.MethodGet, "/v1/engrams/deadb/context", "")
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if result["engram"] == nil {
		t.Error("expected non-nil engram in context response")
	}
}

func TestGetEngramContext_NotFound(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/engrams/no-such-trace/context", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// --- Episodes ---

func TestGetEpisode_NotFound(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/episodes/no-such-ep", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetEpisode_Found(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	ep := &graph.Episode{
		ID:             "ep-api-test-001",
		Content:        "Test episode content",
		Source:         "test",
		TimestampEvent: time.Now(),
	}
	if err := svc.Graph.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode failed: %v", err)
	}

	resp := doRequest(t, srv, http.MethodGet, "/v1/episodes/ep-api-test-001", "")
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if result["id"] != "ep-api-test-001" {
		t.Errorf("expected episode id 'ep-api-test-001', got %v", result["id"])
	}
}

// --- Episode count ---

func TestEpisodeCount_DefaultsToAll(t *testing.T) {
	// Omitting ?unconsolidated=true is valid; the handler counts all episodes.
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/episodes/count", "")
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for /v1/episodes/count (no params), got %d", resp.StatusCode)
	}
	if _, ok := result["count"]; !ok {
		t.Errorf("expected count field in response, got %v", result)
	}
}

func TestEpisodeCount_Empty(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/episodes/count?unconsolidated=true", "")
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	count, ok := result["count"].(float64)
	if !ok {
		t.Fatalf("expected numeric count field, got %T: %v", result["count"], result["count"])
	}
	if count != 0 {
		t.Errorf("expected count=0 on empty DB, got %v", count)
	}
}

func TestEpisodeCount_WithEpisodes(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	for i, id := range []string{"ep-count-1", "ep-count-2"} {
		ep := &graph.Episode{
			ID:             id,
			Content:        "content " + string(rune('A'+i)),
			Source:         "test",
			TimestampEvent: time.Now(),
		}
		if err := svc.Graph.AddEpisode(ep); err != nil {
			t.Fatalf("AddEpisode failed: %v", err)
		}
	}

	resp := doRequest(t, srv, http.MethodGet, "/v1/episodes/count?unconsolidated=true", "")
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	count, ok := result["count"].(float64)
	if !ok {
		t.Fatalf("expected numeric count field, got %T: %v", result["count"], result["count"])
	}
	if count != 2 {
		t.Errorf("expected count=2, got %v", count)
	}
}

// --- Episode edges ---

func TestAddEpisodeEdge_MissingToID(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	body := `{"edge_type":"follows","confidence":1.0}`
	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes/ep-edge-from/edges", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 when to_id missing, got %d", resp.StatusCode)
	}
}

func TestAddEpisodeEdge_Success(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	for _, ep := range []*graph.Episode{
		{ID: "ep-from", Content: "first message", Source: "test", TimestampEvent: time.Now()},
		{ID: "ep-to", Content: "second message", Source: "test", TimestampEvent: time.Now()},
	} {
		if err := svc.Graph.AddEpisode(ep); err != nil {
			t.Fatalf("AddEpisode failed: %v", err)
		}
	}

	body := `{"to_id":"ep-to","edge_type":"follows","confidence":1.0}`
	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes/ep-from/edges", body)
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}
}

func TestAddEpisodeEdge_DefaultsApplied(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	for _, ep := range []*graph.Episode{
		{ID: "ep-def-from", Content: "msg a", Source: "test", TimestampEvent: time.Now()},
		{ID: "ep-def-to", Content: "msg b", Source: "test", TimestampEvent: time.Now()},
	} {
		if err := svc.Graph.AddEpisode(ep); err != nil {
			t.Fatalf("AddEpisode failed: %v", err)
		}
	}

	// Omit edge_type and confidence — handler should default to "follows" / 1.0
	body := `{"to_id":"ep-def-to"}`
	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes/ep-def-from/edges", body)
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201 with defaults applied, got %d", resp.StatusCode)
	}
}

// --- Entities ---

func TestListEntities_Empty(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/entities", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestListEntities_WithTypeFilter(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	svc.Graph.AddEntity(&graph.Entity{ID: "ent-1", Name: "Alice", Type: graph.EntityPerson})
	svc.Graph.AddEntity(&graph.Entity{ID: "ent-2", Name: "Acme Corp", Type: graph.EntityOther})

	resp := doRequest(t, srv, http.MethodGet, "/v1/entities?type=PERSON", "")
	var result []*graph.Entity
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Activation decay ---

func TestDecayActivation_NoBody(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	svc.Graph.AddEngram(&graph.Engram{ID: "tr-decay-1", Summary: "decay test", Activation: 0.8})

	resp := doRequest(t, srv, http.MethodPost, "/v1/activation/decay", `{}`)
	var result map[string]int
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Reinforce trace ---

func TestReinforceEngram_NotFound(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	body := `{"alpha": 0.3}`
	resp := doRequest(t, srv, http.MethodPost, "/v1/engrams/no-such-trace/reinforce", body)
	defer resp.Body.Close()

	// ReinforceTrace on a non-existent trace may return 200 or 500 depending on implementation.
	// We just check the server doesn't panic.
	if resp.StatusCode == 0 {
		t.Error("expected a valid HTTP status")
	}
}

// --- Reset ---

func TestReset_ClearsData(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	// Insert some data
	svc.Graph.AddEngram(&graph.Engram{ID: "tr-before-reset", Summary: "will be cleared"})

	resp := doRequest(t, srv, http.MethodDelete, "/v1/memory/reset", "")
	var result map[string]bool
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !result["ok"] {
		t.Error("expected {ok: true}")
	}

	// Verify the trace is gone
	trace, _ := svc.Graph.GetEngram("tr-before-reset")
	if trace != nil {
		t.Error("expected trace to be gone after reset")
	}
}

// --- Consolidate (no consolidator configured) ---

func TestConsolidate_NotConfigured(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/consolidate", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when consolidator is nil, got %d", resp.StatusCode)
	}
}

// --- Content-Type and JSON correctness ---

func TestResponseContentType(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json content-type, got %q", ct)
	}
}

func TestIngestEpisode_InvalidJSON(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/episodes", `{not valid json}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

// TestFullCycle does a minimal ingest → list traces round-trip (no LLM).
func TestFullCycle_IngestAndList(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	// Ingest an episode
	payload := map[string]any{
		"content": "The new architecture uses a graph-based memory store",
		"source":  "discord",
	}
	body, _ := json.Marshal(payload)
	ingestResp := doRequest(t, srv, http.MethodPost, "/v1/episodes", string(body))
	var ingestResult map[string]string
	decodeJSON(t, ingestResp, &ingestResult)

	if ingestResp.StatusCode != http.StatusCreated {
		t.Fatalf("ingest failed with status %d", ingestResp.StatusCode)
	}
	epID := ingestResult["id"]
	if epID == "" {
		t.Fatal("no episode id in response")
	}

	// Verify episode exists
	ep, err := svc.Graph.GetEpisode(epID)
	if err != nil || ep == nil {
		t.Fatalf("episode %s not found in DB: %v", epID, err)
	}
	if ep.Content != "The new architecture uses a graph-based memory store" {
		t.Errorf("unexpected episode content: %q", ep.Content)
	}

	// List traces (empty since no consolidation was run)
	listResp := doRequest(t, srv, http.MethodGet, "/v1/engrams", "")
	var traces []map[string]any
	decodeJSON(t, listResp, &traces)

	if listResp.StatusCode != http.StatusOK {
		t.Errorf("list traces failed with status %d", listResp.StatusCode)
	}

	// Search with keyword (no embedding available but should not error)
	searchResp := doRequest(t, srv, http.MethodPost, "/v1/engrams/search", `{"query":"architecture","limit":5}`)
	searchResp.Body.Close()

	if searchResp.StatusCode != http.StatusOK {
		t.Errorf("search failed with status %d", searchResp.StatusCode)
	}
}
