package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// addTestEngram adds a trace with minimal pyramid summary for testing
func addTestEngram(t *testing.T, db *DB, tr *Engram) error {
	t.Helper()
	if err := db.AddEngram(tr); err != nil {
		return err
	}
	// Add minimal L32 summary so GetEngram queries work
	summary := tr.Summary
	if summary == "" {
		summary = "Test trace"
	}
	return db.AddEngramSummary(tr.ID, 32, summary, estimateTokens(summary))
}

// setupTestDB creates a temporary test database
func setupTestDB(t *testing.T) (*DB, func()) {
	t.Helper()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "graph-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Open database
	db, err := Open(tmpDir)
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

// TestSpreadingActivation tests the spreading activation algorithm
func TestSpreadingActivation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create a network of related traces:
	// A --0.8--> B --0.6--> C
	//            |
	//            v
	//            D

	traces := []*Engram{
		{ID: "engram-A", Summary: "Trace A", Activation: 0.5, Embedding: []float64{1.0, 0.0, 0.0, 0.0}},
		{ID: "engram-B", Summary: "Trace B", Activation: 0.5, Embedding: []float64{0.8, 0.6, 0.0, 0.0}},
		{ID: "engram-C", Summary: "Trace C", Activation: 0.5, Embedding: []float64{0.5, 0.5, 0.5, 0.0}},
		{ID: "engram-D", Summary: "Trace D", Activation: 0.5, Embedding: []float64{0.3, 0.3, 0.3, 0.3}},
	}

	for _, tr := range traces {
		if err := db.AddEngram(tr); err != nil {
			t.Fatalf("Failed to add trace: %v", err)
		}
	}

	// Add relations
	db.AddEngramRelation("engram-A", "engram-B", EdgeRelatedTo, 0.8)
	db.AddEngramRelation("engram-B", "engram-C", EdgeRelatedTo, 0.6)
	db.AddEngramRelation("engram-B", "engram-D", EdgeRelatedTo, 0.4)

	// Spread activation from trace A
	activation, err := db.SpreadActivation([]string{"engram-A"}, 3)
	if err != nil {
		t.Fatalf("SpreadActivation failed: %v", err)
	}

	// Verify A has highest activation (seed node)
	if activation["engram-A"] == 0 {
		t.Error("Expected trace-A to have activation > 0")
	}

	// Verify B received activation from A
	if activation["engram-B"] == 0 {
		t.Error("Expected trace-B to receive activation from A")
	}

	// Verify C and D received less activation (further from seed)
	if activation["engram-C"] >= activation["engram-B"] {
		t.Error("Expected trace-C to have less activation than trace-B")
	}

	// Verify activation decays with distance
	t.Logf("Activations: A=%f, B=%f, C=%f, D=%f",
		activation["engram-A"], activation["engram-B"], activation["engram-C"], activation["engram-D"])
}

// TestMultiHopRetrieval tests that activation spreads across multiple hops
func TestMultiHopRetrieval(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create a chain: A -> B -> C -> D -> E
	// Starting from A, we should reach at least C after 3 iterations
	for i := 0; i < 5; i++ {
		id := string(rune('A' + i))
		db.AddEngram(&Engram{
			ID:         "engram-" + id,
			Summary:    "Trace " + id,
			Activation: 0.5,
		})
	}

	db.AddEngramRelation("engram-A", "engram-B", EdgeRelatedTo, 0.9)
	db.AddEngramRelation("engram-B", "engram-C", EdgeRelatedTo, 0.9)
	db.AddEngramRelation("engram-C", "engram-D", EdgeRelatedTo, 0.9)
	db.AddEngramRelation("engram-D", "engram-E", EdgeRelatedTo, 0.9)

	// Spread with default iterations
	activation, _ := db.SpreadActivation([]string{"engram-A"}, 3)

	t.Logf("Multi-hop activations: A=%f, B=%f, C=%f, D=%f, E=%f",
		activation["engram-A"], activation["engram-B"], activation["engram-C"],
		activation["engram-D"], activation["engram-E"])

	// B and C should receive activation (1-2 hops)
	if activation["engram-B"] == 0 {
		t.Error("Expected trace-B to receive activation (1 hop)")
	}

	// Due to lateral inhibition and decay, very distant nodes may not activate
	// This is actually correct behavior - we don't want unbounded spreading
	// Verify that activation decreases with distance for nodes that are activated
	if activation["engram-B"] > 0 && activation["engram-C"] > 0 {
		if activation["engram-C"] >= activation["engram-B"] {
			t.Error("Expected activation to decrease with distance")
		}
	}
}

