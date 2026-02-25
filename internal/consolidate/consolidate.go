// Package consolidate handles memory consolidation - grouping related episodes
// into consolidated traces with LLM-generated summaries.
package consolidate

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vthunder/engram/internal/filter"
	"github.com/vthunder/engram/internal/graph"
)

// LLMClient provides embedding and text generation capabilities
type LLMClient interface {
	Embed(text string) ([]float64, error)
	Generate(prompt string) (string, error)
}

// Consolidator handles memory consolidation
type Consolidator struct {
	mu    sync.Mutex
	graph  *graph.DB
	llm    LLMClient

	// Configuration
	MinGroupSize int // Minimum episodes to form a group (default 1)
	MaxGroupSize int // Maximum episodes per group (default 10)

	// Identity fields for role-aware consolidation prompts.
	// If BotName is empty, falls back to neutral name-only framing.
	BotName     string
	BotAuthorID string
	OwnerIDs    []string

	// Claude inference for relationship linking
	claude *ClaudeInference

	// Episode-episode sliding window configuration
	episodeBatchSize    int     // Batch size for sliding window (default 20)
	episodeBatchOverlap float64 // Overlap ratio for sliding window (default 0.5 = 50%)

	// Incremental mode: only infer edges for windows with new episodes
	IncrementalMode bool

	// NewEngramHook is called asynchronously after each new L1 engram is created.
	// Used for forward schema matching. Set from main.go to avoid import cycles.
	NewEngramHook func(engram *graph.Engram)
}

// NewConsolidator creates a new consolidator
func NewConsolidator(g *graph.DB, llm LLMClient, claude *ClaudeInference) *Consolidator {
	return &Consolidator{
		graph:               g,
		llm:                 llm,
		claude:              claude,
		MinGroupSize:        1,
		MaxGroupSize:        10,
		episodeBatchSize:    20,
		episodeBatchOverlap: 0.5,
	}
}


// ShouldRun returns true if at least one channel meets the consolidation trigger conditions:
//   - at least minEpisodes unconsolidated episodes exist in the channel, AND
//   - either the channel has been idle for idleTime, OR the channel has maxBuffer+ unconsolidated episodes
func (c *Consolidator) ShouldRun(minEpisodes int, idleTime time.Duration, maxBuffer int) (bool, error) {
	stats, err := c.graph.GetChannelConsolidationStats(minEpisodes)
	if err != nil {
		return false, fmt.Errorf("consolidation eligibility check: %w", err)
	}
	for _, s := range stats {
		if s.UnconsolidatedCount >= maxBuffer {
			return true, nil
		}
		if time.Since(s.LastEpisodeTime) >= idleTime {
			return true, nil
		}
	}
	return false, nil
}

// episodeGroup represents a group of related episodes to consolidate
type episodeGroup struct {
	episodes     []*graph.Episode
	entityIDs    map[string]bool // union of all entity IDs
	priorContext string          // optional preceding context shown to LLM (prior sub-group or prior engram summary)
}

