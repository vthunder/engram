package consolidate

// Tests for episode clustering logic in consolidation.
// Covers: edge-based clustering, connected components, confidence thresholds, edge cases.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vthunder/engram/internal/graph"
)

// setupTestDB creates a temporary test database
func setupTestDB(t *testing.T) (*graph.DB, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "consolidate-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	db, err := graph.Open(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to open database: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return db, cleanup
}

// mockLLM implements LLMClient for tests
type mockLLM struct{}

func (m *mockLLM) Embed(text string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3, 0.4}, nil
}

func (m *mockLLM) Generate(prompt string) (string, error) {
	// For consolidation prompts, extract fragment content so that classification
	// functions (isEphemeralContent, classifyEngramType) work correctly in tests.
	const fragHeader = "Fragments ("
	const memMarker = "\nMemory:"
	if idx := strings.Index(prompt, fragHeader); idx >= 0 {
		lineEnd := strings.Index(prompt[idx:], "\n")
		if lineEnd >= 0 {
			section := prompt[idx+lineEnd+1:]
			if memEnd := strings.Index(section, memMarker); memEnd >= 0 {
				section = section[:memEnd]
			}
			var parts []string
			for _, line := range strings.Split(strings.TrimSpace(section), "\n") {
				if closing := strings.Index(line, "]: "); closing >= 0 {
					line = line[closing+3:]
				}
				if line != "" {
					parts = append(parts, line)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " "), nil
			}
		}
	}
	// Fallback for non-consolidation prompts (e.g. pyramid generation)
	if len(prompt) > 100 {
		return prompt[:100], nil
	}
	return prompt, nil
}

func TestClusterEpisodesByEdgesEmpty(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)

	groups, existing := c.clusterEpisodesByEdges(nil, nil)
	if groups != nil || existing != nil {
		t.Error("Expected nil for empty episodes")
	}

	groups, existing = c.clusterEpisodesByEdges([]*graph.Episode{}, nil)
	if groups != nil || existing != nil {
		t.Error("Expected nil for empty episodes slice")
	}
}

func TestClusterEpisodesByEdgesNoEdges(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1

	now := time.Now()
	episodes := []*graph.Episode{
		{ID: "ep-1", Content: "First message", Channel: "general", TimestampEvent: now},
		{ID: "ep-2", Content: "Second message", Channel: "general", TimestampEvent: now.Add(10 * time.Minute)},
	}

	// No edges = each episode is its own component
	groups, _ := c.clusterEpisodesByEdges(episodes, nil)

	if len(groups) != 2 {
		t.Fatalf("Expected 2 groups (no edges = separate components), got %d", len(groups))
	}
}

func TestClusterEpisodesByEdgesHighConfidence(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1

	now := time.Now()
	episodes := []*graph.Episode{
		{ID: "ep-1", Content: "First message", Channel: "general", TimestampEvent: now},
		{ID: "ep-2", Content: "Second message", Channel: "general", TimestampEvent: now.Add(10 * time.Minute)},
		{ID: "ep-3", Content: "Third message", Channel: "general", TimestampEvent: now.Add(1 * time.Hour)},
	}

	// High-confidence edge between ep-1 and ep-2; ep-3 unconnected
	edges := []EpisodeEdge{
		{FromID: "ep-1", ToID: "ep-2", Confidence: 0.9},
	}

	groups, _ := c.clusterEpisodesByEdges(episodes, edges)

	// Should have 2 groups: (ep-1, ep-2) and (ep-3)
	if len(groups) != 2 {
		t.Fatalf("Expected 2 groups, got %d", len(groups))
	}

	// Find the group with ep-1
	var joinedGroup *episodeGroup
	for _, g := range groups {
		for _, ep := range g.episodes {
			if ep.ID == "ep-1" {
				joinedGroup = g
				break
			}
		}
	}

	if joinedGroup == nil {
		t.Fatal("Could not find group containing ep-1")
	}

	if len(joinedGroup.episodes) != 2 {
		t.Errorf("Expected 2 episodes in ep-1's group, got %d", len(joinedGroup.episodes))
	}
}