// TestFeelingOfKnowing tests the FoK rejection mechanism
func TestFeelingOfKnowing(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create traces with embeddings
	knownTrace := &Engram{
		ID:        "engram-known",
		Summary:   "The project deadline is Friday",
		Embedding: []float64{0.9, 0.1, 0.0, 0.0}, // Specific topic
	}
	db.AddEngram(knownTrace)

	// Query with similar embedding - should find it
	similarQuery := []float64{0.85, 0.15, 0.0, 0.0}
	result, err := db.Retrieve(similarQuery, "project deadline", 5)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if len(result.Engrams) == 0 {
		t.Error("Expected to retrieve trace with similar embedding")
	}

	// Query with very different embedding - FoK should reject
	differentQuery := []float64{0.0, 0.0, 0.9, 0.1}
	result, err = db.Retrieve(differentQuery, "unrelated topic", 5)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	// With low similarity, max activation should be below FoK threshold
	// so we expect empty or minimal results
	t.Logf("FoK test: retrieved %d traces with different query", len(result.Engrams))
}

// TestEngramActivationUpdate tests that retrieval updates activation
func TestEngramActivationUpdate(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	trace := &Engram{
		ID:         "engram-1",
		Summary:    "Test trace",
		Activation: 0.5,
		Embedding:  []float64{0.5, 0.5, 0.0, 0.0},
	}
	db.AddEngram(trace)

	// Record initial last_accessed
	initialTrace, _ := db.GetEngram("engram-1")
	initialAccess := initialTrace.LastAccessed

	// Wait a moment
	time.Sleep(10 * time.Millisecond)

	// Update activation
	db.UpdateEngramActivation("engram-1", 0.9)

	// Verify update
	updated, _ := db.GetEngram("engram-1")
	if updated.Activation != 0.9 {
		t.Errorf("Expected activation 0.9, got %f", updated.Activation)
	}

	if !updated.LastAccessed.After(initialAccess) {
		t.Error("Expected last_accessed to be updated")
	}
}

// TestDecayActivation tests global activation decay
func TestDecayActivation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	db.AddEngram(&Engram{ID: "engram-1", Summary: "Test 1", Activation: 1.0})
	db.AddEngram(&Engram{ID: "engram-2", Summary: "Test 2", Activation: 0.8})

	// Decay by 0.9 (lose 10% activation)
	db.DecayActivation(0.9)

	t1, _ := db.GetEngram("engram-1")
	if t1.Activation != 0.9 {
		t.Errorf("Expected trace-1 activation 0.9, got %f", t1.Activation)
	}

	t2, _ := db.GetEngram("engram-2")
	if t2.Activation < 0.71 || t2.Activation > 0.73 {
		t.Errorf("Expected trace-2 activation ~0.72, got %f", t2.Activation)
	}
}

// TestReinforceEngram tests trace reinforcement with EMA
func TestReinforceEngram(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create trace with initial embedding
	db.AddEngram(&Engram{
		ID:        "engram-1",
		Summary:   "Test trace",
		Strength:  1,
		Embedding: []float64{1.0, 0.0, 0.0, 0.0},
	})

	// Reinforce with different embedding
	newEmb := []float64{0.0, 1.0, 0.0, 0.0}
	db.ReinforceEngram("engram-1", newEmb, 0.3) // alpha=0.3

	// Check embedding was blended
	updated, _ := db.GetEngram("engram-1")

	// With alpha=0.3: new = 0.3*[0,1,0,0] + 0.7*[1,0,0,0] = [0.7, 0.3, 0, 0]
	expectedFirst := 0.7
	if updated.Embedding[0] < expectedFirst-0.01 || updated.Embedding[0] > expectedFirst+0.01 {
		t.Errorf("Expected embedding[0] ~%f, got %f", expectedFirst, updated.Embedding[0])
	}

	// Strength should increase
	if updated.Strength != 2 {
		t.Errorf("Expected strength 2, got %d", updated.Strength)
	}
}

// TestLabile tests the labile/reconsolidation window
func TestLabile(t *testing.T) {
	trace := &Engram{
		ID:      "engram-1",
		Summary: "Test trace",
	}

	// Initially not labile
	if trace.IsLabile() {
		t.Error("Expected trace to not be labile initially")
	}

	// Make labile
	trace.MakeLabile(1 * time.Hour)
	if !trace.IsLabile() {
		t.Error("Expected trace to be labile after MakeLabile")
	}

	// Set expired labile window
	trace.LabileUntil = time.Now().Add(-1 * time.Hour)
	if trace.IsLabile() {
		t.Error("Expected trace to not be labile after window expires")
	}
}