// Run consolidates unconsolidated episodes into traces.
// Returns the number of traces created.
//
// Architecture:
// Phase 1: Claude infers episode-episode edges (sliding window per channel)
// Phase 2: Graph clustering using those edges → episode groups
// Phase 3: Create traces from clustered groups
func (c *Consolidator) Run() (int, error) {
	if !c.mu.TryLock() {
		log.Printf("[consolidate] Run skipped: consolidation already in progress")
		return 0, nil
	}
	defer c.mu.Unlock()

	totalCreated := 0
	var prevEpisodeIDs map[string]bool

	// Process episodes in batches until all are consolidated
	for {
		// Get episodes that haven't been consolidated yet
		episodes, err := c.graph.GetUnconsolidatedEpisodes(500)
		if err != nil {
			return totalCreated, fmt.Errorf("failed to get unconsolidated episodes: %w", err)
		}

		if len(episodes) == 0 {
			return totalCreated, nil
		}

		// No-progress guard: if the same set of episode IDs is returned as the previous
		// iteration, clustering made no progress and we must stop to avoid an infinite loop.
		currentEpisodeIDs := make(map[string]bool, len(episodes))
		for _, ep := range episodes {
			currentEpisodeIDs[ep.ID] = true
		}
		if prevEpisodeIDs != nil && mapsEqual(currentEpisodeIDs, prevEpisodeIDs) {
			log.Printf("[consolidate] No progress: same %d episodes returned, stopping", len(episodes))
			return totalCreated, nil
		}
		prevEpisodeIDs = currentEpisodeIDs

		log.Printf("[consolidate] Batch: %d episodes to process (total engrams so far: %d)", len(episodes), totalCreated)

		ctx := context.Background()

		// Phase 0: Detect near-duplicate episodes using C16 summary similarity
		duplicateEdges := c.detectDuplicateEpisodes(episodes)

		// Phase 1: Load existing episode-episode edges or infer new ones
		existingEdges := c.loadExistingEdges(episodes)
		var episodeEdges []EpisodeEdge

		if c.IncrementalMode && len(existingEdges) > 0 {
			// Incremental mode: skip inference for fully-consolidated batches
			// All episodes in this batch already have edges, so use existing ones
			episodeEdges = existingEdges
			log.Printf("[consolidate] Using %d existing edges (incremental mode)", len(existingEdges))
		} else if len(existingEdges) > 0 {
			// Non-incremental: use existing edges if available
			log.Printf("[consolidate] Using %d existing episode edges (skip inference with --wipe-edges to re-detect)", len(existingEdges))
			episodeEdges = existingEdges
		} else {
			// No existing edges - infer new ones
			var err error
			episodeEdges, err = c.inferEpisodeEpisodeLinks(ctx, episodes)
			if err != nil {
				log.Printf("[consolidate] Failed to infer episode edges: %v", err)
				// Continue anyway - we can still try clustering with no edges
			}
		}

		// Merge duplicate edges with inferred edges
		episodeEdges = append(duplicateEdges, episodeEdges...)

		// Deduplicate edges (same from/to/relationship)
		episodeEdges = deduplicateEdges(episodeEdges)

		log.Printf("[consolidate] Phase 1 done: %d edges (inferring clustering)", len(episodeEdges))

		// Print edge summaries in verbose mode
		if c.claude != nil && c.claude.verbose {
			c.printEdgeSummaries(episodes, episodeEdges)
		}

		// Store edges in database (only if both episodes exist)
		episodeIDs := make(map[string]bool)
		for _, ep := range episodes {
			episodeIDs[ep.ID] = true
		}
		for _, edge := range episodeEdges {
			// Skip edges where either endpoint doesn't exist in this batch
			if !episodeIDs[edge.FromID] || !episodeIDs[edge.ToID] {
				continue
			}
			if err := c.graph.AddEpisodeEpisodeEdge(edge.FromID, edge.ToID, "RELATED_TO", edge.Relationship, edge.Confidence); err != nil {
				log.Printf("[consolidate] Failed to add episode edge %s -> %s: %v", edge.FromID, edge.ToID, err)
			}
		}

		// Phase 2: Graph clustering using Claude-inferred edges
		// Returns: new groups (to be consolidated) and existing engrams with new episodes
		newGroups, existingEngramsWithNewEpisodes := c.clusterEpisodesByEdges(episodes, episodeEdges)

		// Phase 3a: Add new episodes to labile engrams; route non-labile to new groups.
		// Non-labile groups carry the prior engram's summary as context so the new engram
		// can reference what came before.
		for engramID, newEpisodes := range existingEngramsWithNewEpisodes {
			engram, err := c.graph.GetEngram(engramID)
			if err != nil || engram == nil || !engram.IsLabile() {
				// Engram past its labile window — new episodes form their own group.
				newGroup := &episodeGroup{
					episodes:  newEpisodes,
					entityIDs: make(map[string]bool),
				}
				if engram != nil && engram.Summary != "" {
					newGroup.priorContext = "Previous memory: " + engram.Summary
				}
				for _, ep := range newEpisodes {
					if entities, err := c.graph.GetEpisodeEntities(ep.ID); err == nil {
						for _, eid := range entities {
							newGroup.entityIDs[eid] = true
						}
					}
				}
				newGroups = append(newGroups, newGroup)
				continue
			}
			// Labile: extend the existing engram.
			for _, ep := range newEpisodes {
				if err := c.graph.LinkEngramToSource(engramID, ep.ID); err != nil {
					log.Printf("[consolidate] Failed to link episode %s to existing engram %s: %v", ep.ID[:5], engramID, err)
					continue
				}
			}
			if err := c.graph.MarkEngramForReconsolidation(engramID); err != nil {
				log.Printf("[consolidate] Failed to mark engram %s for reconsolidation: %v", engramID, err)
			}
		}

		// Apply MaxGroupSize: split large components into ≤MaxGroupSize sub-groups.
		// Done after Phase 3a so rerouted non-labile groups are also subject to splitting.
		if c.MaxGroupSize > 0 {
			split := make([]*episodeGroup, 0, len(newGroups))
			for _, g := range newGroups {
				split = append(split, c.splitEpisodeGroup(g, c.MaxGroupSize)...)
			}
			newGroups = split
		}

		// Phase 3b: Create engrams from new clustered groups
		log.Printf("[consolidate] Phase 2 done: %d groups to summarise", len(newGroups))
		created := 0
		for i, group := range newGroups {
			if err := c.consolidateGroup(group, i); err != nil {
				log.Printf("[consolidate] Failed to consolidate group %d: %v", i, err)
				continue
			}
			created++
		}

		totalCreated += created
		log.Printf("[consolidate] Phase 3 done: %d engrams created this batch (%d total)", created, totalCreated)

		// Phase 3c: Link episodes to semantically related existing traces (episode_engram_edges)
		// This captures cross-references between individual episodes and historical traces
		// that they're related to but didn't consolidate into.
		linked := c.linkEpisodesToRelatedEngrams(episodes)
		if linked > 0 {
			log.Printf("[consolidate] Created %d episode→trace cross-reference edges", linked)
		}

		// Phase 4: Batch reconsolidation of traces with new episodes
		engramsNeedingRecon, err := c.graph.GetEngramsNeedingReconsolidation()
		if err != nil {
			log.Printf("[consolidate] Failed to get traces needing reconsolidation: %v", err)
		} else if len(engramsNeedingRecon) > 0 {
			log.Printf("[consolidate] Reconsolidating %d traces with new episodes", len(engramsNeedingRecon))
			for _, engramID := range engramsNeedingRecon {
				if err := c.reconsolidateEngram(engramID); err != nil {
					log.Printf("[consolidate] Failed to reconsolidate trace %s: %v", engramID, err)
				}
			}
		}

		// If we processed fewer than 500 episodes, we're done
		if len(episodes) < 500 {
			return totalCreated, nil
		}
	}
}

// clusterEpisodesByEdges uses Claude-inferred edges to cluster episodes into groups.
// Uses a simple connected components algorithm on high-confidence edges.
// Returns: new groups to consolidate, and existing traces with new episodes to add.
func (c *Consolidator) clusterEpisodesByEdges(episodes []*graph.Episode, edges []EpisodeEdge) ([]*episodeGroup, map[string][]*graph.Episode) {
	if len(episodes) == 0 {
		return nil, nil
	}

	// Build episode ID -> episode map
	episodeMap := make(map[string]*graph.Episode)
	for _, ep := range episodes {
		episodeMap[ep.ID] = ep
	}

	// Check which episodes are already part of existing engrams.
	// All batch episodes are unconsolidated so GetEpisodeEngrams returns empty for them.
	// We keep this loop as a safety net in case of inconsistent state.
	episodeToEngram := make(map[string]string) // episode ID -> engram ID
	for _, ep := range episodes {
		engrams, err := c.graph.GetEpisodeEngrams(ep.ID)
		if err == nil && len(engrams) > 0 {
			episodeToEngram[ep.ID] = engrams[0]
		}
	}

	// Cross-batch lookup: find engrams reachable via edges to consolidated episodes.
	// This makes reconsolidation work: if a batch episode replies to (or was previously
	// linked to) a consolidated episode, it extends that consolidated engram.
	batchIDs := make(map[string]bool, len(episodeMap))
	for id := range episodeMap {
		batchIDs[id] = true
	}
	if crossBatch, err := c.graph.QueryCrossBatchEpisodeEngrams(batchIDs); err == nil {
		for epID, engramID := range crossBatch {
			if _, exists := episodeToEngram[epID]; !exists {
				episodeToEngram[epID] = engramID
			}
		}
	} else {
		log.Printf("[consolidate] cross-batch engram lookup failed: %v", err)
	}

	// Build adjacency list from high-confidence edges (confidence >= 0.7)
	adjacency := make(map[string][]string)
	for _, edge := range edges {
		if edge.Confidence >= 0.7 {
			adjacency[edge.FromID] = append(adjacency[edge.FromID], edge.ToID)
			adjacency[edge.ToID] = append(adjacency[edge.ToID], edge.FromID)
		}
	}

	// Find connected components using DFS
	visited := make(map[string]bool)
	var newGroups []*episodeGroup
	existingEngramsWithNewEpisodes := make(map[string][]*graph.Episode)

	var dfs func(episodeID string, group *episodeGroup, existingEngramID *string)
	dfs = func(episodeID string, group *episodeGroup, existingEngramID *string) {
		if visited[episodeID] {
			return
		}
		visited[episodeID] = true

		ep, exists := episodeMap[episodeID]
		if !exists {
			return
		}

		// Check if this episode belongs to an existing trace
		if engramID, ok := episodeToEngram[ep.ID]; ok {
			// Episode is already in a trace - mark this cluster as belonging to that trace
			if *existingEngramID == "" {
				*existingEngramID = engramID
			} else if *existingEngramID != engramID {
				// Conflict: cluster spans multiple existing traces
				// For now, prefer the first trace encountered
				log.Printf("[consolidate] Warning: episode cluster spans multiple traces (%s, %s)", *existingEngramID, engramID)
			}
		}

		group.episodes = append(group.episodes, ep)

		// Visit neighbors
		for _, neighborID := range adjacency[episodeID] {
			dfs(neighborID, group, existingEngramID)
		}
	}

	// Process each episode
	for _, ep := range episodes {
		if visited[ep.ID] {
			continue
		}

		group := &episodeGroup{
			episodes:  []*graph.Episode{},
			entityIDs: make(map[string]bool),
		}
		existingEngramID := ""

		dfs(ep.ID, group, &existingEngramID)

		// Collect entities from all episodes in group
		for _, e := range group.episodes {
			entities, _ := c.graph.GetEpisodeEntities(e.ID)
			for _, entityID := range entities {
				group.entityIDs[entityID] = true
			}
		}

		if len(group.episodes) < c.MinGroupSize {
			continue
		}

		if existingEngramID != "" {
			// This cluster belongs to an existing trace
			// Find episodes that aren't already in the trace
			for _, e := range group.episodes {
				if episodeToEngram[e.ID] == "" {
					// New episode for this trace
					existingEngramsWithNewEpisodes[existingEngramID] = append(existingEngramsWithNewEpisodes[existingEngramID], e)
				}
			}
		} else {
			// New cluster - create a new trace
			newGroups = append(newGroups, group)
		}
	}

	return newGroups, existingEngramsWithNewEpisodes
}

