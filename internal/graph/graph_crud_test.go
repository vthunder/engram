package graph

import (
	"testing"
	"time"
)

// ---- Episode CRUD ----

func TestAddAndGetEpisode(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &Episode{
		ID:             "ep-crud-1",
		Content:        "Alice sent a message about the project deadline",
		Author:         "alice",
		AuthorID:       "U123",
		Channel:        "general",
		Source:         "discord",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode failed: %v", err)
	}

	got, err := db.GetEpisode("ep-crud-1")
	if err != nil {
		t.Fatalf("GetEpisode failed: %v", err)
	}
	if got == nil {
		t.Fatal("Expected episode, got nil")
	}
	if got.Content != ep.Content {
		t.Errorf("Content mismatch: want %q, got %q", ep.Content, got.Content)
	}
	if got.Author != "alice" {
		t.Errorf("Author mismatch: want alice, got %s", got.Author)
	}
	if got.Channel != "general" {
		t.Errorf("Channel mismatch: want general, got %s", got.Channel)
	}
	// ShortID should have been auto-generated
	if got.ID[:5] == "" {
		t.Error("Expected short_id to be generated")
	}
}

func TestGetEpisodeNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	got, err := db.GetEpisode("nonexistent")
	if err != nil {
		t.Fatalf("GetEpisode failed: %v", err)
	}
	if got != nil {
		t.Errorf("Expected nil for nonexistent episode, got %+v", got)
	}
}

func TestResolveEpisodeID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &Episode{
		ID:             "ep-shortid-1",
		Content:        "Short ID test episode",
		Source:         "test",
		TimestampEvent: time.Now(),
	}
	if err := db.AddEpisode(ep); err != nil {
		t.Fatalf("AddEpisode failed: %v", err)
	}

	// Retrieve by short ID
	fullID, err := db.ResolveEpisodeID(ep.ID[:5])
	if err != nil {
		t.Fatalf("ResolveEpisodeID failed: %v", err)
	}
	if fullID != ep.ID {
		t.Errorf("ID mismatch: want %s, got %s", ep.ID, fullID)
	}
}

func TestCountEpisodes(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	count, err := db.CountEpisodes()
	if err != nil {
		t.Fatalf("CountEpisodes failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 episodes, got %d", count)
	}

	db.AddEpisode(&Episode{ID: "ep-count-1", Content: "A", Source: "test", TimestampEvent: time.Now()})
	db.AddEpisode(&Episode{ID: "ep-count-2", Content: "B", Source: "test", TimestampEvent: time.Now()})

	count, err = db.CountEpisodes()
	if err != nil {
		t.Fatalf("CountEpisodes failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 episodes, got %d", count)
	}
}

func TestGetEpisodes(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep1 := &Episode{ID: "ep-batch-1", Content: "First", Source: "test", TimestampEvent: time.Now()}
	ep2 := &Episode{ID: "ep-batch-2", Content: "Second", Source: "test", TimestampEvent: time.Now()}
	ep3 := &Episode{ID: "ep-batch-3", Content: "Third", Source: "test", TimestampEvent: time.Now()}
	db.AddEpisode(ep1)
	db.AddEpisode(ep2)
	db.AddEpisode(ep3)

	// Retrieve 2 of 3
	got, err := db.GetEpisodes([]string{"ep-batch-1", "ep-batch-3"})
	if err != nil {
		t.Fatalf("GetEpisodes failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Expected 2 episodes, got %d", len(got))
	}

	// Empty input returns nil without error
	got, err = db.GetEpisodes(nil)
	if err != nil {
		t.Fatalf("GetEpisodes(nil) failed: %v", err)
	}
	if got != nil {
		t.Errorf("Expected nil for empty input, got %v", got)
	}
}

func TestGetAllEpisodes(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		db.AddEpisode(&Episode{
			ID:             "ep-all-" + string(rune('a'+i)),
			Content:        "Content",
			Source:         "test",
			TimestampEvent: time.Now().Add(time.Duration(i) * time.Second),
		})
	}

	got, err := db.GetAllEpisodes(10)
	if err != nil {
		t.Fatalf("GetAllEpisodes failed: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("Expected 5 episodes, got %d", len(got))
	}
	// Verify ordered by timestamp DESC
	if len(got) >= 2 && got[0].TimestampEvent.Before(got[1].TimestampEvent) {
		t.Error("Expected episodes ordered by timestamp DESC")
	}
}