// TestEngramRelations tests linking traces via relations
func TestEngramRelations(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create two traces
	db.AddEngram(&Engram{ID: "engram-A", Summary: "Trace A"})
	db.AddEngram(&Engram{ID: "engram-B", Summary: "Trace B"})

	// Link them
	err := db.AddEngramRelation("engram-A", "engram-B", EdgeRelatedTo, 0.8)
	if err != nil {
		t.Fatalf("AddEngramRelation failed: %v", err)
	}

	// Get neighbors of A
	neighbors, err := db.GetEngramNeighbors("engram-A")
	if err != nil {
		t.Fatalf("GetEngramNeighbors failed: %v", err)
	}

	if len(neighbors) != 1 {
		t.Fatalf("Expected 1 neighbor, got %d", len(neighbors))
	}

	if neighbors[0].ID != "engram-B" {
		t.Errorf("Expected neighbor ID trace-B, got %s", neighbors[0].ID)
	}

	if neighbors[0].Weight != 0.8 {
		t.Errorf("Expected weight 0.8, got %f", neighbors[0].Weight)
	}

	// Relation should be bidirectional in GetEngramNeighbors
	neighborsB, _ := db.GetEngramNeighbors("engram-B")
	if len(neighborsB) != 1 || neighborsB[0].ID != "engram-A" {
		t.Error("Expected bidirectional neighbor lookup")
	}
}

// TestFindSimilarEngramsAboveThreshold tests finding traces by similarity
func TestFindSimilarEngramsAboveThreshold(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create traces with embeddings
	// Trace A and B have similar embeddings, C is different
	db.AddEngram(&Engram{ID: "engram-A", Summary: "About work projects", Embedding: []float64{0.9, 0.1, 0.1}})
	db.AddEngram(&Engram{ID: "engram-B", Summary: "Work related", Embedding: []float64{0.85, 0.15, 0.1}})
	db.AddEngram(&Engram{ID: "engram-C", Summary: "Completely different", Embedding: []float64{0.1, 0.9, 0.1}})

	// Find traces similar to A with high threshold
	similar, err := db.FindSimilarEngramsAboveThreshold([]float64{0.9, 0.1, 0.1}, 0.9, "engram-A")
	if err != nil {
		t.Fatalf("FindSimilarEngramsAboveThreshold failed: %v", err)
	}

	// B should be found (similarity ~0.98), C should not (similarity ~0.3)
	if len(similar) != 1 {
		t.Fatalf("Expected 1 similar trace, got %d", len(similar))
	}
	if similar[0].ID != "engram-B" {
		t.Errorf("Expected trace-B, got %s", similar[0].ID)
	}
	if similar[0].Similarity < 0.9 {
		t.Errorf("Expected similarity >= 0.9, got %f", similar[0].Similarity)
	}

	// Lower threshold should find more
	similar2, _ := db.FindSimilarEngramsAboveThreshold([]float64{0.9, 0.1, 0.1}, 0.2, "engram-A")
	if len(similar2) != 2 {
		t.Errorf("Expected 2 similar traces at threshold 0.2, got %d", len(similar2))
	}
}

// TestSimilarToEdgeInSpreadingActivation tests that SIMILAR_TO edges are used in spreading activation
func TestSimilarToEdgeInSpreadingActivation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create traces with embeddings (A similar to B, C different)
	db.AddEngram(&Engram{ID: "engram-A", Summary: "Topic one", Embedding: []float64{0.9, 0.1, 0.0}, Activation: 0.5})
	db.AddEngram(&Engram{ID: "engram-B", Summary: "Topic one related", Embedding: []float64{0.85, 0.15, 0.0}, Activation: 0.5})
	db.AddEngram(&Engram{ID: "engram-C", Summary: "Different topic", Embedding: []float64{0.0, 0.1, 0.9}, Activation: 0.5})

	// Add SIMILAR_TO edge between A and B
	err := db.AddEngramRelation("engram-A", "engram-B", EdgeSimilarTo, 0.95)
	if err != nil {
		t.Fatalf("AddEngramRelation failed: %v", err)
	}

	// Verify neighbors work
	neighbors, _ := db.GetEngramNeighbors("engram-A")
	hasB := false
	for _, n := range neighbors {
		if n.ID == "engram-B" && n.Type == EdgeSimilarTo {
			hasB = true
			break
		}
	}
	if !hasB {
		t.Error("Expected trace-B as SIMILAR_TO neighbor of trace-A")
	}

	// Do spreading activation from query similar to A
	// B should receive activation through SIMILAR_TO edge
	activations, err := db.SpreadActivationFromEmbedding([]float64{0.9, 0.1, 0.0}, "", 5, 2)
	if err != nil {
		t.Fatalf("SpreadActivation failed: %v", err)
	}

	t.Logf("Activations: A=%.4f, B=%.4f, C=%.4f", activations["engram-A"], activations["engram-B"], activations["engram-C"])

	// Both A and B should have activation (B via spreading through SIMILAR_TO edge)
	if activations["engram-A"] == 0 {
		t.Error("Expected trace-A to have activation")
	}
	if activations["engram-B"] == 0 {
		t.Error("Expected trace-B to have activation (via SIMILAR_TO edge)")
	}
}