// reconsolidateEngram regenerates an engram's summary after new episodes are linked to it.
// Skips if the engram's labile window has expired — frozen engrams don't get updated.
func (c *Consolidator) reconsolidateEngram(engramID string) error {
	// Check labile window: only reconsolidate while the engram is modifiable.
	engram, err := c.graph.GetEngram(engramID)
	if err != nil || engram == nil {
		return fmt.Errorf("failed to get engram for labile check: %w", err)
	}
	if !engram.IsLabile() {
		log.Printf("[consolidate] Engram %s labile window expired, skipping reconsolidation", engramID[:min(8, len(engramID))])
		return c.graph.ClearReconsolidationFlag(engramID)
	}

	// Get all source episodes for this engram
	sourceEpisodes, err := c.graph.GetEngramSourceEpisodes(engramID)
	if err != nil {
		return fmt.Errorf("failed to get source episodes: %w", err)
	}

	if len(sourceEpisodes) == 0 {
		return fmt.Errorf("trace has no source episodes")
	}

	// Build fragments for summarization
	var fragments []string
	episodePtrs := make([]*graph.Episode, len(sourceEpisodes))
	for i, ep := range sourceEpisodes {
		episodePtrs[i] = &ep
		prefix := ""
		if ep.Author != "" {
			prefix = ep.Author + ": "
		}
		fragments = append(fragments, prefix+ep.Content)
	}

	// Generate new summary
	var summary string
	if c.llm != nil {
		// Fetch entity context from prior engrams (exclude self during reconsolidation)
		entityIDs, _ := c.graph.GetEngramEntities(engramID)
		entityCtx := c.buildEntityContext(entityIDs, engramID)
		prompt := c.buildConsolidationPrompt(episodePtrs, entityCtx, "")
		summary, err = c.llm.Generate(prompt)
		if err != nil {
			summary = truncate(strings.Join(fragments, " "), 300)
		}
	} else {
		summary = truncate(strings.Join(fragments, " "), 300)
	}

	summary = strings.TrimPrefix(summary, "[Past] ")

	// Calculate new embedding
	var embedding []float64
	if c.llm != nil {
		embedding, _ = c.llm.Embed(summary)
	}
	if len(embedding) == 0 {
		embedding = calculateCentroid(episodePtrs)
	}

	// Reclassify trace type
	engramType := classifyEngramType(summary, episodePtrs)

	// Compute event_time as the latest timestamp_event among all current source episodes.
	var eventTime time.Time
	for _, ep := range sourceEpisodes {
		if ep.TimestampEvent.After(eventTime) {
			eventTime = ep.TimestampEvent
		}
	}
	if eventTime.IsZero() {
		eventTime = time.Now()
	}

	// Update trace
	if err := c.graph.UpdateEngram(engramID, summary, embedding, engramType, len(sourceEpisodes), eventTime); err != nil {
		return fmt.Errorf("failed to update trace: %w", err)
	}

	// Regenerate C8 summary
	if c.llm != nil {
		if err := c.graph.GenerateEngramSummaryLevel(engramID, graph.CompressionLevel8, episodePtrs, c.llm); err != nil {
			log.Printf("Failed to regenerate C8 summary for trace: %v", err)
		}
	}

	// Clear reconsolidation flag
	if err := c.graph.ClearReconsolidationFlag(engramID); err != nil {
		return fmt.Errorf("failed to clear reconsolidation flag: %w", err)
	}

	log.Printf("[consolidate] Reconsolidated trace with %d episodes", len(sourceEpisodes))
	return nil
}

