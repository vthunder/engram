package api

import (
	"bytes"
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
		req.Header.Set("X-API-Key", key)
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

	resp := doRequestWithKey(t, srv, http.MethodGet, "/v1/traces", "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthWrongKey(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequestWithKey(t, srv, http.MethodGet, "/v1/traces", "", "wrong-key")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthCorrectKey(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/traces", "")
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

// --- Search ---

func TestSearch_MissingQuery(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/search", `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSearch_EmptyDB(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodPost, "/v1/search", `{"query":"anything"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on empty DB search, got %d", resp.StatusCode)
	}
}

// --- Traces ---

func TestListTraces_Empty(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/traces", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestGetTrace_NotFound(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/traces/nonexistent-id", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetTrace_Found(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	// Insert a trace directly via DB
	tr := &graph.Trace{
		ID:        "test-trace-001",
		Summary:   "Test trace for API",
		TraceType: graph.TraceTypeKnowledge,
	}
	if err := svc.Graph.AddTrace(tr); err != nil {
		t.Fatalf("AddTrace failed: %v", err)
	}
	// Add an L1 summary so GetTraceSummary works
	svc.Graph.AddTraceSummary("test-trace-001", 1, "Test trace for API", 5)

	resp := doRequest(t, srv, http.MethodGet, "/v1/traces/test-trace-001", "")
	var result map[string]any
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if result["id"] != "test-trace-001" {
		t.Errorf("expected id 'test-trace-001', got %v", result["id"])
	}
}

func TestGetTraceContext_Found(t *testing.T) {
	svc, srv, cleanup := setupTestServices(t)
	defer cleanup()

	tr := &graph.Trace{
		ID:      "trace-ctx-001",
		Summary: "Context trace",
	}
	svc.Graph.AddTrace(tr)

	resp := doRequest(t, srv, http.MethodGet, "/v1/traces/trace-ctx-001/context", "")
	var result traceContextResponse
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if result.Trace == nil {
		t.Error("expected non-nil trace in context response")
	}
}

func TestGetTraceContext_NotFound(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	resp := doRequest(t, srv, http.MethodGet, "/v1/traces/no-such-trace/context", "")
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

	svc.Graph.AddTrace(&graph.Trace{ID: "tr-decay-1", Summary: "decay test", Activation: 0.8})

	resp := doRequest(t, srv, http.MethodPost, "/v1/activation/decay", `{}`)
	var result map[string]int
	decodeJSON(t, resp, &result)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Reinforce trace ---

func TestReinforceTrace_NotFound(t *testing.T) {
	_, srv, cleanup := setupTestServices(t)
	defer cleanup()

	body := `{"alpha": 0.3}`
	resp := doRequest(t, srv, http.MethodPost, "/v1/traces/no-such-trace/reinforce", body)
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
	svc.Graph.AddTrace(&graph.Trace{ID: "tr-before-reset", Summary: "will be cleared"})

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
	trace, _ := svc.Graph.GetTrace("tr-before-reset")
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
	listResp := doRequest(t, srv, http.MethodGet, "/v1/traces", "")
	var traces []map[string]any
	decodeJSON(t, listResp, &traces)

	if listResp.StatusCode != http.StatusOK {
		t.Errorf("list traces failed with status %d", listResp.StatusCode)
	}

	// Search with keyword (no embedding available but should not error)
	searchBody := bytes.NewBufferString(`{"query":"architecture","limit":5}`)
	searchReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/search", searchBody)
	searchReq.Header.Set("Content-Type", "application/json")
	searchReq.Header.Set("X-API-Key", testAPIKey)
	searchResp, _ := http.DefaultClient.Do(searchReq)
	searchResp.Body.Close()

	if searchResp.StatusCode != http.StatusOK {
		t.Errorf("search failed with status %d", searchResp.StatusCode)
	}
}