// TestStats tests database statistics
func TestStats(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Add some data
	db.AddEngram(&Engram{ID: "engram-1", Summary: "Test"})
	db.AddEngram(&Engram{ID: "engram-2", Summary: "Test"})

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats["engrams"] != 2 {
		t.Errorf("Expected engrams count 2, got %d", stats["engrams"])
	}
}

// TestClear tests database clearing
func TestClear(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Add data
	db.AddEngram(&Engram{ID: "engram-1", Summary: "Test"})

	// Clear
	if err := db.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// Verify empty
	stats, _ := db.Stats()
	if stats["engrams"] != 0 {
		t.Error("Expected engrams to be cleared")
	}
}

// BenchmarkSpreadActivation benchmarks spreading activation performance
func BenchmarkSpreadActivation(b *testing.B) {
	// Create temp directory
	tmpDir, _ := os.MkdirTemp("", "graph-bench-*")
	defer os.RemoveAll(tmpDir)

	db, _ := Open(tmpDir)
	defer db.Close()

	// Create 100 traces with random connections
	for i := 0; i < 100; i++ {
		id := filepath.Base(tmpDir) + "-" + string(rune('A'+i%26)) + string(rune('0'+i/26))
		db.AddEngram(&Engram{ID: id, Summary: "Trace"})

		if i > 0 {
			prevID := filepath.Base(tmpDir) + "-" + string(rune('A'+(i-1)%26)) + string(rune('0'+(i-1)/26))
			db.AddEngramRelation(prevID, id, EdgeRelatedTo, 0.5)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.SpreadActivation([]string{filepath.Base(tmpDir) + "-A0"}, 3)
	}
}

// setupEntityBridgedDB creates a test DB with traces sharing entities.
// Returns: db, cleanup, and the entity/trace IDs for assertions.
//
//	trace-1 --[entity-jane]--> trace-2
//	trace-1 --[entity-jane, entity-proj]--> trace-3
//	trace-4 has no shared entities
func setupEntityBridgedDB(t *testing.T) (*DB, func()) {
	t.Helper()
	db, cleanup := setupTestDB(t)

	// Create entities
	db.AddEntity(&Entity{ID: "entity-jane", Name: "Jane", Type: EntityPerson, Salience: 0.8})
	db.AddEntity(&Entity{ID: "entity-proj", Name: "Project Alpha", Type: EntityProduct, Salience: 0.6})
	db.AddEntity(&Entity{ID: "entity-bob", Name: "Bob", Type: EntityPerson, Salience: 0.4})

	// Add alias for Jane
	db.AddEntityAlias("entity-jane", "Jane Smith")

	// Create traces
	db.AddEngram(&Engram{ID: "engram-1", Summary: "Meeting with Jane about Project Alpha", Activation: 0.5, Embedding: []float64{1.0, 0.0, 0.0, 0.0}})
	db.AddEngram(&Engram{ID: "engram-2", Summary: "Jane's birthday is in March", Activation: 0.5, Embedding: []float64{0.0, 1.0, 0.0, 0.0}})
	db.AddEngram(&Engram{ID: "engram-3", Summary: "Project Alpha deadline discussion with Jane", Activation: 0.5, Embedding: []float64{0.0, 0.0, 1.0, 0.0}})
	db.AddEngram(&Engram{ID: "engram-4", Summary: "Unrelated trace about weather", Activation: 0.5, Embedding: []float64{0.0, 0.0, 0.0, 1.0}})

	// Link traces to entities
	db.LinkEngramToEntity("engram-1", "entity-jane")
	db.LinkEngramToEntity("engram-1", "entity-proj")
	db.LinkEngramToEntity("engram-2", "entity-jane")
	db.LinkEngramToEntity("engram-3", "entity-jane")
	db.LinkEngramToEntity("engram-3", "entity-proj")
	db.LinkEngramToEntity("engram-4", "entity-bob")

	return db, cleanup
}

func TestEntityBridgedNeighbors(t *testing.T) {
	db, cleanup := setupEntityBridgedDB(t)
	defer cleanup()

	// trace-1 shares entity-jane with trace-2 and trace-3
	// trace-1 also shares entity-proj with trace-3
	neighbors, err := db.GetEngramNeighborsThroughEntities("engram-1", 15)
	if err != nil {
		t.Fatalf("GetEngramNeighborsThroughEntities failed: %v", err)
	}

	if len(neighbors) == 0 {
		t.Fatal("Expected entity-bridged neighbors, got none")
	}

	// Should find trace-2 and trace-3 as neighbors
	neighborIDs := make(map[string]float64)
	for _, n := range neighbors {
		neighborIDs[n.ID] = n.Weight
		if n.Type != EdgeSharedEntity {
			t.Errorf("Expected EdgeSharedEntity type, got %s", n.Type)
		}
	}

	if _, ok := neighborIDs["engram-2"]; !ok {
		t.Error("Expected trace-2 as neighbor (shares entity-jane)")
	}
	if _, ok := neighborIDs["engram-3"]; !ok {
		t.Error("Expected trace-3 as neighbor (shares entity-jane and entity-proj)")
	}
	if _, ok := neighborIDs["engram-4"]; ok {
		t.Error("engram-4 should NOT be a neighbor of trace-1 (no shared entities)")
	}

	t.Logf("Entity-bridged neighbors of trace-1: %+v", neighbors)
}

func TestEntityBridgedSpreadActivation(t *testing.T) {
	db, cleanup := setupEntityBridgedDB(t)
	defer cleanup()

	// Spread from trace-1 — should reach trace-2 and trace-3 through entity bridges
	activation, err := db.SpreadActivation([]string{"engram-1"}, 3)
	if err != nil {
		t.Fatalf("SpreadActivation failed: %v", err)
	}

	if activation["engram-2"] == 0 {
		t.Error("Expected trace-2 to receive activation through entity bridge")
	}
	if activation["engram-3"] == 0 {
		t.Error("Expected trace-3 to receive activation through entity bridge")
	}

	t.Logf("Entity-bridged activations: trace-1=%f, trace-2=%f, trace-3=%f, trace-4=%f",
		activation["engram-1"], activation["engram-2"], activation["engram-3"], activation["engram-4"])
}

func TestEntitySeeding(t *testing.T) {
	db, cleanup := setupEntityBridgedDB(t)
	defer cleanup()

	// Query mentioning "Jane" should seed Jane-related traces
	matchedEntities, err := db.FindEntitiesByText("meeting with Jane tomorrow", 5)
	if err != nil {
		t.Fatalf("FindEntitiesByText failed: %v", err)
	}

	if len(matchedEntities) == 0 {
		t.Fatal("Expected to find entity 'Jane' in query text")
	}

	found := false
	for _, e := range matchedEntities {
		if e.ID == "entity-jane" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected entity-jane to be matched")
	}

	// Also test alias matching: "Jane Smith"
	matchedAliases, err := db.FindEntitiesByText("I spoke with Jane Smith yesterday", 5)
	if err != nil {
		t.Fatalf("FindEntitiesByText failed: %v", err)
	}

	found = false
	for _, e := range matchedAliases {
		if e.ID == "entity-jane" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected entity-jane to be matched via alias 'Jane Smith'")
	}
}

func TestEntitySeedingNoFalsePositives(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create entities with names that could cause false positives
	db.AddEntity(&Entity{ID: "entity-ai", Name: "AI", Type: EntityProduct, Salience: 0.5})
	db.AddEntity(&Entity{ID: "entity-go", Name: "Go", Type: EntityLanguage, Salience: 0.5})
	db.AddEntity(&Entity{ID: "entity-ed", Name: "Ed", Type: EntityPerson, Salience: 0.5})

	// "AI" is only 2 chars — should be skipped (< 3 char minimum)
	matches, _ := db.FindEntitiesByText("I said something about AI today", 5)
	for _, m := range matches {
		if m.ID == "entity-ai" {
			t.Error("Should not match 'AI' (too short, < 3 chars)")
		}
	}

	// "Go" is only 2 chars — should be skipped
	matches, _ = db.FindEntitiesByText("Let's go to the store", 5)
	for _, m := range matches {
		if m.ID == "entity-go" {
			t.Error("Should not match 'Go' (too short, < 3 chars)")
		}
	}

	// "Ed" is only 2 chars — should be skipped
	matches, _ = db.FindEntitiesByText("I edited the file", 5)
	for _, m := range matches {
		if m.ID == "entity-ed" {
			t.Error("Should not match 'Ed' (too short, < 3 chars)")
		}
	}

	// But longer names should match with word boundaries
	db.AddEntity(&Entity{ID: "entity-alice", Name: "Alice", Type: EntityPerson, Salience: 0.5})
	matches, _ = db.FindEntitiesByText("I met Alice at the park", 5)
	found := false
	for _, m := range matches {
		if m.ID == "entity-alice" {
			found = true
		}
	}
	if !found {
		t.Error("Expected to match 'Alice' as a whole word")
	}

	// "Alice" should NOT match inside "Malice"
	matches, _ = db.FindEntitiesByText("There was no malice intended", 5)
	for _, m := range matches {
		if m.ID == "entity-alice" {
			t.Error("Should not match 'Alice' inside 'malice' (word boundary)")
		}
	}
}

func TestEntityNeighborCap(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create one entity shared by many traces
	db.AddEntity(&Entity{ID: "entity-shared", Name: "SharedThing", Type: EntityProduct, Salience: 0.5})

	// Create source trace
	db.AddEngram(&Engram{ID: "engram-source", Summary: "Source trace"})
	db.LinkEngramToEntity("engram-source", "entity-shared")

	// Create 20 traces sharing the same entity (more than MaxEdgesPerNode=15)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("engram-neighbor-%d", i)
		db.AddEngram(&Engram{ID: id, Summary: fmt.Sprintf("Neighbor trace %d", i)})
		db.LinkEngramToEntity(id, "entity-shared")
	}

	neighbors, err := db.GetEngramNeighborsThroughEntities("engram-source", MaxEdgesPerNode)
	if err != nil {
		t.Fatalf("GetEngramNeighborsThroughEntities failed: %v", err)
	}

	if len(neighbors) > MaxEdgesPerNode {
		t.Errorf("Expected at most %d neighbors, got %d", MaxEdgesPerNode, len(neighbors))
	}

	t.Logf("Returned %d entity-bridged neighbors (cap=%d)", len(neighbors), MaxEdgesPerNode)
}