// consolidateGroup creates a trace from a group of episodes
func (c *Consolidator) consolidateGroup(group *episodeGroup, index int) error {
	// Build fragments for summarization
	var fragments []string
	for _, ep := range group.episodes {
		prefix := ""
		if ep.Author != "" {
			prefix = ep.Author + ": "
		}
		fragments = append(fragments, prefix+ep.Content)
	}

	// Generate summary - always use LLM for proper memory format
	var summary string
	var err error

	if c.llm != nil {
		// Convert entity ID map to slice for context lookup
		entityIDSlice := make([]string, 0, len(group.entityIDs))
		for id := range group.entityIDs {
			entityIDSlice = append(entityIDSlice, id)
		}
		entityCtx := c.buildEntityContext(entityIDSlice, "")
		prompt := c.buildConsolidationPrompt(group.episodes, entityCtx, group.priorContext)
		summary, err = c.llm.Generate(prompt)
		if err != nil {
			// Summarization failed, fall back to truncation
			summary = truncate(strings.Join(fragments, " "), 300)
		}
	} else {
		summary = truncate(strings.Join(fragments, " "), 300)
	}

	// Remove [Past] prefix if present (legacy)
	summary = strings.TrimPrefix(summary, "[Past] ")

	// Skip ephemeral/low-value content that shouldn't become long-term memories
	if isEphemeralContent(summary) || isAllLowInfo(group.episodes) {
		// Link episodes to sentinel trace so they aren't retried by GetUnconsolidatedEpisodes
		for _, ep := range group.episodes {
			c.graph.LinkEngramToSource("_ephemeral", ep.ID)
		}
		// Skipped low-value content
		return nil
	}

	// Generate engram ID using BLAKE3 hash of summary + timestamp
	engramID := graph.GenerateEngramID(summary, time.Now().UnixNano())

	// Calculate embedding
	var embedding []float64
	if c.llm != nil {
		embedding, _ = c.llm.Embed(summary)
	}
	if len(embedding) == 0 {
		embedding = calculateCentroid(group.episodes)
	}

	// Classify trace type
	engramType := classifyEngramType(summary, group.episodes)

	// Compute event_time as the latest timestamp_event among source episodes.
	var eventTime time.Time
	for _, ep := range group.episodes {
		if ep.TimestampEvent.After(eventTime) {
			eventTime = ep.TimestampEvent
		}
	}
	if eventTime.IsZero() {
		eventTime = time.Now()
	}

	// Create engram
	engram := &graph.Engram{
		ID:         engramID,
		Summary:    summary,
		Topic:      "conversation",
		EngramType: engramType,
		Activation: 0.5, // Neutral starting activation (schema default), decay will lower over time
		Strength:   len(group.episodes), // Strength based on number of source episodes
		Embedding:  embedding,
		EventTime:  eventTime,
		CreatedAt:  time.Now(),
	}

	if err := c.graph.AddEngram(engram); err != nil {
		return fmt.Errorf("failed to add trace: %w", err)
	}

	// Fire the new-engram hook asynchronously (used by forward schema matcher).
	if c.NewEngramHook != nil {
		hook := c.NewEngramHook
		go hook(engram)
	}

	// Mark engram as labile for 24 hours: new related episodes extend it via reconsolidation.
	// After the window expires, new related episodes form a separate engram instead.
	engram.MakeLabile(24 * time.Hour)
	if err := c.graph.UpdateEngramLabileUntil(engramID, engram.LabileUntil); err != nil {
		log.Printf("[consolidate] Failed to set labile window for %s: %v", engramID[:8], err)
	}

	// Link engram to all source episodes
	for _, ep := range group.episodes {
		if err := c.graph.LinkEngramToSource(engramID, ep.ID); err != nil {
			log.Printf("Failed to link trace to episode %s: %v", ep.ID, err)
		}
	}

	// Link engram to all entities (only if entity exists) and regenerate entity pyramids
	for entityID := range group.entityIDs {
		// Check if entity exists before attempting to link
		if exists, _ := c.graph.EntityExists(entityID); !exists {
			continue // Skip orphaned entity references
		}
		if err := c.graph.LinkEngramToEntity(engramID, entityID); err != nil {
			log.Printf("Failed to link trace to entity %s: %v", entityID, err)
		}
		// Regenerate entity pyramid asynchronously (relations may have been updated)
		if c.llm != nil {
			eid := entityID
			go func() {
				if err := c.graph.GenerateEntityPyramid(eid, c.llm); err != nil {
					log.Printf("Failed to generate entity pyramid for %s: %v", eid, err)
				}
			}()
		}
	}

	// Generate C8 summary only (for verbose display and basic retrieval)
	// Full pyramid (L64→L32→L16→L8→L4) should be backfilled by compress-traces later
	if c.llm != nil {
		if err := c.graph.GenerateEngramSummaryLevel(engramID, graph.CompressionLevel8, group.episodes, c.llm); err != nil {
			log.Printf("Failed to generate C8 summary for trace %s: %v", engramID[:8], err)
		}
	}

	// Link to similar traces (>0.85 similarity)
	if len(embedding) > 0 {
		c.linkToSimilarEngrams(engramID, embedding, 0.85)
	}

	// Operational traces are logged if verbose mode is enabled

	return nil
}

// calculateCentroid computes the centroid embedding from multiple episodes
func calculateCentroid(episodes []*graph.Episode) []float64 {
	if len(episodes) == 0 {
		return nil
	}

	// Find first episode with embedding to determine dimension
	var dim int
	for _, ep := range episodes {
		if len(ep.Embedding) > 0 {
			dim = len(ep.Embedding)
			break
		}
	}
	if dim == 0 {
		return nil
	}

	// Calculate mean
	centroid := make([]float64, dim)
	count := 0
	for _, ep := range episodes {
		if len(ep.Embedding) == dim {
			for i, v := range ep.Embedding {
				centroid[i] += v
			}
			count++
		}
	}

	if count == 0 {
		return nil
	}

	for i := range centroid {
		centroid[i] /= float64(count)
	}

	return centroid
}

// labelFragment returns the role label for an episode, e.g. "[BotName]", "[Alice (owner)]", "[Alice]".
func (c *Consolidator) labelFragment(ep *graph.Episode) string {
	// Check if this is the bot
	if c.BotName != "" {
		if (ep.Author != "" && ep.Author == c.BotName) ||
			(ep.AuthorID != "" && c.BotAuthorID != "" && ep.AuthorID == c.BotAuthorID) {
			return "[" + c.BotName + "]"
		}
	}
	// Check if this is an owner (matched by AuthorID)
	if len(c.OwnerIDs) > 0 && ep.AuthorID != "" {
		for _, ownerID := range c.OwnerIDs {
			if ep.AuthorID == ownerID {
				name := ep.Author
				if name == "" {
					name = "owner"
				}
				return "[" + name + " (owner)]"
			}
		}
	}
	// Third party or unknown
	if ep.Author != "" {
		return "[" + ep.Author + "]"
	}
	return ""
}