func TestClusterEpisodesByEdgesLowConfidenceSkipped(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1

	now := time.Now()
	episodes := []*graph.Episode{
		{ID: "ep-1", Content: "First", Channel: "ch1", TimestampEvent: now},
		{ID: "ep-2", Content: "Second", Channel: "ch2", TimestampEvent: now.Add(5 * time.Minute)},
	}

	// Low-confidence edge (below 0.7 threshold) should be ignored
	edges := []EpisodeEdge{
		{FromID: "ep-1", ToID: "ep-2", Confidence: 0.5},
	}

	groups, _ := c.clusterEpisodesByEdges(episodes, edges)

	// Should have 2 separate groups since edge confidence is too low
	if len(groups) != 2 {
		t.Fatalf("Expected 2 separate groups (low confidence edge ignored), got %d", len(groups))
	}
}

func TestClusterEpisodesByEdgesTransitiveChain(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1

	now := time.Now()
	episodes := []*graph.Episode{
		{ID: "ep-1", Content: "A", Channel: "ch1", TimestampEvent: now},
		{ID: "ep-2", Content: "B", Channel: "ch2", TimestampEvent: now.Add(5 * time.Minute)},
		{ID: "ep-3", Content: "C", Channel: "ch3", TimestampEvent: now.Add(10 * time.Minute)},
	}

	// Chain: ep-1 <-> ep-2 <-> ep-3 (transitive)
	edges := []EpisodeEdge{
		{FromID: "ep-1", ToID: "ep-2", Confidence: 0.9},
		{FromID: "ep-2", ToID: "ep-3", Confidence: 0.8},
	}

	groups, _ := c.clusterEpisodesByEdges(episodes, edges)

	// All 3 should be in one group via transitivity
	if len(groups) != 1 {
		t.Fatalf("Expected 1 group (transitive chain), got %d", len(groups))
	}

	if len(groups[0].episodes) != 3 {
		t.Errorf("Expected 3 episodes in group, got %d", len(groups[0].episodes))
	}
}

func TestClusterEpisodesByEdgesMinGroupSize(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 2 // Require at least 2 episodes

	now := time.Now()
	episodes := []*graph.Episode{
		{ID: "ep-1", Content: "First", Channel: "general", TimestampEvent: now},
		{ID: "ep-2", Content: "Second", Channel: "general", TimestampEvent: now.Add(5 * time.Minute)},
		{ID: "ep-3", Content: "Lonely", Channel: "random", TimestampEvent: now.Add(2 * time.Hour)},
	}

	// ep-1 and ep-2 connected, ep-3 isolated
	edges := []EpisodeEdge{
		{FromID: "ep-1", ToID: "ep-2", Confidence: 0.9},
	}

	groups, _ := c.clusterEpisodesByEdges(episodes, edges)

	// Only ep-1+ep-2 group should be returned; ep-3 alone is below MinGroupSize
	if len(groups) != 1 {
		t.Fatalf("Expected 1 group (ep-3 filtered by MinGroupSize), got %d", len(groups))
	}

	if len(groups[0].episodes) != 2 {
		t.Errorf("Expected 2 episodes in group, got %d", len(groups[0].episodes))
	}
}

func TestClusterEpisodesByEdgesMultipleDisconnected(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)
	c.MinGroupSize = 1

	now := time.Now()
	episodes := []*graph.Episode{
		{ID: "ep-1", Content: "A", Channel: "ch1", TimestampEvent: now},
		{ID: "ep-2", Content: "B", Channel: "ch1", TimestampEvent: now.Add(1 * time.Minute)},
		{ID: "ep-3", Content: "C", Channel: "ch2", TimestampEvent: now.Add(2 * time.Minute)},
		{ID: "ep-4", Content: "D", Channel: "ch2", TimestampEvent: now.Add(3 * time.Minute)},
	}

	// Two separate components: (ep-1, ep-2) and (ep-3, ep-4)
	edges := []EpisodeEdge{
		{FromID: "ep-1", ToID: "ep-2", Confidence: 0.95},
		{FromID: "ep-3", ToID: "ep-4", Confidence: 0.85},
	}

	groups, _ := c.clusterEpisodesByEdges(episodes, edges)

	if len(groups) != 2 {
		t.Fatalf("Expected 2 groups (2 disconnected components), got %d", len(groups))
	}

	for _, g := range groups {
		if len(g.episodes) != 2 {
			t.Errorf("Expected 2 episodes per group, got %d", len(g.episodes))
		}
	}
}

