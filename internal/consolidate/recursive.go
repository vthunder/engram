package consolidate

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/vthunder/engram/internal/graph"
)

const (
	// MinRecursiveGroupSize is the minimum number of ungrouped engrams required to
	// attempt recursive consolidation at a given depth.
	MinRecursiveGroupSize = 3

	// MaxRecursiveDepth is a safety cap to prevent runaway recursion.
	MaxRecursiveDepth = 5

	// RecursiveBatchSize is the sliding window size for engram edge inference.
	RecursiveBatchSize = 15

	// RecursiveBatchOverlap is the overlap ratio for the sliding window (50%).
	RecursiveBatchOverlap = 0.5
)

// ShouldRunRecursive returns true if recursive consolidation should run:
// - At least minNewEngrams new L1 engrams have been created since the last recursive run, OR
// - At least hoursSinceLastRun hours have passed and there are ungrouped engrams.
func (c *Consolidator) ShouldRunRecursive(minNewEngrams int, hoursSinceLastRun float64) (bool, error) {
	ungrouped, err := c.graph.GetUngroupedEngrams(0)
	if err != nil {
		return false, err
	}
	return len(ungrouped) >= minNewEngrams, nil
}

// RunRecursive performs recursive consolidation: clusters ungrouped L1 engrams into L2
// engrams, then L2s into L3s, and so on until no cluster of size > 1 forms.
// Must be called after Run() completes.
// Returns the total number of higher-depth engrams created.
func (c *Consolidator) RunRecursive(ctx context.Context) (int, error) {
	if !c.mu.TryLock() {
		log.Printf("[recursive] RunRecursive skipped: consolidation already in progress")
		return 0, nil
	}
	defer c.mu.Unlock()

	total := 0
	for depth := 0; depth < MaxRecursiveDepth; depth++ {
		created, err := c.runRecursiveDepth(ctx, depth)
		if err != nil {
			return total, fmt.Errorf("recursive consolidation at depth %d: %w", depth, err)
		}
		total += created
		if created == 0 {
			break // no new engrams formed at this depth → stop
		}
		log.Printf("[recursive] depth %d → %d L%d engrams created", depth, created, depth+2)
	}

	log.Printf("[recursive] RunRecursive complete: %d higher-depth engrams created", total)
	return total, nil
}

// runRecursiveDepth consolidates ungrouped engrams at `depth` into engrams at `depth+1`.
// Returns the number of new engrams created.
func (c *Consolidator) runRecursiveDepth(ctx context.Context, depth int) (int, error) {
	engrams, err := c.graph.GetUngroupedEngrams(depth)
	if err != nil {
		return 0, fmt.Errorf("GetUngroupedEngrams(%d): %w", depth, err)
	}

	if len(engrams) < MinRecursiveGroupSize {
		log.Printf("[recursive] depth %d: only %d ungrouped engrams (need %d), skipping",
			depth, len(engrams), MinRecursiveGroupSize)
		return 0, nil
	}

	log.Printf("[recursive] depth %d: %d ungrouped engrams, inferring edges", depth, len(engrams))

	// Fetch C16 summaries for all engrams (used in edge inference prompts)
	ids := make([]string, len(engrams))
	for i, e := range engrams {
		ids[i] = e.ID
	}
	c16Map, _ := c.graph.GetEngramSummariesBatch(ids, graph.CompressionLevel16)

	// Build summaries map: ID → best available summary
	summaries := make(map[string]string, len(engrams))
	for _, en := range engrams {
		if s, ok := c16Map[en.ID]; ok && s != nil && s.Summary != "" {
			summaries[en.ID] = s.Summary
		} else {
			summaries[en.ID] = en.Summary
		}
	}

	// Infer engram-engram edges using sliding window
	edges, err := c.inferEngramEdges(ctx, engrams, summaries)
	if err != nil {
		log.Printf("[recursive] edge inference failed at depth %d: %v", depth, err)
		// Continue with no edges — clustering will produce singletons and exit
	}

	// Cluster by edges (connected components via DFS)
	groups := c.clusterEngramsByEdges(engrams, edges)

	created := 0
	for _, group := range groups {
		if len(group) < 2 {
			continue // skip singletons
		}
		if err := c.consolidateEngramGroup(ctx, group, depth+1); err != nil {
			log.Printf("[recursive] failed to consolidate engram group of %d at depth %d: %v",
				len(group), depth+1, err)
			continue
		}
		created++
	}

	return created, nil
}