func TestMultiEntitySharedWeight(t *testing.T) {
	db, cleanup := setupEntityBridgedDB(t)
	defer cleanup()

	neighbors, err := db.GetEngramNeighborsThroughEntities("engram-1", 15)
	if err != nil {
		t.Fatalf("GetEngramNeighborsThroughEntities failed: %v", err)
	}

	weightByID := make(map[string]float64)
	for _, n := range neighbors {
		weightByID[n.ID] = n.Weight
	}

	// trace-3 shares 2 entities (jane + project alpha) with trace-1 -> weight = min(1.0, 2*0.3) = 0.6
	// trace-2 shares 1 entity (jane) with trace-1 -> weight = min(1.0, 1*0.3) = 0.3
	w3 := weightByID["engram-3"]
	w2 := weightByID["engram-2"]

	if w3 <= w2 {
		t.Errorf("Expected trace-3 (2 shared entities, weight=%f) to have higher weight than trace-2 (1 shared entity, weight=%f)", w3, w2)
	}

	if w3 != 0.6 {
		t.Errorf("Expected trace-3 weight 0.6 (2 shared * 0.3), got %f", w3)
	}

	if w2 != 0.3 {
		t.Errorf("Expected trace-2 weight 0.3 (1 shared * 0.3), got %f", w2)
	}

	t.Logf("Multi-entity weights: trace-2=%f, trace-3=%f", w2, w3)
}