func TestIsAllLowInfo(t *testing.T) {
	tests := []struct {
		name     string
		episodes []*graph.Episode
		expected bool
	}{
		{
			name:     "empty",
			episodes: []*graph.Episode{},
			expected: true,
		},
		{
			name: "all backchannels",
			episodes: []*graph.Episode{
				{DialogueAct: "backchannel"},
				{DialogueAct: "backchannel"},
			},
			expected: true,
		},
		{
			name: "all greetings",
			episodes: []*graph.Episode{
				{DialogueAct: "greeting"},
				{DialogueAct: "greeting"},
			},
			expected: true,
		},
		{
			name: "mixed low info",
			episodes: []*graph.Episode{
				{DialogueAct: "backchannel"},
				{DialogueAct: "greeting"},
			},
			expected: true,
		},
		{
			name: "one substantive",
			episodes: []*graph.Episode{
				{DialogueAct: "backchannel"},
				{DialogueAct: "statement"},
			},
			expected: false,
		},
		{
			name: "content fallback - low info",
			episodes: []*graph.Episode{
				{Content: "ok"},
				{Content: "great"},
			},
			expected: true,
		},
		{
			name: "content fallback - substantive",
			episodes: []*graph.Episode{
				{Content: "ok"},
				{Content: "Let me explain the architecture"},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isAllLowInfo(tc.episodes)
			if result != tc.expected {
				t.Errorf("isAllLowInfo() = %v, expected %v", result, tc.expected)
			}
		})
	}
}

func TestClassifyEngramType(t *testing.T) {
	tests := []struct {
		name     string
		summary  string
		expected graph.EngramType
	}{
		{
			name:     "meeting reminder",
			summary:  "[Past] Bud: Upcoming meeting in 10 minutes: Sprint planning",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "meeting reminder - starts soon",
			summary:  "[Past] Bud: Sprint Planning for Nightshade starts soon",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "meeting reminder - starts in with time",
			summary:  "[Past] Bud: Heads up - DevOps Sprint Planning starts in 13m37s.",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "meeting reminder - Google Meet link",
			summary:  "[Past] Bud: Upcoming DevOps Sprint Planning meeting starting soon https://meet.google.com/abc-defg-hij",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "meeting reminder - meeting starts",
			summary:  "[Past] Bud: DA Sprint Planning meeting starts in 10 minutes",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "meeting reminder - scheduled to start soon",
			summary:  "[Past] An unblock light node development meeting is scheduled to start soon; the link is provided.",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "state sync",
			summary:  "[Past] Bud: State sync completed, pushed changes",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "idle wake",
			summary:  "[Past] No actionable work found during wake",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "knowledge - decision",
			summary:  "[Past] Decided to use Redis for caching",
			expected: graph.EngramTypeKnowledge,
		},
		{
			name:     "knowledge - preference",
			summary:  "[Past] User prefers morning check-ins",
			expected: graph.EngramTypeKnowledge,
		},
		{
			name:     "knowledge - fact",
			summary:  "[Past] Sarah is the new PM for Project Alpha",
			expected: graph.EngramTypeKnowledge,
		},
		{
			name:     "knowledge - meeting discussion",
			summary:  "[Past] We discussed the sprint planning process and decided to move it to Mondays",
			expected: graph.EngramTypeKnowledge,
		},
		// Dev work patterns - operational (no decision rationale)
		{
			name:     "dev work - updated",
			summary:  "[Past] Updated Budget to use output tokens",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "dev work - implemented",
			summary:  "[Past] Implemented FOLLOWS edges between episodes",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "dev work - fixed",
			summary:  "[Past] Fixed token metrics display",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "dev work - added",
			summary:  "[Past] Added entity extraction to Bud responses",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "dev work - explored",
			summary:  "[Past] Explored WNUT 2017 NER benchmark for entity extraction",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "dev work - researched",
			summary:  "[Past] Researched spreading activation parameters",
			expected: graph.EngramTypeOperational,
		},
		{
			name:     "dev work - pruned",
			summary:  "[Past] Pruned 32 bad PRODUCT entities from the database",
			expected: graph.EngramTypeOperational,
		},
		// Dev work with knowledge indicators - should stay knowledge
		{
			name:     "dev work - with decision",
			summary:  "[Past] Updated caching layer because Redis was causing latency issues",
			expected: graph.EngramTypeKnowledge,
		},
		{
			name:     "dev work - with finding",
			summary:  "[Past] Explored entropy filter and finding was that it blocks all user messages",
			expected: graph.EngramTypeKnowledge,
		},
		{
			name:     "dev work - with root cause",
			summary:  "[Past] Fixed entity extraction bug, root cause was missing null check",
			expected: graph.EngramTypeKnowledge,
		},
		{
			name:     "dev work - with approach",
			summary:  "[Past] Implemented two-pass extraction approach for better precision",
			expected: graph.EngramTypeKnowledge,
		},
		{
			name:     "dev work - with chose",
			summary:  "[Past] Refactored auth module, chose JWT over sessions for scalability",
			expected: graph.EngramTypeKnowledge,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := classifyEngramType(tc.summary, nil)
			if result != tc.expected {
				t.Errorf("classifyEngramType(%q) = %v, expected %v", tc.summary, result, tc.expected)
			}
		})
	}
}