// buildEntityContext fetches brief summaries from prior engrams linked to the given
// entity IDs and formats them as a bulleted context block for the consolidation prompt.
// excludeEngramID (non-empty during reconsolidation) is filtered from results.
// Returns "" when no relevant prior context exists.
func (c *Consolidator) buildEntityContext(entityIDs []string, excludeEngramID string) string {
	if len(entityIDs) == 0 || c.graph == nil {
		return ""
	}

	const capPerEntity = 2
	const totalCap = 8

	// Fetch prior engram IDs linked to these entities (2 per entity, deduped)
	engramIDs, err := c.graph.GetEngramsForEntitiesBatch(entityIDs, capPerEntity)
	if err != nil || len(engramIDs) == 0 {
		return ""
	}

	// Filter out the engram being reconsolidated and the _ephemeral sentinel
	filtered := engramIDs[:0]
	for _, id := range engramIDs {
		if id != excludeEngramID && id != "_ephemeral" {
			filtered = append(filtered, id)
		}
	}
	engramIDs = filtered

	if len(engramIDs) == 0 {
		return ""
	}
	if len(engramIDs) > totalCap {
		engramIDs = engramIDs[:totalCap]
	}

	// Fetch engrams — GetEngramsBatch checks L0 (verbatim) first, then pyramid levels,
	// then falls back to engrams.summary. Works for newly created engrams without pyramids.
	engrams, err := c.graph.GetEngramsBatch(engramIDs)
	if err != nil || len(engrams) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, id := range engramIDs {
		en, ok := engrams[id]
		if !ok || en.Summary == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(en.Summary)
		sb.WriteString("\n")
	}

	return sb.String()
}

// buildConsolidationPrompt constructs an identity-aware, temporally-framed prompt for
// generating a memory summary from a group of episodes. entityContext is an optional
// pre-formatted block of prior memory lines (from buildEntityContext); empty = omitted.
// priorContext is optional preceding content (prior sub-group episodes or a prior engram
// summary) shown for reference — the LLM should not re-summarize it.
func (c *Consolidator) buildConsolidationPrompt(episodes []*graph.Episode, entityContext, priorContext string) string {
	// Find latest timestamp across episodes for temporal anchor
	var latest time.Time
	for _, ep := range episodes {
		if ep.TimestampEvent.After(latest) {
			latest = ep.TimestampEvent
		}
	}
	if latest.IsZero() {
		latest = time.Now()
	}

	var sb strings.Builder

	// Role context header — only when bot identity is configured
	if c.BotName != "" {
		sb.WriteString("You are recording a memory for an AI assistant named " + c.BotName + ".\n\n")
		sb.WriteString("Role context:\n")
		sb.WriteString("- Fragments labeled \"[" + c.BotName + "]\" are from the assistant. Write about the\n")
		sb.WriteString("  assistant's statements in first person: \"I\", \"me\", \"my\".\n")
		sb.WriteString("- Fragments labeled with \"(owner)\" are from the owner. Refer to them as\n")
		sb.WriteString("  \"the owner\" or by their name.\n")
		sb.WriteString("- All other names are third parties. Refer to them by name.\n\n")
	}

	sb.WriteString("Summarize the following fragments into a single memory (1-2 sentences).\n\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Attribute statements to their SUBJECT, not just their speaker.\n")
	sb.WriteString("  Example: [owner]: \"Alice prefers mornings\" → Alice prefers morning meetings\n")
	sb.WriteString("- Pronouns resolve to the author of that fragment:\n")
	sb.WriteString("  [alice]: \"I prefer mornings\" → Alice prefers mornings\n")
	if c.BotName != "" {
		sb.WriteString("  [owner]: \"you should be more concise\" → The owner wants me to be more concise\n")
	}
	sb.WriteString("- Standing preferences/facts: present tense (\"Alice prefers...\", \"I should...\")\n")
	sb.WriteString("- One-time events, approvals, specific actions: past tense + time reference.\n")
	sb.WriteString("  Example: \"On Feb 21, the owner approved a restart\"\n")
	sb.WriteString("  NEVER generalize a one-time approval into a standing permission.\n")
	sb.WriteString("- Include ONLY information explicitly stated — do not infer or embellish.\n")
	sb.WriteString("- Output ONLY the memory text, no preamble.\n\n")

	// Inject preceding context (prior sub-group episodes or prior engram summary).
	// Shown for continuity — the LLM should not re-summarize it.
	if priorContext != "" {
		sb.WriteString("Preceding context (for reference only — do not re-summarize):\n")
		sb.WriteString(priorContext)
		sb.WriteString("\n\n")
	}

	// Inject prior entity context — only when available (second+ consolidation run)
	if entityContext != "" {
		sb.WriteString("Background context from prior memories:\n")
		sb.WriteString(entityContext)
		sb.WriteString("\n")
	}

	timeStr := latest.Format("Jan 2, 2006 at 15:04")
	sb.WriteString("Fragments (" + timeStr + "):\n")
	for _, ep := range episodes {
		label := c.labelFragment(ep)
		if label != "" {
			sb.WriteString(label + ": " + ep.Content + "\n")
		} else {
			sb.WriteString(ep.Content + "\n")
		}
	}

	sb.WriteString("\nMemory:")
	return sb.String()
}

// truncate shortens text to maxLen, adding ellipsis if needed
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// isAllLowInfo returns true if every episode in the group is a backchannel or
// greeting (e.g., "ok", "great", "hi"). Uses the dialogue_act field if set,
// otherwise falls back to content-based classification.
func isAllLowInfo(episodes []*graph.Episode) bool {
	if len(episodes) == 0 {
		return true
	}
	for _, ep := range episodes {
		act := ep.DialogueAct
		if act == "" {
			// Fallback: classify content directly
			act = string(filter.ClassifyDialogueAct(ep.Content))
		}
		if act != string(filter.ActBackchannel) && act != string(filter.ActGreeting) {
			return false
		}
	}
	return true
}