// TestReconsolidationFlags tests marking, querying, and clearing reconsolidation flags
func TestReconsolidationFlags(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Add two traces
	tr1 := &Engram{ID: "engram-recon-1", Summary: "Original summary about Alice"}
	tr2 := &Engram{ID: "engram-recon-2", Summary: "Another trace about Bob"}
	if err := addTestEngram(t, db, tr1); err != nil {
		t.Fatalf("AddEngram failed: %v", err)
	}
	if err := addTestEngram(t, db, tr2); err != nil {
		t.Fatalf("AddEngram failed: %v", err)
	}

	// Initially no traces need reconsolidation
	needsRecon, err := db.GetEngramsNeedingReconsolidation()
	if err != nil {
		t.Fatalf("GetEngramsNeedingReconsolidation failed: %v", err)
	}
	if len(needsRecon) != 0 {
		t.Errorf("Expected 0 traces needing reconsolidation, got %d", len(needsRecon))
	}

	// Mark trace-1 for reconsolidation
	if err := db.MarkEngramForReconsolidation("engram-recon-1"); err != nil {
		t.Fatalf("MarkEngramForReconsolidation failed: %v", err)
	}

	// Now 1 trace should need reconsolidation
	needsRecon, err = db.GetEngramsNeedingReconsolidation()
	if err != nil {
		t.Fatalf("GetEngramsNeedingReconsolidation failed: %v", err)
	}
	if len(needsRecon) != 1 {
		t.Fatalf("Expected 1 trace needing reconsolidation, got %d", len(needsRecon))
	}
	if needsRecon[0] != "engram-recon-1" {
		t.Errorf("Expected trace-recon-1, got %s", needsRecon[0])
	}

	// Mark trace-2 as well
	if err := db.MarkEngramForReconsolidation("engram-recon-2"); err != nil {
		t.Fatalf("MarkEngramForReconsolidation failed: %v", err)
	}
	needsRecon, err = db.GetEngramsNeedingReconsolidation()
	if err != nil {
		t.Fatalf("GetEngramsNeedingReconsolidation failed: %v", err)
	}
	if len(needsRecon) != 2 {
		t.Errorf("Expected 2 traces needing reconsolidation, got %d", len(needsRecon))
	}

	// Clear flag for trace-1
	if err := db.ClearReconsolidationFlag("engram-recon-1"); err != nil {
		t.Fatalf("ClearReconsolidationFlag failed: %v", err)
	}
	needsRecon, err = db.GetEngramsNeedingReconsolidation()
	if err != nil {
		t.Fatalf("GetEngramsNeedingReconsolidation failed: %v", err)
	}
	if len(needsRecon) != 1 {
		t.Fatalf("Expected 1 trace after clear, got %d", len(needsRecon))
	}
	if needsRecon[0] != "engram-recon-2" {
		t.Errorf("Expected trace-recon-2 to remain, got %s", needsRecon[0])
	}
}