// inferEngramEdges runs LLM edge inference over a sliding window of engrams.
// Uses the same sliding window approach as episode edge inference.
func (c *Consolidator) inferEngramEdges(ctx context.Context, engrams []*graph.Engram, summaries map[string]string) ([]EngramEdge, error) {
	if c.claude == nil {
		return nil, nil
	}

	batchSize := RecursiveBatchSize
	overlap := int(float64(batchSize) * RecursiveBatchOverlap)
	step := batchSize - overlap
	if step < 1 {
		step = 1
	}

	var allEdges []EngramEdge
	seen := make(map[string]bool)

	for start := 0; start < len(engrams); start += step {
		end := start + batchSize
		if end > len(engrams) {
			end = len(engrams)
		}
		batch := engrams[start:end]

		edges, err := c.claude.InferEngramEdges(ctx, batch, summaries)
		if err != nil {
			log.Printf("[recursive] InferEngramEdges window %d-%d failed: %v", start, end, err)
			continue
		}

		for _, e := range edges {
			key := e.FromID + "→" + e.ToID
			if !seen[key] {
				seen[key] = true
				allEdges = append(allEdges, e)
			}
		}
	}

	return allEdges, nil
}

// clusterEngramsByEdges uses DFS on high-confidence edges to find connected components.
// Returns groups of engrams (including singletons).
func (c *Consolidator) clusterEngramsByEdges(engrams []*graph.Engram, edges []EngramEdge) [][]*graph.Engram {
	// Build adjacency list
	adj := make(map[string][]string)
	for _, e := range edges {
		if e.Confidence >= 0.7 {
			adj[e.FromID] = append(adj[e.FromID], e.ToID)
			adj[e.ToID] = append(adj[e.ToID], e.FromID)
		}
	}

	engramMap := make(map[string]*graph.Engram, len(engrams))
	for _, en := range engrams {
		engramMap[en.ID] = en
	}

	visited := make(map[string]bool)
	var groups [][]*graph.Engram

	var dfs func(id string, group *[]*graph.Engram)
	dfs = func(id string, group *[]*graph.Engram) {
		if visited[id] {
			return
		}
		visited[id] = true
		if en, ok := engramMap[id]; ok {
			*group = append(*group, en)
		}
		for _, neighbor := range adj[id] {
			dfs(neighbor, group)
		}
	}

	for _, en := range engrams {
		if visited[en.ID] {
			continue
		}
		var group []*graph.Engram
		dfs(en.ID, &group)
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}

	return groups
}

// consolidateEngramGroup creates a single higher-depth engram from a group of source engrams.
func (c *Consolidator) consolidateEngramGroup(ctx context.Context, group []*graph.Engram, targetDepth int) error {
	// Collect summaries for the LLM prompt
	var parts []string
	for _, en := range group {
		if en.Summary != "" {
			parts = append(parts, en.Summary)
		}
	}

	// Generate summary via LLM
	var summary string
	var err error
	if c.llm != nil {
		prompt := buildEngramGroupPrompt(parts, targetDepth, c.BotName)
		summary, err = c.llm.Generate(prompt)
		if err != nil {
			summary = truncate(strings.Join(parts, " | "), 300)
		}
	} else {
		summary = truncate(strings.Join(parts, " | "), 300)
	}

	summary = strings.TrimPrefix(summary, "[Past] ")

	// Generate embedding
	var embedding []float64
	if c.llm != nil {
		embedding, _ = c.llm.Embed(summary)
	}
	if len(embedding) == 0 {
		embedding = calculateEngramCentroid(group)
	}

	// event_time = latest event_time among source engrams
	var eventTime time.Time
	for _, en := range group {
		if en.EventTime.After(eventTime) {
			eventTime = en.EventTime
		}
	}
	if eventTime.IsZero() {
		eventTime = time.Now()
	}

	engramID := graph.GenerateEngramID(summary, time.Now().UnixNano())

	en := &graph.Engram{
		ID:         engramID,
		Summary:    summary,
		Depth:      targetDepth,
		Topic:      "consolidated",
		EngramType: graph.EngramTypeKnowledge,
		Activation: 0.5,
		Strength:   len(group),
		Embedding:  embedding,
		EventTime:  eventTime,
		CreatedAt:  time.Now(),
	}

	if err := c.graph.AddEngram(en); err != nil {
		return fmt.Errorf("failed to add L%d engram: %w", targetDepth+1, err)
	}

	// Add CONSOLIDATED_FROM edges: parent → each source engram
	for _, src := range group {
		if err := c.graph.AddEngramRelation(engramID, src.ID, graph.EdgeConsolidatedFrom, 1.0); err != nil {
			log.Printf("[recursive] failed to add CONSOLIDATED_FROM edge %s→%s: %v",
				engramID[:8], src.ID[:8], err)
		}
	}

	// Generate pyramid summaries using C16 summaries of source engrams as context
	if c.llm != nil {
		if compressor, ok := c.llm.(graph.Compressor); ok {
			go func() {
				if err := c.graph.GenerateEngramPyramidFromEngrams(engramID, group, compressor, c.BotName); err != nil {
					log.Printf("[recursive] failed to generate pyramid for L%d engram %s: %v",
						targetDepth+1, engramID[:8], err)
				}
			}()
		} else {
			// Fall back to generating C8 only from the summary itself
			if err := c.graph.AddEngramSummary(engramID, graph.CompressionLevel8,
				truncate(summary, 80), len(summary)/4); err != nil {
				log.Printf("[recursive] failed to store C8 for %s: %v", engramID[:8], err)
			}
		}
	}

	return nil
}

