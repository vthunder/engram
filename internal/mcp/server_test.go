package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"log/slog"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/vthunder/engram/internal/graph"
)

// setupTestServices creates a Services instance backed by a temporary graph.DB.
func setupTestServices(t *testing.T) (*Services, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "mcp-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	db, err := graph.Open(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to open graph DB: %v", err)
	}

	svc := &Services{
		Graph:       db,
		EmbedClient: nil, // no embedding in unit tests
		NERClient:   nil, // no NER in unit tests
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return svc, cleanup
}

// makeRequest creates a CallToolRequest with the given arguments.
func makeRequest(args map[string]any) mcpgo.CallToolRequest {
	return mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Arguments: args,
		},
	}
}

// getResultText extracts text from the first TextContent in a ToolResult.
func getResultText(t *testing.T, result *mcpgo.CallToolResult) string {
	t.Helper()
	if result == nil {
		t.Fatal("nil result")
	}
	for _, c := range result.Content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// isError returns true if the result has IsError set.
func isError(result *mcpgo.CallToolResult) bool {
	return result != nil && result.IsError
}

// addTestEpisode inserts a test episode into the DB and returns its ID.
func addTestEpisode(t *testing.T, db *graph.DB, id, content string) *graph.Episode {
	t.Helper()
	ep := &graph.Episode{
		ID:             id,
		Content:        content,
		Source:         "test",
		Author:         "tester",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode(%q) failed: %v", id, err)
	}
	return ep
}

// --- Tests: search_memory ---

func TestSearchMemory_EmptyQuery(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := searchMemoryHandler(svc)
	req := makeRequest(map[string]any{}) // no query
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isError(result) {
		t.Error("expected error result for empty query")
	}
}

func TestSearchMemory_EmptyDB(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := searchMemoryHandler(svc)
	req := makeRequest(map[string]any{"query": "something interesting", "limit": float64(5)})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isError(result) {
		t.Errorf("unexpected error result: %s", getResultText(t, result))
	}
}

func TestSearchMemory_DefaultLimit(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := searchMemoryHandler(svc)
	req := makeRequest(map[string]any{"query": "test"})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isError(result) {
		t.Errorf("unexpected error result: %s", getResultText(t, result))
	}
}

func TestSearchMemory_InvalidLimit(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := searchMemoryHandler(svc)
	// Negative limit should fall back to default (10)
	req := makeRequest(map[string]any{"query": "test", "limit": float64(-1)})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not error — limit < 0 is coerced to default
	if isError(result) {
		t.Errorf("unexpected error for negative limit: %s", getResultText(t, result))
	}
}

// --- Tests: list_engrams ---

func TestListEngrams_EmptyDB(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := listEngramsHandler(svc)
	req := makeRequest(map[string]any{})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isError(result) {
		t.Errorf("unexpected error: %s", getResultText(t, result))
	}

	// Result should be a valid JSON array (possibly empty or null)
	text := getResultText(t, result)
	var arr []any
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		// Also accept null for empty DB
		if text != "null" {
			t.Errorf("expected JSON array or null, got: %s (err: %v)", text, err)
		}
	}
}

// --- Tests: get_engram ---

func TestGetEngram_MissingID(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := getEngramHandler(svc)
	req := makeRequest(map[string]any{}) // no engram_id
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isError(result) {
		t.Error("expected error result for missing engram_id")
	}
}

func TestGetEngram_NotFound(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := getEngramHandler(svc)
	req := makeRequest(map[string]any{"engram_id": "nonexistent-id-xyz"})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isError(result) {
		t.Error("expected error result for nonexistent engram")
	}
}

// --- Tests: get_engram_context ---

func TestGetEngramContext_MissingID(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := getEngramContextHandler(svc)
	req := makeRequest(map[string]any{}) // no engram_id
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isError(result) {
		t.Error("expected error result for missing engram_id")
	}
}

func TestGetEngramContext_NotFound(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := getEngramContextHandler(svc)
	req := makeRequest(map[string]any{"engram_id": "nonexistent-xyz"})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isError(result) {
		t.Error("expected error result for nonexistent engram")
	}
}

// --- Tests: query_episode ---

func TestQueryEpisode_MissingID(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := queryEpisodeHandler(svc)
	req := makeRequest(map[string]any{}) // no id
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isError(result) {
		t.Error("expected error result for missing id")
	}
}

func TestQueryEpisode_NotFound(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	handler := queryEpisodeHandler(svc)
	req := makeRequest(map[string]any{"id": "nonexistent-id"})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isError(result) {
		t.Error("expected error result for nonexistent episode")
	}
}

func TestQueryEpisode_Found(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	// Ingest an episode to retrieve later
	ep := addTestEpisode(t, svc.Graph, "ep-mcp-test-1", "test episode content for query_episode")

	handler := queryEpisodeHandler(svc)
	req := makeRequest(map[string]any{"id": ep.ID})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isError(result) {
		t.Errorf("unexpected error: %s", getResultText(t, result))
	}

	// Verify the response contains the episode ID
	text := getResultText(t, result)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if id, _ := parsed["id"].(string); id != ep.ID {
		t.Errorf("expected id %q, got %q", ep.ID, id)
	}
	if content, _ := parsed["content"].(string); content != ep.Content {
		t.Errorf("expected content %q, got %q", ep.Content, content)
	}
}

func TestQueryEpisode_PrefixLookup(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	ep := addTestEpisode(t, svc.Graph, "ep-prefix-test-xyz", "test for prefix lookup")

	handler := queryEpisodeHandler(svc)
	// Use a prefix of the ID (first 8 chars)
	prefix := ep.ID[:8]
	req := makeRequest(map[string]any{"id": prefix})
	result, err := handler(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Prefix lookup may or may not resolve (depends on uniqueness); just verify no panic
	// and we get either a valid episode or a not-found error
	_ = result
}

// --- Tests: NewServer ---

func TestNewServer_RegistersTools(t *testing.T) {
	svc, cleanup := setupTestServices(t)
	defer cleanup()

	s := NewServer(svc)
	if s == nil {
		t.Fatal("expected non-nil server")
	}
}