func TestGetRecentEpisodes(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	db.AddEpisode(&Episode{ID: "ep-rec-1", Content: "General 1", Source: "test", Channel: "general", TimestampEvent: now.Add(-2 * time.Hour)})
	db.AddEpisode(&Episode{ID: "ep-rec-2", Content: "General 2", Source: "test", Channel: "general", TimestampEvent: now.Add(-1 * time.Hour)})
	db.AddEpisode(&Episode{ID: "ep-rec-3", Content: "Dev 1", Source: "test", Channel: "dev", TimestampEvent: now})

	// Channel filter
	general, err := db.GetRecentEpisodes("general", 10)
	if err != nil {
		t.Fatalf("GetRecentEpisodes failed: %v", err)
	}
	if len(general) != 2 {
		t.Errorf("Expected 2 general episodes, got %d", len(general))
	}

	// No filter: all channels
	all, err := db.GetRecentEpisodes("", 10)
	if err != nil {
		t.Fatalf("GetRecentEpisodes('') failed: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("Expected 3 episodes with no channel filter, got %d", len(all))
	}

	// Limit
	limited, err := db.GetRecentEpisodes("", 2)
	if err != nil {
		t.Fatalf("GetRecentEpisodes with limit failed: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("Expected 2 episodes with limit=2, got %d", len(limited))
	}
}

func TestGetEpisodeReplies(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	parent := &Episode{ID: "ep-parent", Content: "Parent message", Source: "test", TimestampEvent: now}
	reply1 := &Episode{ID: "ep-reply-1", Content: "Reply 1", Source: "test", TimestampEvent: now.Add(1 * time.Minute), ReplyTo: "ep-parent"}
	reply2 := &Episode{ID: "ep-reply-2", Content: "Reply 2", Source: "test", TimestampEvent: now.Add(2 * time.Minute), ReplyTo: "ep-parent"}
	unrelated := &Episode{ID: "ep-unrelated", Content: "Other", Source: "test", TimestampEvent: now.Add(3 * time.Minute)}

	db.AddEpisode(parent)
	db.AddEpisode(reply1)
	db.AddEpisode(reply2)
	db.AddEpisode(unrelated)

	replies, err := db.GetEpisodeReplies("ep-parent")
	if err != nil {
		t.Fatalf("GetEpisodeReplies failed: %v", err)
	}
	if len(replies) != 2 {
		t.Fatalf("Expected 2 replies, got %d", len(replies))
	}
	// Verify reply content
	for _, r := range replies {
		if r.ID != "ep-reply-1" && r.ID != "ep-reply-2" {
			t.Errorf("Unexpected reply ID: %s", r.ID)
		}
	}
}

func TestAddEpisodeEdgeAndGetNeighbors(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep1 := &Episode{ID: "ep-edge-1", Content: "First", Source: "test", TimestampEvent: time.Now()}
	ep2 := &Episode{ID: "ep-edge-2", Content: "Second", Source: "test", TimestampEvent: time.Now()}
	db.AddEpisode(ep1)
	db.AddEpisode(ep2)

	err := db.AddEpisodeEdge("ep-edge-1", "ep-edge-2", EdgeRelatedTo, 0.7)
	if err != nil {
		t.Fatalf("AddEpisodeEdge failed: %v", err)
	}

	neighbors, err := db.GetEpisodeNeighbors("ep-edge-1")
	if err != nil {
		t.Fatalf("GetEpisodeNeighbors failed: %v", err)
	}
	if len(neighbors) != 1 {
		t.Fatalf("Expected 1 neighbor, got %d", len(neighbors))
	}
	if neighbors[0].ID != "ep-edge-2" {
		t.Errorf("Expected neighbor ep-edge-2, got %s", neighbors[0].ID)
	}
	if neighbors[0].Weight != 0.7 {
		t.Errorf("Expected weight 0.7, got %f", neighbors[0].Weight)
	}

	// Edge should be bidirectional for GetEpisodeNeighbors
	neighbors2, _ := db.GetEpisodeNeighbors("ep-edge-2")
	if len(neighbors2) != 1 || neighbors2[0].ID != "ep-edge-1" {
		t.Error("Expected bidirectional neighbor lookup")
	}
}

func TestGetUnconsolidatedEpisodes(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Add episodes
	db.AddEpisode(&Episode{ID: "ep-u1", Content: "Unconsolidated 1", Source: "test", TimestampEvent: time.Now()})
	db.AddEpisode(&Episode{ID: "ep-u2", Content: "Unconsolidated 2", Source: "test", TimestampEvent: time.Now()})
	db.AddEpisode(&Episode{ID: "ep-c1", Content: "Consolidated", Source: "test", TimestampEvent: time.Now()})

	// Add a trace and link ep-c1 to it
	db.AddEngram(&Engram{ID: "tr-for-ep", Summary: "Test"})
	db.LinkEngramToSource("tr-for-ep", "ep-c1")

	count, err := db.GetUnconsolidatedEpisodeCount()
	if err != nil {
		t.Fatalf("GetUnconsolidatedEpisodeCount failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 unconsolidated episodes, got %d", count)
	}

	unconsolidated, err := db.GetUnconsolidatedEpisodes(10)
	if err != nil {
		t.Fatalf("GetUnconsolidatedEpisodes failed: %v", err)
	}
	if len(unconsolidated) != 2 {
		t.Fatalf("Expected 2 unconsolidated episodes, got %d", len(unconsolidated))
	}
	for _, ep := range unconsolidated {
		if ep.ID == "ep-c1" {
			t.Error("Consolidated episode ep-c1 should not appear in unconsolidated list")
		}
	}
}

func TestGetUnconsolidatedEpisodeIDsForChannel(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	db.AddEpisode(&Episode{ID: "ep-ch1", Content: "Chan A", Source: "test", Channel: "chan-a", TimestampEvent: time.Now()})
	db.AddEpisode(&Episode{ID: "ep-ch2", Content: "Chan B", Source: "test", Channel: "chan-b", TimestampEvent: time.Now()})
	db.AddEpisode(&Episode{ID: "ep-ch3", Content: "Chan A consolidated", Source: "test", Channel: "chan-a", TimestampEvent: time.Now()})

	// Consolidate ep-ch3
	db.AddEngram(&Engram{ID: "tr-ch", Summary: "Test"})
	db.LinkEngramToSource("tr-ch", "ep-ch3")

	ids, err := db.GetUnconsolidatedEpisodeIDsForChannel("chan-a")
	if err != nil {
		t.Fatalf("GetUnconsolidatedEpisodeIDsForChannel failed: %v", err)
	}
	if !ids["ep-ch1"] {
		t.Error("Expected ep-ch1 in unconsolidated set")
	}
	if ids["ep-ch3"] {
		t.Error("ep-ch3 is consolidated, should not be in set")
	}
	if ids["ep-ch2"] {
		t.Error("ep-ch2 is wrong channel, should not appear")
	}
}

func TestUpdateEpisodeAuthorization(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &Episode{ID: "ep-auth-1", Content: "Auth test", Source: "test", TimestampEvent: time.Now()}
	db.AddEpisode(ep)

	// Initially authorization_checked=false
	got, _ := db.GetEpisode("ep-auth-1")
	if got.AuthorizationChecked {
		t.Error("Expected authorization_checked=false initially")
	}

	// Set authorization
	if err := db.UpdateEpisodeAuthorization("ep-auth-1", true); err != nil {
		t.Fatalf("UpdateEpisodeAuthorization failed: %v", err)
	}

	got, _ = db.GetEpisode("ep-auth-1")
	if !got.AuthorizationChecked {
		t.Error("Expected authorization_checked=true after update")
	}
	if !got.HasAuthorization {
		t.Error("Expected has_authorization=true")
	}
}

func TestGetEpisodeEntities(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &Episode{ID: "ep-ent-1", Content: "Alice and Bob", Source: "test", TimestampEvent: time.Now()}
	db.AddEpisode(ep)
	db.AddEntity(&Entity{ID: "entity-alice2", Name: "Alice2", Type: EntityPerson, Salience: 0.9})
	db.AddEntity(&Entity{ID: "entity-bob2", Name: "Bob2", Type: EntityPerson, Salience: 0.7})

	// Link entities to episode
	db.db.Exec("INSERT INTO episode_mentions (episode_id, entity_id) VALUES (?, ?)", "ep-ent-1", "entity-alice2")
	db.db.Exec("INSERT INTO episode_mentions (episode_id, entity_id) VALUES (?, ?)", "ep-ent-1", "entity-bob2")

	ids, err := db.GetEpisodeEntities("ep-ent-1")
	if err != nil {
		t.Fatalf("GetEpisodeEntities failed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("Expected 2 entity IDs, got %d", len(ids))
	}
}

// ---- Trace CRUD ----

func TestResolveEngramID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := &Engram{ID: "engram-shortid-1", Summary: "Short ID test trace"}
	if err := addTestEngram(t, db, tr); err != nil {
		t.Fatalf("AddEngram failed: %v", err)
	}

	got, err := db.ResolveEngramID(tr.ID[:5])
	if err != nil {
		t.Fatalf("ResolveEngramID failed: %v", err)
	}
	if got == "" {
		t.Fatal("Expected engram ID, got empty string")
	}
	if got != tr.ID {
		t.Errorf("ID mismatch: want %s, got %s", tr.ID, got)
	}
}

func TestCountEngrams(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	count, err := db.CountEngrams()
	if err != nil {
		t.Fatalf("CountEngrams failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 traces, got %d", count)
	}

	db.AddEngram(&Engram{ID: "tr-cnt-1", Summary: "One"})
	db.AddEngram(&Engram{ID: "tr-cnt-2", Summary: "Two"})

	count, err = db.CountEngrams()
	if err != nil {
		t.Fatalf("CountEngrams failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 traces, got %d", count)
	}
}

func TestDeleteEngram(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	if err := addTestEngram(t, db, &Engram{ID: "tr-del-1", Summary: "To be deleted"}); err != nil {
		t.Fatalf("AddEngram failed: %v", err)
	}

	// Verify it exists
	got, _ := db.GetEngram("tr-del-1")
	if got == nil {
		t.Fatal("Expected trace before delete")
	}

	// Delete it
	if err := db.DeleteEngram("tr-del-1"); err != nil {
		t.Fatalf("DeleteEngram failed: %v", err)
	}

	// Should be gone
	got, _ = db.GetEngram("tr-del-1")
	if got != nil {
		t.Error("Expected nil after delete")
	}

	// Deleting non-existent should error
	if err := db.DeleteEngram("nonexistent"); err == nil {
		t.Error("Expected error deleting nonexistent trace")
	}
}

func TestDeleteEngramCascades(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create trace, link to entity and source episode
	ep := &Episode{ID: "ep-cascade", Content: "Source", Source: "test", TimestampEvent: time.Now()}
	db.AddEpisode(ep)
	db.AddEntity(&Entity{ID: "entity-cascade", Name: "Cascade", Type: EntityPerson, Salience: 0.5})
	addTestEngram(t, db, &Engram{ID: "tr-cascade", Summary: "Will cascade"})
	db.LinkEngramToSource("tr-cascade", "ep-cascade")
	db.LinkEngramToEntity("tr-cascade", "entity-cascade")

	// Delete the trace
	db.DeleteEngram("tr-cascade")

	// Source and entity links should be gone (cascade)
	sources, _ := db.GetEngramSources("tr-cascade")
	if len(sources) != 0 {
		t.Errorf("Expected engram_episodes to cascade delete, got %d sources", len(sources))
	}
	entities, _ := db.GetEngramEntities("tr-cascade")
	if len(entities) != 0 {
		t.Errorf("Expected engram_entities to cascade delete, got %d entities", len(entities))
	}
}

func TestGetAllEngrams(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	addTestEngram(t, db, &Engram{ID: "tr-all-1", Summary: "First"})
	addTestEngram(t, db, &Engram{ID: "tr-all-2", Summary: "Second"})
	addTestEngram(t, db, &Engram{ID: "tr-all-3", Summary: "Third"})

	traces, err := db.GetAllEngrams()
	if err != nil {
		t.Fatalf("GetAllEngrams failed: %v", err)
	}
	if len(traces) != 3 {
		t.Errorf("Expected 3 traces, got %d", len(traces))
	}
}

func TestGetActivatedEngrams(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	addTestEngram(t, db, &Engram{ID: "tr-act-high", Summary: "High", Activation: 0.9})
	addTestEngram(t, db, &Engram{ID: "tr-act-mid", Summary: "Mid", Activation: 0.6})
	addTestEngram(t, db, &Engram{ID: "tr-act-low", Summary: "Low", Activation: 0.2})

	// Threshold 0.5 should return 2 traces
	traces, err := db.GetActivatedEngrams(0.5, 10)
	if err != nil {
		t.Fatalf("GetActivatedEngrams failed: %v", err)
	}
	if len(traces) != 2 {
		t.Fatalf("Expected 2 traces above 0.5, got %d", len(traces))
	}
	// Should be ordered by activation DESC
	if traces[0].Activation < traces[1].Activation {
		t.Error("Expected traces ordered by activation DESC")
	}

	// Limit should be respected
	limited, _ := db.GetActivatedEngrams(0.0, 1)
	if len(limited) != 1 {
		t.Errorf("Expected 1 trace with limit=1, got %d", len(limited))
	}
}

func TestGetActivatedEngramsWithLevel(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := &Engram{ID: "tr-lvl-1", Summary: "Test summary", Activation: 0.8}
	addTestEngram(t, db, tr)
	// Add a level-8 summary
	db.AddEngramSummary("tr-lvl-1", 8, "Short summary", 2)

	traces, err := db.GetActivatedEngramsWithLevel(0.5, 10, 8)
	if err != nil {
		t.Fatalf("GetActivatedEngramsWithLevel failed: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("Expected 1 trace, got %d", len(traces))
	}
	if traces[0].Summary != "Short summary" {
		t.Errorf("Expected level-8 summary, got %q", traces[0].Summary)
	}
}

func TestGetEngramsBatch(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	addTestEngram(t, db, &Engram{ID: "tr-batch-1", Summary: "Batch One"})
	addTestEngram(t, db, &Engram{ID: "tr-batch-2", Summary: "Batch Two"})
	addTestEngram(t, db, &Engram{ID: "tr-batch-3", Summary: "Batch Three"})

	result, err := db.GetEngramsBatch([]string{"tr-batch-1", "tr-batch-3"})
	if err != nil {
		t.Fatalf("GetEngramsBatch failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Expected 2 traces in map, got %d", len(result))
	}
	if _, ok := result["tr-batch-1"]; !ok {
		t.Error("Expected tr-batch-1 in result")
	}
	if _, ok := result["tr-batch-3"]; !ok {
		t.Error("Expected tr-batch-3 in result")
	}
	if _, ok := result["tr-batch-2"]; ok {
		t.Error("tr-batch-2 was not requested, should not be in result")
	}

	// Empty input
	empty, err := db.GetEngramsBatch(nil)
	if err != nil {
		t.Fatalf("GetEngramsBatch(nil) failed: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Expected empty map, got %d entries", len(empty))
	}
}

func TestGetEngramsBatchAtLevel(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	addTestEngram(t, db, &Engram{ID: "tr-blvl-1", Summary: "Full summary here"})
	db.AddEngramSummary("tr-blvl-1", 4, "4-word summary", 4)

	result, err := db.GetEngramsBatchAtLevel([]string{"tr-blvl-1"}, 4)
	if err != nil {
		t.Fatalf("GetEngramsBatchAtLevel failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("Expected 1 trace, got %d", len(result))
	}
	if result["tr-blvl-1"].Summary != "4-word summary" {
		t.Errorf("Expected level-4 summary, got %q", result["tr-blvl-1"].Summary)
	}
}

func TestGetEngramSources(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep1 := &Episode{ID: "ep-src-1", Content: "Source 1", Source: "test", TimestampEvent: time.Now()}
	ep2 := &Episode{ID: "ep-src-2", Content: "Source 2", Source: "test", TimestampEvent: time.Now()}
	db.AddEpisode(ep1)
	db.AddEpisode(ep2)
	addTestEngram(t, db, &Engram{ID: "tr-src-1", Summary: "From episodes"})
	db.LinkEngramToSource("tr-src-1", "ep-src-1")
	db.LinkEngramToSource("tr-src-1", "ep-src-2")

	sources, err := db.GetEngramSources("tr-src-1")
	if err != nil {
		t.Fatalf("GetEngramSources failed: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("Expected 2 source episode IDs, got %d", len(sources))
	}
}

func TestGetEpisodeEngrams(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	ep := &Episode{ID: "ep-et-1", Content: "Shared episode", Source: "test", TimestampEvent: time.Now()}
	db.AddEpisode(ep)
	addTestEngram(t, db, &Engram{ID: "tr-et-1", Summary: "Trace 1"})
	addTestEngram(t, db, &Engram{ID: "tr-et-2", Summary: "Trace 2"})
	db.LinkEngramToSource("tr-et-1", "ep-et-1")
	db.LinkEngramToSource("tr-et-2", "ep-et-1")

	traceIDs, err := db.GetEpisodeEngrams("ep-et-1")
	if err != nil {
		t.Fatalf("GetEpisodeEngrams failed: %v", err)
	}
	if len(traceIDs) != 2 {
		t.Fatalf("Expected 2 trace IDs, got %d", len(traceIDs))
	}
}

func TestGetEngramEntities(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	addTestEngram(t, db, &Engram{ID: "tr-te-1", Summary: "Test"})
	db.AddEntity(&Entity{ID: "entity-te-1", Name: "TestEntity1", Type: EntityProduct, Salience: 0.5})
	db.AddEntity(&Entity{ID: "entity-te-2", Name: "TestEntity2", Type: EntityProduct, Salience: 0.5})
	db.LinkEngramToEntity("tr-te-1", "entity-te-1")
	db.LinkEngramToEntity("tr-te-1", "entity-te-2")

	entities, err := db.GetEngramEntities("tr-te-1")
	if err != nil {
		t.Fatalf("GetEngramEntities failed: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("Expected 2 entity IDs, got %d", len(entities))
	}
}

// ---- Trace bulk operations ----

func TestBoostEngramAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	addTestEngram(t, db, &Engram{ID: "tr-boost-1", Summary: "Test", Activation: 0.5})
	addTestEngram(t, db, &Engram{ID: "tr-boost-2", Summary: "Test", Activation: 0.8})

	if err := db.BoostEngramAccess([]string{"tr-boost-1"}, 0.2); err != nil {
		t.Fatalf("BoostEngramAccess failed: %v", err)
	}

	got, _ := db.GetEngram("tr-boost-1")
	if got.Activation < 0.69 || got.Activation > 0.71 {
		t.Errorf("Expected activation ~0.7 after boost, got %f", got.Activation)
	}

	// Trace 2 should be unchanged
	got2, _ := db.GetEngram("tr-boost-2")
	if got2.Activation != 0.8 {
		t.Errorf("Expected tr-boost-2 activation 0.8 (unchanged), got %f", got2.Activation)
	}

	// Activation should not exceed 1.0
	addTestEngram(t, db, &Engram{ID: "tr-boost-max", Summary: "Near max", Activation: 0.95})
	db.BoostEngramAccess([]string{"tr-boost-max"}, 0.5)
	got3, _ := db.GetEngram("tr-boost-max")
	if got3.Activation > 1.0 {
		t.Errorf("Activation should not exceed 1.0, got %f", got3.Activation)
	}
}

func TestDecayActivationByAge(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Add a knowledge trace with old last_accessed
	addTestEngram(t, db, &Engram{ID: "tr-decay-old", Summary: "Old", Activation: 0.9})
	// Set last_accessed to 100 hours ago
	db.TestSetEngramTimestamp("tr-decay-old", time.Now().Add(-100*time.Hour))

	// Add a recently accessed trace
	addTestEngram(t, db, &Engram{ID: "tr-decay-new", Summary: "New", Activation: 0.9})
	// last_accessed is now (set by AddEngram)

	// Decay with lambda=0.005 (gentle)
	count, err := db.DecayActivationByAge(0.005, 0.01)
	if err != nil {
		t.Fatalf("DecayActivationByAge failed: %v", err)
	}
	if count == 0 {
		t.Error("Expected at least 1 trace to be updated by decay")
	}

	// Old trace should have decayed significantly
	old, _ := db.GetEngram("tr-decay-old")
	if old.Activation >= 0.9 {
		t.Errorf("Expected old trace to decay below 0.9, got %f", old.Activation)
	}

	// Operational traces decay 3x faster
	addTestEngram(t, db, &Engram{ID: "tr-decay-op", Summary: "Operational", Activation: 0.9, EngramType: EngramTypeOperational})
	db.TestSetEngramTimestamp("tr-decay-op", time.Now().Add(-24*time.Hour))
	addTestEngram(t, db, &Engram{ID: "tr-decay-kn", Summary: "Knowledge", Activation: 0.9, EngramType: EngramTypeKnowledge})
	db.TestSetEngramTimestamp("tr-decay-kn", time.Now().Add(-24*time.Hour))

	db.DecayActivationByAge(0.005, 0.01)

	op, _ := db.GetEngram("tr-decay-op")
	kn, _ := db.GetEngram("tr-decay-kn")
	if op.Activation >= kn.Activation {
		t.Errorf("Operational trace (%.3f) should decay faster than knowledge trace (%.3f)", op.Activation, kn.Activation)
	}
}

// ---- Batch neighbor operations ----

func TestGetEngramNeighborsBatch(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create a small network: A->B, B->C
	db.AddEngram(&Engram{ID: "tr-nb-A", Summary: "A"})
	db.AddEngram(&Engram{ID: "tr-nb-B", Summary: "B"})
	db.AddEngram(&Engram{ID: "tr-nb-C", Summary: "C"})
	db.AddEngram(&Engram{ID: "tr-nb-D", Summary: "D"}) // isolated

	db.AddEngramRelation("tr-nb-A", "tr-nb-B", EdgeRelatedTo, 0.8)
	db.AddEngramRelation("tr-nb-B", "tr-nb-C", EdgeRelatedTo, 0.6)

	result, err := db.GetEngramNeighborsBatch([]string{"tr-nb-A", "tr-nb-B", "tr-nb-D"})
	if err != nil {
		t.Fatalf("GetEngramNeighborsBatch failed: %v", err)
	}

	// All requested IDs should be present in the map
	for _, id := range []string{"tr-nb-A", "tr-nb-B", "tr-nb-D"} {
		if _, ok := result[id]; !ok {
			t.Errorf("Expected key %s in result", id)
		}
	}

	// A's neighbors should include B
	aNeighbors := result["tr-nb-A"]
	found := false
	for _, n := range aNeighbors {
		if n.ID == "tr-nb-B" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected tr-nb-B as neighbor of tr-nb-A")
	}

	// B's neighbors should include both A and C
	bNeighbors := result["tr-nb-B"]
	bNeighborIDs := make(map[string]bool)
	for _, n := range bNeighbors {
		bNeighborIDs[n.ID] = true
	}
	if !bNeighborIDs["tr-nb-A"] {
		t.Error("Expected tr-nb-A as neighbor of tr-nb-B")
	}
	if !bNeighborIDs["tr-nb-C"] {
		t.Error("Expected tr-nb-C as neighbor of tr-nb-B")
	}

	// D has no neighbors
	if len(result["tr-nb-D"]) != 0 {
		t.Errorf("Expected no neighbors for isolated node D, got %d", len(result["tr-nb-D"]))
	}

	// Empty input returns empty map
	empty, err := db.GetEngramNeighborsBatch(nil)
	if err != nil {
		t.Fatalf("GetEngramNeighborsBatch(nil) failed: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Expected empty map for nil input, got %d entries", len(empty))
	}
}

func TestGetConsolidatedEpisodesWithEmbeddings(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	ep1 := &Episode{ID: "ep-cemb-1", Content: "Consolidated A", Source: "test", TimestampEvent: now, Embedding: []float64{0.1, 0.2}}
	ep2 := &Episode{ID: "ep-cemb-2", Content: "Consolidated B", Source: "test", TimestampEvent: now.Add(time.Second), Embedding: []float64{0.3, 0.4}}
	ep3 := &Episode{ID: "ep-cemb-3", Content: "Unconsolidated", Source: "test", TimestampEvent: now.Add(2 * time.Second), Embedding: []float64{0.5, 0.6}}
	db.AddEpisode(ep1)
	db.AddEpisode(ep2)
	db.AddEpisode(ep3)

	// Consolidate ep1 and ep2; ep3 stays unconsolidated
	db.AddEngram(&Engram{ID: "tr-cemb", Summary: "Test"})
	db.LinkEngramToSource("tr-cemb", "ep-cemb-1")
	db.LinkEngramToSource("tr-cemb", "ep-cemb-2")

	got, err := db.GetConsolidatedEpisodesWithEmbeddings(0, 100)
	if err != nil {
		t.Fatalf("GetConsolidatedEpisodesWithEmbeddings failed: %v", err)
	}
	// ep1 and ep2 are consolidated with embeddings; ep3 is unconsolidated
	if len(got) != 2 {
		t.Fatalf("Expected 2 consolidated episodes with embeddings, got %d", len(got))
	}
	for _, ep := range got {
		if ep.ID == "ep-cemb-3" {
			t.Error("Unconsolidated ep-cemb-3 should not appear")
		}
	}

	// Pagination: offset=1 should return just 1
	paged, err := db.GetConsolidatedEpisodesWithEmbeddings(1, 100)
	if err != nil {
		t.Fatalf("GetConsolidatedEpisodesWithEmbeddings with offset failed: %v", err)
	}
	if len(paged) != 1 {
		t.Errorf("Expected 1 episode with offset=1, got %d", len(paged))
	}
}