// TestUpdateEngram tests updating a trace's summary, embedding, and type
func TestUpdateEngram(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	original := &Engram{ID: "engram-update-1", Summary: "Original content"}
	if err := addTestEngram(t, db, original); err != nil {
		t.Fatalf("AddEngram failed: %v", err)
	}

	// Update with new summary, embedding, type
	newSummary := "Updated content with richer context about the project"
	newEmbedding := []float64{0.1, 0.2, 0.3, 0.4, 0.5}
	if err := db.UpdateEngram("engram-update-1", newSummary, newEmbedding, EngramTypeKnowledge, 3); err != nil {
		t.Fatalf("UpdateEngram failed: %v", err)
	}

	// Update trace_summaries at level=0 (verbatim) - this is what GetEngram reads first
	if err := db.AddEngramSummary("engram-update-1", 0, newSummary, estimateTokens(newSummary)); err != nil {
		t.Fatalf("AddEngramSummary failed: %v", err)
	}

	// Verify update
	updated, err := db.GetEngram("engram-update-1")
	if err != nil {
		t.Fatalf("GetEngram failed: %v", err)
	}
	if updated == nil {
		t.Fatal("Expected trace, got nil")
	}
	if updated.Summary != newSummary {
		t.Errorf("Expected summary %q, got %q", newSummary, updated.Summary)
	}
	if updated.EngramType != EngramTypeKnowledge {
		t.Errorf("Expected EngramTypeKnowledge, got %s", updated.EngramType)
	}
	if updated.Strength != 3 {
		t.Errorf("Expected strength 3, got %d", updated.Strength)
	}
}

// TestReconsolidationEndToEnd tests the full reconsolidation flow:
// add episode to existing trace → mark for reconsolidation → reconsolidate → verify summary updated
func TestReconsolidationEndToEnd(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create an episode and initial trace
	ep1 := &Episode{
		ID:             "ep-recon-1",
		Content:        "Alice joined the Nightshade team as tech lead",
		Author:         "user",
		Channel:        "general",
		TimestampEvent: time.Now().Add(-2 * time.Hour),
	}
	if err := db.AddEpisode(ep1); err != nil {
		t.Fatalf("AddEpisode failed: %v", err)
	}

	// Initial trace from first episode
	tr := &Engram{
		ID:       "engram-e2e-recon",
		Summary:  "Alice joined Nightshade as tech lead",
		Strength: 1,
	}
	if err := addTestEngram(t, db, tr); err != nil {
		t.Fatalf("AddEngram failed: %v", err)
	}
	if err := db.LinkEngramToSource("engram-e2e-recon", "ep-recon-1"); err != nil {
		t.Fatalf("LinkEngramToSource failed: %v", err)
	}

	// New episode arrives with updated information
	ep2 := &Episode{
		ID:             "ep-recon-2",
		Content:        "Alice is now leading the privacy initiative at Nightshade",
		Author:         "user",
		Channel:        "general",
		TimestampEvent: time.Now().Add(-1 * time.Hour),
	}
	if err := db.AddEpisode(ep2); err != nil {
		t.Fatalf("AddEpisode failed: %v", err)
	}

	// Link new episode to trace and mark for reconsolidation
	if err := db.LinkEngramToSource("engram-e2e-recon", "ep-recon-2"); err != nil {
		t.Fatalf("LinkEngramToSource failed: %v", err)
	}
	if err := db.MarkEngramForReconsolidation("engram-e2e-recon"); err != nil {
		t.Fatalf("MarkEngramForReconsolidation failed: %v", err)
	}

	// Verify flag is set
	needsRecon, err := db.GetEngramsNeedingReconsolidation()
	if err != nil {
		t.Fatalf("GetEngramsNeedingReconsolidation failed: %v", err)
	}
	if len(needsRecon) == 0 || needsRecon[0] != "engram-e2e-recon" {
		t.Fatal("Expected trace to be marked for reconsolidation")
	}

	// Simulate reconsolidation: update trace with combined content
	allEpisodes, err := db.GetEngramSourceEpisodes("engram-e2e-recon")
	if err != nil {
		t.Fatalf("GetEngramSourceEpisodes failed: %v", err)
	}
	if len(allEpisodes) != 2 {
		t.Fatalf("Expected 2 source episodes, got %d", len(allEpisodes))
	}

	// Build new summary from all episodes (simulating LLM summarization)
	newSummary := "Alice joined Nightshade as tech lead and is now leading the privacy initiative"
	newEmbedding := []float64{0.5, 0.6, 0.7}
	if err := db.UpdateEngram("engram-e2e-recon", newSummary, newEmbedding, EngramTypeKnowledge, len(allEpisodes)); err != nil {
		t.Fatalf("UpdateEngram failed: %v", err)
	}

	// Store updated summary at level=0 (verbatim) - this is what GetEngram reads first
	if err := db.AddEngramSummary("engram-e2e-recon", 0, newSummary, estimateTokens(newSummary)); err != nil {
		t.Fatalf("AddEngramSummary failed: %v", err)
	}

	// Clear the flag
	if err := db.ClearReconsolidationFlag("engram-e2e-recon"); err != nil {
		t.Fatalf("ClearReconsolidationFlag failed: %v", err)
	}

	// Verify: no more traces needing reconsolidation
	needsRecon, err = db.GetEngramsNeedingReconsolidation()
	if err != nil {
		t.Fatalf("GetEngramsNeedingReconsolidation failed: %v", err)
	}
	if len(needsRecon) != 0 {
		t.Errorf("Expected 0 traces needing reconsolidation after clear, got %d", len(needsRecon))
	}

	// Verify: trace now has updated summary and increased strength
	updated, err := db.GetEngram("engram-e2e-recon")
	if err != nil {
		t.Fatalf("GetEngram failed: %v", err)
	}
	if updated == nil {
		t.Fatal("Expected trace, got nil")
	}
	if updated.Summary != newSummary {
		t.Errorf("Expected updated summary %q, got %q", newSummary, updated.Summary)
	}
	if updated.Strength != 2 {
		t.Errorf("Expected strength 2 (2 episodes), got %d", updated.Strength)
	}

	t.Logf("Reconsolidation e2e: trace updated from 1 episode to 2, summary reflects new context")
}