func TestIsEphemeralContent(t *testing.T) {
	tests := []struct {
		name     string
		summary  string
		expected bool
	}{
		{
			name:     "countdown",
			summary:  "[Past] Meeting in 5 minutes and 30 seconds",
			expected: true,
		},
		{
			name:     "starting in",
			summary:  "[Past] Starting in 10 minutes",
			expected: true,
		},
		{
			name:     "starts in",
			summary:  "[Past] Meeting starts in 15 minutes",
			expected: true,
		},
		{
			name:     "not ephemeral - decision about meeting",
			summary:  "[Past] Decided the meeting starts in the afternoon, specifically at 2pm. This is important because...",
			expected: false, // Long enough to not be ephemeral
		},
		{
			name:     "not ephemeral - normal content",
			summary:  "[Past] Discussed the new architecture for the memory system",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isEphemeralContent(tc.summary)
			if result != tc.expected {
				t.Errorf("isEphemeralContent(%q) = %v, expected %v", tc.summary, result, tc.expected)
			}
		})
	}
}

func TestCalculateCentroid(t *testing.T) {
	episodes := []*graph.Episode{
		{ID: "ep-1", Embedding: []float64{1.0, 0.0, 0.0}},
		{ID: "ep-2", Embedding: []float64{0.0, 1.0, 0.0}},
		{ID: "ep-3", Embedding: []float64{0.0, 0.0, 1.0}},
	}

	centroid := calculateCentroid(episodes)

	if len(centroid) != 3 {
		t.Fatalf("Expected centroid of length 3, got %d", len(centroid))
	}

	// Centroid should be (1/3, 1/3, 1/3)
	expected := 1.0 / 3.0
	tolerance := 0.001
	for i, v := range centroid {
		if v < expected-tolerance || v > expected+tolerance {
			t.Errorf("centroid[%d] = %f, expected ~%f", i, v, expected)
		}
	}
}

func TestCalculateCentroidEmpty(t *testing.T) {
	centroid := calculateCentroid(nil)
	if centroid != nil {
		t.Error("Expected nil for empty input")
	}

	centroid = calculateCentroid([]*graph.Episode{})
	if centroid != nil {
		t.Error("Expected nil for empty slice")
	}
}

func TestCalculateCentroidNoEmbeddings(t *testing.T) {
	episodes := []*graph.Episode{
		{ID: "ep-1", Embedding: nil},
		{ID: "ep-2", Embedding: []float64{}},
	}

	centroid := calculateCentroid(episodes)
	if centroid != nil {
		t.Error("Expected nil when no episodes have embeddings")
	}
}