// calculateEngramCentroid computes the centroid embedding from a slice of engrams.
func calculateEngramCentroid(engrams []*graph.Engram) []float64 {
	var dim int
	for _, en := range engrams {
		if len(en.Embedding) > 0 {
			dim = len(en.Embedding)
			break
		}
	}
	if dim == 0 {
		return nil
	}

	centroid := make([]float64, dim)
	count := 0
	for _, en := range engrams {
		if len(en.Embedding) == dim {
			for i, v := range en.Embedding {
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

// buildEngramGroupPrompt builds the LLM prompt for summarizing a group of engrams.
func buildEngramGroupPrompt(summaries []string, targetDepth int, botName string) string {
	levelName := fmt.Sprintf("L%d", targetDepth+1)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are creating a %s memory — a high-level synthesis of related memory clusters.

`, levelName))

	if botName != "" {
		sb.WriteString("Identity context: In the source summaries below, \"I\", \"me\", and \"my\" refer to " + botName + " (the AI assistant) — NOT to the user or owner.\n\n")
	}

	sb.WriteString("The following are summaries of related memory engrams that share a common theme:\n\n")

	for i, s := range summaries {
		sb.WriteString(fmt.Sprintf("[%d] %s\n\n", i+1, s))
	}

	sb.WriteString(`Synthesize these into a single concise summary (2-4 sentences) that:
- Captures the overarching theme or pattern that connects them
- Preserves the most important facts and decisions
- Preserves the voice and tense of the source summaries (if sources use first person or present tense, maintain that)
- Does NOT just list the individual summaries

Output ONLY the synthesized summary — no preamble, no labels, no word count.`)

	return sb.String()
}

// EngramEdge represents a relationship between two engrams (for recursive clustering).
type EngramEdge struct {
	FromID       string
	ToID         string
	Relationship string
	Confidence   float64
}

// InferEngramEdges analyzes a batch of engrams and infers relationships between them.
// Uses the same sliding window approach as InferEpisodeEdges but accepts engram summaries.
func (c *ClaudeInference) InferEngramEdges(ctx context.Context, engrams []*graph.Engram, summaries map[string]string) ([]EngramEdge, error) {
	if len(engrams) == 0 {
		return nil, nil
	}

	prompt := c.buildEngramInferencePrompt(engrams, summaries)

	if c.verbose {
		log.Printf("[claude-inference] Sending %d engrams to Claude for edge inference", len(engrams))
	}

	output, err := c.client.Generate(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("claude inference failed: %w", err)
	}

	var response struct {
		Edges []struct {
			FromID       string  `json:"from_id"`
			ToID         string  `json:"to_id"`
			Relationship string  `json:"relationship"`
			Confidence   float64 `json:"confidence"`
		} `json:"edges"`
	}

	extracted := extractJSON(output)
	if err := json.Unmarshal([]byte(extracted), &response); err != nil {
		if c.verbose {
			log.Printf("[claude-inference] Warning: failed to parse engram edge JSON, skipping batch")
		}
		return nil, nil
	}

	edges := make([]EngramEdge, 0, len(response.Edges))
	for _, e := range response.Edges {
		edges = append(edges, EngramEdge{
			FromID:       e.FromID,
			ToID:         e.ToID,
			Relationship: e.Relationship,
			Confidence:   e.Confidence,
		})
	}

	if c.verbose {
		log.Printf("[claude-inference] Inferred %d engram edges", len(edges))
	}

	return edges, nil
}

func (c *ClaudeInference) buildEngramInferencePrompt(engrams []*graph.Engram, summaries map[string]string) string {
	var sb strings.Builder

	sb.WriteString(`You are analyzing memory engrams to identify which ones share a common theme or topic cluster.

For each pair of related engrams, determine:
1. The semantic relationship (e.g., "same topic", "continues theme", "same project", "related decision")
2. Confidence score (0.0-1.0)

Only return relationships with confidence >= 0.7.

Engrams (16-word summaries):
`)

	for _, en := range engrams {
		s := summaries[en.ID]
		if s == "" {
			s = en.Summary
		}
		sb.WriteString(fmt.Sprintf("\nID: %s\n", en.ID))
		sb.WriteString(fmt.Sprintf("Summary: %s\n", s))
	}

	sb.WriteString(`
Return your analysis as JSON:

{
  "edges": [
    {
      "from_id": "engram-123",
      "to_id": "engram-456",
      "relationship": "same topic",
      "confidence": 0.85
    }
  ]
}

Link engrams that:
- Cover the same project, system, or domain
- Discuss the same recurring pattern or decision type
- Are part of the same logical thread of work or events

Use high confidence (0.8+) when engrams clearly belong to the same cluster.
Use medium confidence (0.6-0.7) for loose topical relationships.
Do NOT link engrams that merely mention the same person or tool by coincidence.
`)

	return sb.String()
}