// classifyEngramType determines whether a trace is operational (transient system
// activity) or knowledge (facts, decisions, preferences worth remembering).
// Operational traces decay 3x faster during activation decay.
func classifyEngramType(summary string, episodes []*graph.Episode) graph.EngramType {
	lower := strings.ToLower(summary)

	// Meeting reminders and calendar notifications
	// Check for calendar notification patterns (starts/starting soon/in, Google Meet links)
	isMeetingReminder := strings.Contains(lower, "upcoming meeting") ||
		strings.Contains(lower, "start soon") || // Covers "starts soon", "start soon", "starting soon"
		strings.Contains(lower, "starts in") && (strings.Contains(lower, "m") || strings.Contains(lower, "minute")) ||
		strings.Contains(lower, "meeting starts") ||
		strings.Contains(lower, "heads up") && (strings.Contains(lower, "meeting") || strings.Contains(lower, "sprint") || strings.Contains(lower, "planning")) ||
		strings.Contains(lower, "meet.google.com") ||
		// Sprint planning notifications (even without "meeting" word)
		strings.Contains(lower, "sprint planning") && (strings.Contains(lower, "starts") || strings.Contains(lower, "soon") || strings.Contains(lower, "in "))
	if isMeetingReminder && !strings.Contains(lower, "discussed") && !strings.Contains(lower, "decided") {
		return graph.EngramTypeOperational
	}

	// State sync / deployment / restart activity
	if strings.Contains(lower, "state sync") || strings.Contains(lower, "synced state") ||
		strings.Contains(lower, "restarted") && !strings.Contains(lower, "because") ||
		strings.Contains(lower, "launchd service") && strings.Contains(lower, "running") ||
		strings.Contains(lower, "rebuilt binaries") ||
		strings.Contains(lower, "deployed") && !strings.Contains(lower, "decision") {
		return graph.EngramTypeOperational
	}

	// Autonomous wake confirmations / idle wakes
	if strings.Contains(lower, "no actionable work") ||
		strings.Contains(lower, "idle wake") ||
		strings.Contains(lower, "wellness check") && !strings.Contains(lower, "finding") {
		return graph.EngramTypeOperational
	}

	// Pure acknowledgments without substantive content
	if strings.Contains(lower, "confirmed") && !strings.Contains(lower, "decision") &&
		!strings.Contains(lower, "preference") && len(summary) < 150 {
		return graph.EngramTypeOperational
	}

	// Dev work implementation notes without decision rationale
	// These are status updates about work done, not learnings or decisions
	if isDevWorkNote(lower) && !hasKnowledgeIndicator(lower) {
		return graph.EngramTypeOperational
	}

	return graph.EngramTypeKnowledge
}

// isDevWorkNote checks if the summary appears to be a dev work status update
func isDevWorkNote(lower string) bool {
	// Past-tense implementation verbs
	devVerbs := []string{
		"updated ", "implemented ", "fixed ", "added ", "created ",
		"refactored ", "removed ", "deleted ", "modified ", "changed ",
		"wrote ", "built ", "expanded ", "pruned ", "wired ",
		"researched ", "explored ", "investigated ", "analyzed ",
	}
	for _, verb := range devVerbs {
		if strings.Contains(lower, verb) {
			return true
		}
	}
	return false
}

// hasKnowledgeIndicator checks if the summary contains decision rationale or learnings
func hasKnowledgeIndicator(lower string) bool {
	indicators := []string{
		"decided", "because", "reason", "chose", "choice",
		"approach", "prefer", "finding", "learned", "discovered",
		"root cause", "conclusion", "insight", "realized",
		"will use", "should use", "plan to", "strategy",
	}
	for _, indicator := range indicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

// isEphemeralContent returns true if the summary represents transient content
// that shouldn't be stored as a long-term memory trace.
func isEphemeralContent(summary string) bool {
	lower := strings.ToLower(summary)

	// Meeting countdown reminders ("X minutes and Y seconds")
	if strings.Contains(lower, "minutes and") && strings.Contains(lower, "seconds") {
		return true
	}

	// "starting in X minutes" without meaningful context
	if strings.Contains(lower, "starting in") && strings.Contains(lower, "minutes") &&
		len(summary) < 200 {
		return true
	}

	// "starts in X minutes" variant
	if strings.Contains(lower, "starts in") && strings.Contains(lower, "minutes") &&
		len(summary) < 200 {
		return true
	}

	return false
}

// linkEpisodesToRelatedEngrams creates episode_engram_edges for episodes that are
// semantically similar to existing traces they don't belong to. This captures
// cross-references between individual episodes and historical traces.
// Threshold: 0.80 similarity (lower than SIMILAR_TO edge threshold of 0.85)
func (c *Consolidator) linkEpisodesToRelatedEngrams(episodes []*graph.Episode) int {
	linked := 0
	const threshold = 0.80

	for _, ep := range episodes {
		if len(ep.Embedding) == 0 {
			continue
		}

		// Find the primary trace(s) this episode belongs to
		primaryEngrams, err := c.graph.GetEpisodeEngrams(ep.ID)
		if err != nil || len(primaryEngrams) == 0 {
			continue
		}

		// Build set of traces to exclude (primary trace + _ephemeral)
		excludeSet := make(map[string]bool)
		for _, t := range primaryEngrams {
			excludeSet[t] = true
		}
		excludeSet["_ephemeral"] = true

		// Find other traces with high similarity to this episode's embedding
		// Use first primary trace as the "exclude" ID (FindSimilarTracesAboveThreshold only takes one)
		similar, err := c.graph.FindSimilarEngramsAboveThreshold(ep.Embedding, threshold, primaryEngrams[0])
		if err != nil {
			continue
		}

		for _, s := range similar {
			if excludeSet[s.ID] {
				continue
			}
			// Create episode_engram_edge with similarity as confidence
			desc := "semantically related"
			if err := c.graph.AddEpisodeEngramEdge(ep.ID, s.ID, desc, s.Similarity); err == nil {
				linked++
			}
		}
	}

	return linked
}

// BackfillEpisodeEngramEdges iterates over all consolidated episodes with embeddings
// and creates episode_engram_edges for any that are semantically similar to traces
// they don't already belong to. Useful for one-time backfill after Phase 5 was deployed.
// Returns total edges created.
func (c *Consolidator) BackfillEpisodeEngramEdges(batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 500
	}

	total := 0
	offset := 0
	for {
		episodes, err := c.graph.GetConsolidatedEpisodesWithEmbeddings(offset, batchSize)
		if err != nil {
			return total, fmt.Errorf("failed to get consolidated episodes at offset %d: %w", offset, err)
		}
		if len(episodes) == 0 {
			break
		}

		linked := c.linkEpisodesToRelatedEngrams(episodes)
		total += linked
		log.Printf("[backfill] Processed %d episodes (offset=%d), created %d edges so far", len(episodes), offset, total)

		offset += len(episodes)
		if len(episodes) < batchSize {
			break
		}
	}

	return total, nil
}

// linkToSimilarEngrams finds existing traces with high similarity and creates SIMILAR_TO edges.
// Returns the number of edges created.
func (c *Consolidator) linkToSimilarEngrams(engramID string, embedding []float64, threshold float64) int {
	similar, err := c.graph.FindSimilarEngramsAboveThreshold(embedding, threshold, engramID)
	if err != nil {
		log.Printf("Failed to find similar traces: %v", err)
		return 0
	}

	linked := 0
	for _, s := range similar {
		err := c.graph.AddEngramRelation(engramID, s.ID, graph.EdgeSimilarTo, s.Similarity)
		if err == nil {
			linked++
		}
	}
	return linked
}

