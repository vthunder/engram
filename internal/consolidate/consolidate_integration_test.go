package consolidate

// Integration tests for Consolidator.Run().
// These tests exercise the full pipeline using a real graph.DB and a mock LLM.
// Claude inference is bypassed by pre-seeding episode-episode edges in the DB,
// which causes loadExistingEdges to return results and skip inferEpisodeEpisodeLinks.

import (
	"testing"
	"time"

	"github.com/vthunder/engram/internal/graph"
)

func TestRunEmpty(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)
	n, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if n != 0 {
		t.Errorf("Expected 0 traces from empty DB, got %d", n)
	}
}

// TestRunBasicConsolidation verifies that two connected episodes produce a trace.
func TestRunBasicConsolidation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	ep1 := &graph.Episode{
		ID:             "ep-basic-1",
		Content:        "We decided to use Redis for session caching",
		Author:         "alice",
		Channel:        "general",
		TimestampEvent: now,
		Embedding:      []float64{0.8, 0.1, 0.1, 0.0},
	}
	ep2 := &graph.Episode{
		ID:             "ep-basic-2",
		Content:        "Redis configuration and TTL settings to use",
		Author:         "bob",
		Channel:        "general",
		TimestampEvent: now.Add(5 * time.Minute),
		Embedding:      []float64{0.75, 0.15, 0.1, 0.0},
	}
	for _, ep := range []*graph.Episode{ep1, ep2} {
		if err := db.AddEpisode(ep); err != nil {
			t.Fatalf("AddEpisode(%s): %v", ep.ID, err)
		}
	}

	// Pre-seed a high-confidence edge so Run() skips Claude inference.
	if err := db.AddEpisodeEpisodeEdge(ep1.ID, ep2.ID, "RELATED_TO", "same topic", 0.9); err != nil {
		t.Fatalf("AddEpisodeEpisodeEdge: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	n, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if n != 1 {
		t.Errorf("Expected 1 trace created, got %d", n)
	}

	// Verify both episodes are now linked to a trace.
	for _, id := range []string{ep1.ID, ep2.ID} {
		traces, err := db.GetEpisodeTraces(id)
		if err != nil {
			t.Fatalf("GetEpisodeTraces(%s): %v", id, err)
		}
		if len(traces) == 0 {
			t.Errorf("Episode %s not linked to any trace after consolidation", id)
		}
	}

	// Verify at least one real trace exists.
	all, err := db.GetAllTraces()
	if err != nil {
		t.Fatalf("GetAllTraces: %v", err)
	}
	if len(all) == 0 {
		t.Error("No traces found after consolidation")
	}
}

// TestRunIsolatedEpisodesEachGetOwnTrace verifies that unconnected episodes
// each produce their own trace when MinGroupSize=1.
func TestRunIsolatedEpisodesEachGetOwnTrace(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	eps := []*graph.Episode{
		{
			ID:             "ep-iso-1",
			Content:        "Decided to adopt gRPC for inter-service communication",
			Author:         "alice",
			Channel:        "ch1",
			TimestampEvent: now,
		},
		{
			ID:             "ep-iso-2",
			Content:        "Chose PostgreSQL over MySQL because of JSONB support",
			Author:         "alice",
			Channel:        "ch2",
			TimestampEvent: now.Add(2 * time.Hour),
		},
	}
	for _, ep := range eps {
		if err := db.AddEpisode(ep); err != nil {
			t.Fatalf("AddEpisode(%s): %v", ep.ID, err)
		}
	}

	// No edges - each episode is its own connected component.
	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1
	n, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if n != 2 {
		t.Errorf("Expected 2 traces (one per episode), got %d", n)
	}
}