// TestLinkEpisodesToRelatedEngrams verifies that episodes are linked to semantically
// similar existing traces via episode_trace_edges.
func TestLinkEpisodesToRelatedEngrams(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)

	now := time.Now()

	// Create an existing trace with a high-similarity embedding
	existingTrace := &graph.Engram{
		ID:        "engram-existing-abc",
		Summary:   "Discussion about entity extraction and NER",
		Topic:     "conversation",
		EngramType: graph.EngramTypeKnowledge,
		Embedding: []float64{0.9, 0.1, 0.0, 0.0}, // Similar to episode below
		CreatedAt: now.Add(-1 * time.Hour),
	}
	if err := db.AddEngram(existingTrace); err != nil {
		t.Fatalf("Failed to add existing trace: %v", err)
	}

	// Create a different trace with a low-similarity embedding (should not be linked)
	distantTrace := &graph.Engram{
		ID:        "engram-distant-xyz",
		Summary:   "Calendar event: sprint planning meeting",
		Topic:     "conversation",
		EngramType: graph.EngramTypeOperational,
		Embedding: []float64{0.0, 0.0, 0.9, 0.1}, // Very different
		CreatedAt: now.Add(-2 * time.Hour),
	}
	if err := db.AddEngram(distantTrace); err != nil {
		t.Fatalf("Failed to add distant trace: %v", err)
	}

	// Create a primary trace that the episode will belong to
	primaryTrace := &graph.Engram{
		ID:        "engram-primary-episode",
		Summary:   "New episode about NER extraction quality",
		Topic:     "conversation",
		EngramType: graph.EngramTypeKnowledge,
		Embedding: []float64{0.8, 0.2, 0.0, 0.0},
		CreatedAt: now,
	}
	if err := db.AddEngram(primaryTrace); err != nil {
		t.Fatalf("Failed to add primary trace: %v", err)
	}

	// Create an episode with a high-similarity embedding to existingTrace
	ep := &graph.Episode{
		ID:             "ep-ner-discussion",
		Content:        "The NER sidecar is working well for entity extraction",
		Author:         "thunder",
		Channel:        "general",
		TimestampEvent: now,
		Embedding:      []float64{0.88, 0.12, 0.0, 0.0}, // Similarity ~0.99 to existingTrace
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("Failed to add episode: %v", err)
	}

	// Link episode to its primary trace
	if err := db.LinkEngramToSource("engram-primary-episode", ep.ID); err != nil {
		t.Fatalf("Failed to link episode to primary trace: %v", err)
	}

	// Run the linking function
	linked := c.linkEpisodesToRelatedEngrams([]*graph.Episode{ep})

	// Should have linked to existingTrace (similarity ~0.99 > 0.80)
	// Should NOT have linked to distantTrace (similarity ~0.0 < 0.80)
	// Should NOT have linked to primaryTrace (it's excluded as the episode's own trace)
	if linked != 1 {
		t.Errorf("Expected 1 episode→trace edge, got %d", linked)
	}

	// Verify the edge exists to existingTrace
	traceIDs, err := db.GetEngramsReferencedByEpisode(ep.ID)
	if err != nil {
		t.Fatalf("GetEngramsReferencedByEpisode failed: %v", err)
	}
	if len(traceIDs) != 1 {
		t.Fatalf("Expected 1 trace reference, got %d: %v", len(traceIDs), traceIDs)
	}
	if traceIDs[0] != "engram-existing-abc" {
		t.Errorf("Expected link to trace-existing-abc, got %s", traceIDs[0])
	}

	// Verify no link to the primary trace or distant trace
	for _, id := range traceIDs {
		if id == "engram-primary-episode" {
			t.Error("Should not have linked episode to its own primary trace")
		}
		if id == "engram-distant-xyz" {
			t.Error("Should not have linked episode to dissimilar trace")
		}
	}
}

// TestLinkEpisodesToRelatedEngramsNoEmbedding verifies episodes without embeddings are skipped.
func TestLinkEpisodesToRelatedEngramsNoEmbedding(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	c := NewConsolidator(db, &mockLLM{}, nil)

	ep := &graph.Episode{
		ID:             "ep-no-embedding",
		Content:        "some content",
		Author:         "thunder",
		Channel:        "general",
		TimestampEvent: time.Now(),
		Embedding:      nil, // No embedding
	}

	linked := c.linkEpisodesToRelatedEngrams([]*graph.Episode{ep})
	if linked != 0 {
		t.Errorf("Expected 0 links for episode without embedding, got %d", linked)
	}
}