// cosineSimilarity computes cosine similarity between two embeddings
func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// loadExistingEdges loads episode-episode edges from the database for the given episodes
func (c *Consolidator) loadExistingEdges(episodes []*graph.Episode) []EpisodeEdge {
	if len(episodes) == 0 {
		return nil
	}

	// Build episode ID set
	episodeIDs := make(map[string]bool)
	for _, ep := range episodes {
		episodeIDs[ep.ID] = true
	}

	// Query edges directly from database to avoid direction issues
	// GetEpisodeNeighbors returns bidirectional, but we want to preserve original direction
	rows, err := c.graph.QueryEpisodeEdges(episodeIDs)
	if err != nil {
		return nil
	}

	var edges []EpisodeEdge
	for _, row := range rows {
		// Only include edges where both endpoints are in the current batch
		if episodeIDs[row.FromID] && episodeIDs[row.ToID] {
			edges = append(edges, EpisodeEdge{
				FromID:       row.FromID,
				ToID:         row.ToID,
				Relationship: row.Relationship,
				Confidence:   row.Confidence,
			})
		}
	}

	return edges
}

// detectDuplicateEpisodes finds near-duplicate episodes using C16 summary similarity.
// Returns high-confidence edges (1.0) for episodes with similarity > 0.95.
// This catches obvious duplicates that Claude inference might miss.
func (c *Consolidator) detectDuplicateEpisodes(episodes []*graph.Episode) []EpisodeEdge {
	if len(episodes) < 2 {
		return nil
	}

	// Load C16 summaries for all episodes
	episodeIDs := make([]string, len(episodes))
	for i, ep := range episodes {
		episodeIDs[i] = ep.ID
	}

	summaries, err := c.graph.GetEpisodeSummariesBatch(episodeIDs, graph.CompressionLevel16)
	if err != nil {
		log.Printf("Failed to load C16 summaries for duplicate detection: %v", err)
		return nil
	}

	// Build map of episode ID -> embedding (use episode embedding as proxy for C16)
	type episodeWithEmbedding struct {
		ep        *graph.Episode
		embedding []float64
	}

	var withEmbeddings []episodeWithEmbedding
	for _, ep := range episodes {
		if len(ep.Embedding) > 0 {
			withEmbeddings = append(withEmbeddings, episodeWithEmbedding{
				ep:        ep,
				embedding: ep.Embedding,
			})
		}
	}

	if len(withEmbeddings) < 2 {
		return nil
	}

	// Compare all pairs using cosine similarity
	var duplicateEdges []EpisodeEdge
	threshold := 0.95 // Very high threshold to catch only near-duplicates

	for i := 0; i < len(withEmbeddings); i++ {
		for j := i + 1; j < len(withEmbeddings); j++ {
			ep1 := withEmbeddings[i]
			ep2 := withEmbeddings[j]

			similarity := cosineSimilarity(ep1.embedding, ep2.embedding)
			if similarity >= threshold {
				// Check C16 summaries if available for additional validation
				summary1, ok1 := summaries[ep1.ep.ID]
				summary2, ok2 := summaries[ep2.ep.ID]

				if ok1 && ok2 && summary1 != nil && summary2 != nil {
					// Both have C16 summaries - verify they're actually similar
					if !strings.Contains(strings.ToLower(summary1.Summary), strings.ToLower(summary2.Summary[:min(len(summary2.Summary), 20)])) &&
					   !strings.Contains(strings.ToLower(summary2.Summary), strings.ToLower(summary1.Summary[:min(len(summary1.Summary), 20)])) {
						// Embeddings similar but content different - skip
						continue
					}
				}

				duplicateEdges = append(duplicateEdges, EpisodeEdge{
					FromID:       ep1.ep.ID,
					ToID:         ep2.ep.ID,
					Relationship: "duplicate_of",
					Confidence:   similarity,
				})
			}
		}
	}

	return duplicateEdges
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// inferEpisodeEpisodeLinks uses Claude to infer semantic relationships between episodes
// using a sliding window approach with 50% overlap to achieve O(kn) complexity instead of O(n²)
func (c *Consolidator) inferEpisodeEpisodeLinks(ctx context.Context, episodes []*graph.Episode) ([]EpisodeEdge, error) {
	if len(episodes) == 0 {
		return nil, nil
	}
	if c.claude == nil {
		return nil, nil
	}

	// Sort episodes by timestamp to ensure temporal ordering
	sorted := make([]*graph.Episode, len(episodes))
	copy(sorted, episodes)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TimestampEvent.Before(sorted[j].TimestampEvent)
	})

	// Load C16 summaries for all episodes
	episodeIDs := make([]string, len(sorted))
	for i, ep := range sorted {
		episodeIDs[i] = ep.ID
	}

	summaries, err := c.graph.GetEpisodeSummariesBatch(episodeIDs, graph.CompressionLevel16)
	if err != nil {
		log.Printf("Failed to load C16 summaries: %v", err)
		return nil, err
	}

	// Create enriched episodes struct with summary
	type enrichedEpisode struct {
		*graph.Episode
		summaryC16 string
	}

	// Build enriched list: use stored C16 summary when available, otherwise fall back
	// to the first 32 words of episode content. This ensures inference always runs even
	// for fresh episodes that have not yet had summaries generated by compress-traces.
	var withSummaries []*enrichedEpisode
	for _, ep := range sorted {
		summaryC16 := ""
		if s, ok := summaries[ep.ID]; ok && s != nil {
			summaryC16 = s.Summary
		} else {
			words := strings.Fields(ep.Content)
			if len(words) > 32 {
				words = words[:32]
			}
			summaryC16 = strings.Join(words, " ")
		}
		withSummaries = append(withSummaries, &enrichedEpisode{
			Episode:    ep,
			summaryC16: summaryC16,
		})
	}

	if len(withSummaries) == 0 {
		return nil, nil
	}

	// Calculate sliding window parameters
	batchSize := c.episodeBatchSize
	stepSize := int(float64(batchSize) * (1.0 - c.episodeBatchOverlap))
	if stepSize < 1 {
		stepSize = 1
	}

	// Process episodes in sliding windows
	totalWindows := (len(withSummaries)-batchSize+stepSize-1)/stepSize + 1
	if totalWindows < 1 {
		totalWindows = 1
	}
	windowNum := 0
	var allEdges []EpisodeEdge
	for start := 0; start < len(withSummaries); start += stepSize {
		end := start + batchSize
		if end > len(withSummaries) {
			end = len(withSummaries)
		}

		enrichedBatch := withSummaries[start:end]
		if len(enrichedBatch) < 2 {
			break // Need at least 2 episodes to infer edges
		}

		windowNum++
		log.Printf("[consolidate] Edge inference: window %d/%d", windowNum, totalWindows)

		// Create episodesWithSummary slice for Claude
		episodesForInference := make([]EpisodeForInference, len(enrichedBatch))
		for i, e := range enrichedBatch {
			episodesForInference[i] = &episodeWithSummary{
				Episode:    e.Episode,
				summaryC16: e.summaryC16,
			}
		}

		// Processing batch for edge inference

		// Infer edges for this batch using Claude
		edges, err := c.claude.InferEpisodeEdges(ctx, episodesForInference)
		if err != nil {
			log.Printf("Failed to infer edges for batch %d-%d: %v", start, end-1, err)
			continue
		}

		allEdges = append(allEdges, edges...)

		// Stop if we've reached the end
		if end == len(withSummaries) {
			break
		}
	}

	return allEdges, nil
}