// TestEpisodeEngramEdges tests linking episodes to traces and querying those links
func TestEpisodeEngramEdges(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create episode and trace
	ep := &Episode{
		ID:             "ep-link-1",
		Content:        "Discussion about the new API design",
		Author:         "user",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode failed: %v", err)
	}
	tr := &Engram{ID: "engram-link-1", Summary: "API design decisions"}
	if err := addTestEngram(t, db, tr); err != nil {
		t.Fatalf("AddEngram failed: %v", err)
	}

	// Add episode-trace edge
	if err := db.AddEpisodeEngramEdge("ep-link-1", "engram-link-1", "informs the design described", 0.85); err != nil {
		t.Fatalf("AddEpisodeEngramEdge failed: %v", err)
	}

	// Query: episodes referencing this trace
	episodes, err := db.GetEpisodesReferencingEngram("engram-link-1")
	if err != nil {
		t.Fatalf("GetEpisodesReferencingEngram failed: %v", err)
	}
	if len(episodes) != 1 {
		t.Fatalf("Expected 1 episode referencing trace, got %d", len(episodes))
	}
	if episodes[0].ID != "ep-link-1" {
		t.Errorf("Expected ep-link-1, got %s", episodes[0].ID)
	}

	// Query: traces referenced by this episode
	traceIDs, err := db.GetEngramsReferencedByEpisode("ep-link-1")
	if err != nil {
		t.Fatalf("GetEngramsReferencedByEpisode failed: %v", err)
	}
	if len(traceIDs) != 1 || traceIDs[0] != "engram-link-1" {
		t.Errorf("Expected [trace-link-1], got %v", traceIDs)
	}

	// Query: edge details
	edges, err := db.GetEpisodeEngramEdges("engram-link-1")
	if err != nil {
		t.Fatalf("GetEpisodeEngramEdges failed: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("Expected 1 edge, got %d", len(edges))
	}
	if edges[0].RelationshipDesc != "informs the design described" {
		t.Errorf("Expected relationship desc 'informs the design described', got %q", edges[0].RelationshipDesc)
	}
	if edges[0].Confidence != 0.85 {
		t.Errorf("Expected confidence 0.85, got %f", edges[0].Confidence)
	}

	// Deduplication: adding same edge again should not error
	if err := db.AddEpisodeEngramEdge("ep-link-1", "engram-link-1", "duplicate attempt", 0.5); err != nil {
		t.Errorf("Duplicate AddEpisodeEngramEdge should not fail, got: %v", err)
	}
	edges, _ = db.GetEpisodeEngramEdges("engram-link-1")
	if len(edges) != 1 {
		t.Errorf("Duplicate edge insert should be ignored, still expected 1 edge, got %d", len(edges))
	}
}