// TestRunEphemeralContentSkipped verifies that meeting countdown messages are
// skipped and don't produce real traces.
// Note: Run() may return a non-zero count even for skipped groups (it counts
// groups processed, not traces created). We verify by checking GetAllTraces().
func TestRunEphemeralContentSkipped(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &graph.Episode{
		ID:             "ep-ephemeral-1",
		Content:        "Meeting starts in 5 minutes and 30 seconds",
		Author:         "bud",
		Channel:        "notifications",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1
	_, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// No real traces should be created (ephemeral content is discarded).
	all, err := db.GetAllTraces()
	if err != nil {
		t.Fatalf("GetAllTraces: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("Expected 0 real traces after ephemeral content, got %d", len(all))
	}
}

// TestRunLowInfoEpisodesSkipped verifies that backchannel-only groups don't
// produce real traces. We verify by checking GetAllTraces() rather than the
// Run() return value (which counts processed groups, not actual traces created).
func TestRunLowInfoEpisodesSkipped(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	ep1 := &graph.Episode{
		ID:             "ep-low-1",
		Content:        "ok",
		Author:         "alice",
		Channel:        "general",
		DialogueAct:    "backchannel",
		TimestampEvent: now,
	}
	ep2 := &graph.Episode{
		ID:             "ep-low-2",
		Content:        "great",
		Author:         "bob",
		Channel:        "general",
		DialogueAct:    "backchannel",
		TimestampEvent: now.Add(1 * time.Minute),
	}
	for _, ep := range []*graph.Episode{ep1, ep2} {
		if err := db.AddEpisode(ep); err != nil {
			t.Fatalf("AddEpisode(%s): %v", ep.ID, err)
		}
	}

	// Connect them so they form a single group - still all backchannel.
	if err := db.AddEpisodeEpisodeEdge(ep1.ID, ep2.ID, "RELATED_TO", "both backchannels", 0.9); err != nil {
		t.Fatalf("AddEpisodeEpisodeEdge: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	_, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// No real traces should be created for all-backchannel content.
	all, err := db.GetAllTraces()
	if err != nil {
		t.Fatalf("GetAllTraces: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("Expected 0 real traces for low-info group, got %d", len(all))
	}
}

// TestRunIdempotent verifies that running consolidation twice on the same
// episodes doesn't create duplicate traces.
func TestRunIdempotent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &graph.Episode{
		ID:             "ep-idem-1",
		Content:        "Architecture decision: use gRPC for internal service communication",
		Author:         "alice",
		Channel:        "engineering",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1

	// First run should consolidate the episode.
	n1, err := c.Run()
	if err != nil {
		t.Fatalf("First Run() error: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("Expected 1 trace from first run, got %d", n1)
	}

	// Second run - episode is now consolidated, nothing left to process.
	n2, err := c.Run()
	if err != nil {
		t.Fatalf("Second Run() error: %v", err)
	}
	if n2 != 0 {
		t.Errorf("Expected 0 traces from second run (idempotent), got %d", n2)
	}

	// Exactly 1 real trace should exist.
	all, err := db.GetAllTraces()
	if err != nil {
		t.Fatalf("GetAllTraces: %v", err)
	}
	realTraces := 0
	for _, tr := range all {
		if tr.ID != "_ephemeral" {
			realTraces++
		}
	}
	if realTraces != 1 {
		t.Errorf("Expected exactly 1 real trace, got %d", realTraces)
	}
}

// TestRunOperationalTraceClassification verifies that state sync messages
// are classified as operational traces (faster decay, excluded from knowledge retrieval).
func TestRunOperationalTraceClassification(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &graph.Episode{
		ID:             "ep-ops-1",
		Content:        "State sync completed, pushed changes to remote",
		Author:         "bud",
		Channel:        "system",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1
	n, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if n != 1 {
		t.Fatalf("Expected 1 trace, got %d", n)
	}

	all, err := db.GetAllTraces()
	if err != nil {
		t.Fatalf("GetAllTraces: %v", err)
	}
	var created *graph.Trace
	for _, tr := range all {
		if tr.ID != "_ephemeral" {
			created = tr
			break
		}
	}
	if created == nil {
		t.Fatal("No trace found")
	}
	if created.TraceType != graph.TraceTypeOperational {
		t.Errorf("Expected TraceTypeOperational, got %q", created.TraceType)
	}
}

// TestRunKnowledgeTraceClassification verifies that decision notes with
// rationale are classified as knowledge traces.
func TestRunKnowledgeTraceClassification(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &graph.Episode{
		ID:             "ep-knowledge-1",
		Content:        "We decided to use Redis for caching because it supports pub/sub natively",
		Author:         "alice",
		Channel:        "engineering",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1
	n, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if n != 1 {
		t.Fatalf("Expected 1 trace, got %d", n)
	}

	all, err := db.GetAllTraces()
	if err != nil {
		t.Fatalf("GetAllTraces: %v", err)
	}
	var created *graph.Trace
	for _, tr := range all {
		if tr.ID != "_ephemeral" {
			created = tr
			break
		}
	}
	if created == nil {
		t.Fatal("No trace found")
	}
	if created.TraceType != graph.TraceTypeKnowledge {
		t.Errorf("Expected TraceTypeKnowledge, got %q", created.TraceType)
	}
}

// TestRunMinGroupSizeFiltering verifies that isolated episodes below MinGroupSize
// are not consolidated and remain unconsolidated (no _ephemeral link either).
func TestRunMinGroupSizeFiltering(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &graph.Episode{
		ID:             "ep-mgs-1",
		Content:        "Single standalone episode",
		Author:         "alice",
		Channel:        "general",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 2 // Requires at least 2 episodes per trace.
	n, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if n != 0 {
		t.Errorf("Expected 0 traces (episode below MinGroupSize), got %d", n)
	}

	// Episode should remain unconsolidated (not linked to any trace).
	traces, err := db.GetEpisodeTraces(ep.ID)
	if err != nil {
		t.Fatalf("GetEpisodeTraces: %v", err)
	}
	if len(traces) != 0 {
		t.Errorf("Expected episode to remain unconsolidated, but got traces: %v", traces)
	}
}

// TestRunMultipleGroups verifies that two disconnected clusters each produce
// their own trace in a single Run() call.
func TestRunMultipleGroups(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()

	// Cluster A: ep-a1 and ep-a2 connected.
	epA1 := &graph.Episode{
		ID:             "ep-mg-a1",
		Content:        "We decided to migrate to Kubernetes for better scaling",
		Author:         "alice",
		Channel:        "infra",
		TimestampEvent: now,
	}
	epA2 := &graph.Episode{
		ID:             "ep-mg-a2",
		Content:        "Kubernetes ingress configuration and load balancer setup",
		Author:         "bob",
		Channel:        "infra",
		TimestampEvent: now.Add(5 * time.Minute),
	}

	// Cluster B: ep-b1 and ep-b2 connected (different topic, different time).
	epB1 := &graph.Episode{
		ID:             "ep-mg-b1",
		Content:        "Decided to use Rust for the hot path because of zero-cost abstractions",
		Author:         "alice",
		Channel:        "lang",
		TimestampEvent: now.Add(3 * time.Hour),
	}
	epB2 := &graph.Episode{
		ID:             "ep-mg-b2",
		Content:        "Rust ownership model prevents the data races we saw in Go",
		Author:         "carol",
		Channel:        "lang",
		TimestampEvent: now.Add(3*time.Hour + 10*time.Minute),
	}

	for _, ep := range []*graph.Episode{epA1, epA2, epB1, epB2} {
		if err := db.AddEpisode(ep); err != nil {
			t.Fatalf("AddEpisode(%s): %v", ep.ID, err)
		}
	}

	if err := db.AddEpisodeEpisodeEdge(epA1.ID, epA2.ID, "RELATED_TO", "same infra topic", 0.9); err != nil {
		t.Fatalf("AddEpisodeEpisodeEdge A: %v", err)
	}
	if err := db.AddEpisodeEpisodeEdge(epB1.ID, epB2.ID, "RELATED_TO", "same lang topic", 0.85); err != nil {
		t.Fatalf("AddEpisodeEpisodeEdge B: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	n, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if n != 2 {
		t.Errorf("Expected 2 traces (two separate clusters), got %d", n)
	}

	all, err := db.GetAllTraces()
	if err != nil {
		t.Fatalf("GetAllTraces: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("Expected 2 traces in DB, got %d", len(all))
	}
}

// TestRunLowConfidenceEdgesIgnored verifies that edges with confidence < 0.7
// do not cause episodes to be merged into the same trace.
func TestRunLowConfidenceEdgesIgnored(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	ep1 := &graph.Episode{
		ID:             "ep-lce-1",
		Content:        "We decided to adopt TypeScript for the frontend",
		Author:         "alice",
		Channel:        "frontend",
		TimestampEvent: now,
	}
	ep2 := &graph.Episode{
		ID:             "ep-lce-2",
		Content:        "Backend performance improvements and profiling results",
		Author:         "bob",
		Channel:        "backend",
		TimestampEvent: now.Add(30 * time.Minute),
	}
	for _, ep := range []*graph.Episode{ep1, ep2} {
		if err := db.AddEpisode(ep); err != nil {
			t.Fatalf("AddEpisode(%s): %v", ep.ID, err)
		}
	}

	// Low-confidence edge (below 0.7 threshold) - episodes should stay separate.
	if err := db.AddEpisodeEpisodeEdge(ep1.ID, ep2.ID, "RELATED_TO", "weak link", 0.5); err != nil {
		t.Fatalf("AddEpisodeEpisodeEdge: %v", err)
	}

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1
	n, err := c.Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Each episode should consolidate independently.
	if n != 2 {
		t.Errorf("Expected 2 separate traces (low-confidence edge ignored), got %d", n)
	}
}