// episodeWithSummary wraps an Episode with its C16 summary for inference
type episodeWithSummary struct {
	*graph.Episode
	summaryC16 string
}

// Interface implementation for EpisodeForInference
func (e *episodeWithSummary) GetID() string {
	return e.Episode.ID
}

func (e *episodeWithSummary) GetAuthor() string {
	return e.Episode.Author
}

func (e *episodeWithSummary) GetTimestamp() time.Time {
	return e.Episode.TimestampEvent
}

func (e *episodeWithSummary) GetSummaryC16() string {
	return e.summaryC16
}

// mapsEqual returns true if both maps have exactly the same set of keys.
func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// splitEpisodeGroup divides a group larger than maxSize into sub-groups in timestamp order.
// Each sub-group (except the first) receives the previous sub-group's formatted episodes as
// priorContext so the LLM has continuity across splits. The first sub-group inherits the
// parent group's priorContext (e.g., a prior engram summary from the non-labile path).
// All entity IDs from the original group are propagated to every sub-group.
func (c *Consolidator) splitEpisodeGroup(group *episodeGroup, maxSize int) []*episodeGroup {
	if len(group.episodes) <= maxSize {
		return []*episodeGroup{group}
	}

	// Sort by event timestamp to preserve temporal locality within each sub-group.
	sorted := make([]*graph.Episode, len(group.episodes))
	copy(sorted, group.episodes)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TimestampEvent.Before(sorted[j].TimestampEvent)
	})

	var result []*episodeGroup
	for start := 0; start < len(sorted); start += maxSize {
		end := start + maxSize
		if end > len(sorted) {
			end = len(sorted)
		}

		sub := &episodeGroup{
			episodes:  sorted[start:end],
			entityIDs: make(map[string]bool, len(group.entityIDs)),
		}
		for id := range group.entityIDs {
			sub.entityIDs[id] = true
		}

		if len(result) == 0 {
			// First sub-group inherits the parent's prior context.
			sub.priorContext = group.priorContext
		} else {
			// Subsequent sub-groups receive the previous sub-group's episodes as context.
			prev := result[len(result)-1]
			var parts []string
			for _, ep := range prev.episodes {
				label := c.labelFragment(ep)
				if label != "" {
					parts = append(parts, label+": "+ep.Content)
				} else {
					parts = append(parts, ep.Content)
				}
			}
			sub.priorContext = strings.Join(parts, "\n")
		}

		result = append(result, sub)
	}
	return result
}

// deduplicateEdges removes duplicate edges (same from/to/relationship)
// NOTE: Multiple different relationships between the same pair are kept intentionally,
// as they represent different semantic connections (e.g., "answers" + "elaborates on")
func deduplicateEdges(edges []EpisodeEdge) []EpisodeEdge {
	seen := make(map[string]bool)
	var unique []EpisodeEdge

	for _, edge := range edges {
		// Create key from fromID, toID, and relationship
		key := edge.FromID + "|" + edge.ToID + "|" + edge.Relationship
		if !seen[key] {
			seen[key] = true
			unique = append(unique, edge)
		}
	}

	return unique
}

// printEdgeSummaries prints episode edge summaries in verbose mode
// Format: [id] 8-word summary -> relationship: [other-id]
func (c *Consolidator) printEdgeSummaries(episodes []*graph.Episode, edges []EpisodeEdge) {
	// Build episode ID -> episode map for quick lookup
	episodeMap := make(map[string]*graph.Episode)
	for _, ep := range episodes {
		episodeMap[ep.ID] = ep
	}

	// Load C8 summaries for all episodes
	episodeIDs := make([]string, len(episodes))
	for i, ep := range episodes {
		episodeIDs[i] = ep.ID
	}

	summaries, err := c.graph.GetEpisodeSummariesBatch(episodeIDs, graph.CompressionLevel8)
	if err != nil {
		log.Printf("Failed to load C8 summaries for edge display: %v", err)
		return
	}

	// Build edge map: fromID -> []edge
	edgeMap := make(map[string][]EpisodeEdge)
	for _, edge := range edges {
		edgeMap[edge.FromID] = append(edgeMap[edge.FromID], edge)
	}

	log.Printf("\n=== Episode Edge Summary ===")

	// Build shortID map for target episodes
	targetShortIDs := make(map[string]string)
	for _, edge := range edges {
		if ep, ok := episodeMap[edge.ToID]; ok {
			targetShortIDs[edge.ToID] = ep.ID[:5]
		}
	}

	// Print each episode and its outgoing edges
	for _, ep := range episodes {
		shortID := ep.ID[:5]

		// Get C8 summary (8 words) for display
		summary := ep.Content
		if s, ok := summaries[ep.ID]; ok && s != nil {
			summary = s.Summary
		} else {
			// Fallback: truncate content to approximately 8 words
			words := strings.Fields(summary)
			if len(words) > 8 {
				summary = strings.Join(words[:8], " ")
			}
		}

		// Truncate summary to fit display
		if len(summary) > 60 {
			summary = summary[:60] + "..."
		}

		// Check if this episode has outgoing edges
		outEdges := edgeMap[ep.ID]
		if len(outEdges) == 0 {
			// No outgoing edges
			log.Printf("[%s] %s", shortID, summary)
		} else {
			// Print with edges
			for i, edge := range outEdges {
				targetShortID := targetShortIDs[edge.ToID]
				if targetShortID == "" {
					// Fallback if target not in current batch
					targetShortID = edge.ToID
					if len(targetShortID) > 5 {
						targetShortID = targetShortID[:5]
					}
				}

				if i == 0 {
					log.Printf("[%s] %s -> %s: [%s]",
						shortID, summary, edge.Relationship, targetShortID)
				} else {
					// Continuation line for multiple edges from same episode
					log.Printf("       %*s -> %s: [%s]",
						len(summary), "", edge.Relationship, targetShortID)
				}
			}
		}
	}

	log.Printf("=== End Edge Summary ===\n")
}

