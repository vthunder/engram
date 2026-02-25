// Package schema implements Phase 2 of fractal memory: schema formation.
// Schemas are cross-cutting pattern templates extracted from L2+ engrams.
// See: state/notes/fractal-memory-design.md
package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/vthunder/engram/internal/graph"
)

// Generator is any LLM that can generate text from a prompt.
type Generator interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// Embedder is any client that can embed text to a float64 vector.
type Embedder interface {
	Embed(text string) ([]float64, error)
}

// SchemaInductor extracts schemas from L2+ engrams.
type SchemaInductor struct {
	graph   *graph.DB
	llm     Generator
	embedder Embedder
	verbose bool

	// InductionThreshold: minimum cosine similarity for an existing schema to "match"
	// a new cluster (skip re-induction). Default: 0.82.
	InductionThreshold float64

	// MinClusterSize: minimum number of L2+ engrams required to induce a schema.
	// Default: 3 (from Phase 2 spec).
	MinClusterSize int
}

// NewSchemaInductor creates a new SchemaInductor.
func NewSchemaInductor(db *graph.DB, llm Generator, embedder Embedder, verbose bool) *SchemaInductor {
	return &SchemaInductor{
		graph:              db,
		llm:                llm,
		embedder:           embedder,
		verbose:            verbose,
		InductionThreshold: 0.82,
		MinClusterSize:     3,
	}
}

// InduceSchemas runs schema induction over all available L2+ engrams.
// Creates new schemas and reconsolidates labile ones.
// Returns the number of schemas created or updated.
func (si *SchemaInductor) InduceSchemas(ctx context.Context) (int, error) {
	// Fetch all L2+ engrams
	engrams, err := si.graph.GetEngramsAtMinDepth(1, 500)
	if err != nil {
		return 0, fmt.Errorf("fetching L2+ engrams: %w", err)
	}
	if len(engrams) < si.MinClusterSize {
		log.Printf("[schema-inductor] only %d L2+ engrams (need %d), skipping induction", len(engrams), si.MinClusterSize)
		return 0, nil
	}

	log.Printf("[schema-inductor] inducing schemas from %d L2+ engrams", len(engrams))

	// Cluster engrams by LLM thematic grouping, falling back to embedding similarity
	clusters, err := si.clusterByLLM(ctx, engrams)
	if err != nil {
		log.Printf("[schema-inductor] LLM clustering failed (%v), falling back to embedding", err)
		clusters = si.clusterByEmbedding(engrams, 0.65)
	}
	log.Printf("[schema-inductor] %d clusters formed", len(clusters))

	total := 0
	for i, cluster := range clusters {
		if len(cluster) < si.MinClusterSize {
			continue
		}

		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}

		created, err := si.processCluster(ctx, cluster, i)
		if err != nil {
			log.Printf("[schema-inductor] cluster %d failed: %v", i, err)
			continue
		}
		total += created
	}

	log.Printf("[schema-inductor] induction complete: %d schemas created/updated", total)

	// Post-induction deduplication: merge schemas that are near-duplicates.
	if mergeCount, err := si.deduplicateSchemas(ctx); err != nil {
		log.Printf("[schema-inductor] deduplication error: %v", err)
	} else if mergeCount > 0 {
		log.Printf("[schema-inductor] deduplication merged %d near-duplicate schemas", mergeCount)
	}

	return total, nil
}

// processCluster induces or updates a schema for a cluster of engrams.
func (si *SchemaInductor) processCluster(ctx context.Context, cluster []*graph.Engram, clusterIdx int) (int, error) {
	// Check if an existing schema already covers this cluster
	if len(cluster[0].Embedding) > 0 {
		centroid := si.centroidEmbedding(cluster)
		existing, err := si.graph.FindSimilarSchemas(centroid, si.InductionThreshold, 3)
		if err == nil && len(existing) > 0 {
			// Existing schema found. If labile, reconsolidate it. Otherwise skip.
			for _, s := range existing {
				if s.IsLabile {
					if si.verbose {
						log.Printf("[schema-inductor] reconsolidating labile schema %s", s.ID[:8])
					}
					return si.reconsolidateSchema(ctx, s, cluster)
				}
			}
			// Non-labile schema exists — skip this cluster
			if si.verbose {
				log.Printf("[schema-inductor] cluster %d: existing schema %s covers it, skipping", clusterIdx, existing[0].ID[:8])
			}
			return 0, nil
		}
	}

	// No existing schema — induce a new one
	return si.induceNewSchema(ctx, cluster)
}

// induceNewSchema runs the LLM prompt and creates a new schema from a cluster.
func (si *SchemaInductor) induceNewSchema(ctx context.Context, cluster []*graph.Engram) (int, error) {
	prompt := si.buildInductionPrompt(cluster)

	if si.verbose {
		log.Printf("[schema-inductor] inducing new schema from %d engrams", len(cluster))
	}

	raw, err := si.llm.Generate(ctx, prompt)
	if err != nil {
		return 0, fmt.Errorf("LLM induction failed: %w", err)
	}

	// Check for SKIP response (cluster is not a recurring pattern)
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToUpper(trimmed), "SKIP:") {
		log.Printf("[schema-inductor] cluster skipped (not a recurring pattern): %s", trimmed)
		return 0, nil
	}

	name, content, err := parseSchemaOutput(raw)
	if err != nil {
		log.Printf("[schema-inductor] failed to parse schema output: %v", err)
		return 0, nil
	}

	// Embed the PATTERN section (first paragraph after "PATTERN\n")
	patternText := extractPatternText(content)
	var embedding []float64
	if si.embedder != nil && patternText != "" {
		embedding, _ = si.embedder.Embed(patternText)
	}

	now := time.Now()
	schema := &graph.Schema{
		ID:        graph.GenerateSchemaID(name, now.UnixNano()),
		Name:      name,
		Content:   content,
		Embedding: embedding,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := si.graph.AddSchema(schema); err != nil {
		return 0, fmt.Errorf("storing schema: %w", err)
	}

	// Create instance records for each engram in the cluster
	for _, en := range cluster {
		inst := &graph.SchemaInstance{
			SchemaID:  schema.ID,
			EngramID:  en.ID,
			MatchedAt: now,
		}
		if err := si.graph.AddSchemaInstance(inst); err != nil {
			log.Printf("[schema-inductor] failed to add instance %s→%s: %v", schema.ID[:8], en.ID[:8], err)
		}
	}

	log.Printf("[schema-inductor] new schema %q (%s) from %d engrams", name, schema.ID[:8], len(cluster))
	return 1, nil
}

// reconsolidateSchema re-runs induction on a labile schema with fresh cluster data.
func (si *SchemaInductor) reconsolidateSchema(ctx context.Context, s *graph.Schema, cluster []*graph.Engram) (int, error) {
	// Fetch anomalous instances that triggered reconsolidation so the prompt
	// can explain what was unexpected rather than just passing raw cluster data.
	var anomalousEngrams []*graph.Engram
	if instances, err := si.graph.GetSchemaInstances(s.ID); err == nil {
		var anomalyIDs []string
		for _, inst := range instances {
			if inst.IsAnomaly {
				anomalyIDs = append(anomalyIDs, inst.EngramID)
			}
		}
		if len(anomalyIDs) > 0 {
			if engrams, err := si.graph.GetEngramsBatch(anomalyIDs); err == nil {
				for _, en := range engrams {
					anomalousEngrams = append(anomalousEngrams, en)
				}
			}
		}
	}

	prompt := si.buildReconsolidationPrompt(s, cluster, anomalousEngrams)

	raw, err := si.llm.Generate(ctx, prompt)
	if err != nil {
		return 0, fmt.Errorf("LLM reconsolidation failed: %w", err)
	}

	_, content, err := parseSchemaOutput(raw)
	if err != nil {
		log.Printf("[schema-inductor] failed to parse reconsolidation output: %v", err)
		return 0, nil
	}

	patternText := extractPatternText(content)
	var embedding []float64
	if si.embedder != nil && patternText != "" {
		embedding, _ = si.embedder.Embed(patternText)
	}

	if err := si.graph.UpdateSchemaContent(s.ID, content, embedding); err != nil {
		return 0, fmt.Errorf("updating schema %s: %w", s.ID, err)
	}

	log.Printf("[schema-inductor] reconsolidated schema %q (%s)", s.Name, s.ID[:8])
	return 1, nil
}

// deduplicateSchemas merges near-duplicate schemas (cosine sim >= 0.88).
// For each duplicate pair, instances are reassigned to the older schema and
// the newer schema is deleted. Returns the number of schemas merged away.
func (si *SchemaInductor) deduplicateSchemas(ctx context.Context) (int, error) {
	schemas, err := si.graph.ListSchemas()
	if err != nil {
		return 0, fmt.Errorf("listing schemas for dedup: %w", err)
	}

	const dedupThreshold = 0.88

	// Track which schemas have been deleted so we skip them in later iterations
	deleted := make(map[string]bool)
	merged := 0

	for i := 0; i < len(schemas); i++ {
		if deleted[schemas[i].ID] {
			continue
		}
		if len(schemas[i].Embedding) == 0 {
			continue
		}
		for j := i + 1; j < len(schemas); j++ {
			if deleted[schemas[j].ID] {
				continue
			}
			if len(schemas[j].Embedding) == 0 {
				continue
			}
			sim := cosineSim(schemas[i].Embedding, schemas[j].Embedding)
			if sim < dedupThreshold {
				continue
			}

			// Schemas i and j are near-duplicates. Keep i (older by index), merge j into i.
			keeper, dup := schemas[i], schemas[j]

			// Reassign all instances from dup → keeper
			instances, err := si.graph.GetSchemaInstances(dup.ID)
			if err != nil {
				log.Printf("[schema-inductor] dedup: failed to get instances for %s: %v", dup.ID[:8], err)
				continue
			}
			for _, inst := range instances {
				newInst := &graph.SchemaInstance{
					SchemaID:  keeper.ID,
					EngramID:  inst.EngramID,
					IsAnomaly: inst.IsAnomaly,
					MatchedAt: inst.MatchedAt,
				}
				if err := si.graph.AddSchemaInstance(newInst); err != nil {
					log.Printf("[schema-inductor] dedup: failed to reassign instance: %v", err)
				}
			}

			if err := si.graph.DeleteSchema(dup.ID); err != nil {
				log.Printf("[schema-inductor] dedup: failed to delete schema %s: %v", dup.ID[:8], err)
				continue
			}

			log.Printf("[schema-inductor] dedup: merged %q (%s) into %q (%s) (sim=%.3f)",
				dup.Name, dup.ID[:8], keeper.Name, keeper.ID[:8], sim)
			deleted[dup.ID] = true
			merged++
		}
	}

	return merged, nil
}

// clusterByLLM asks the LLM to group engrams into thematic clusters.
// The LLM receives all summaries and returns JSON cluster assignments.
// Falls back to embedding clustering if the LLM response can't be parsed.
func (si *SchemaInductor) clusterByLLM(ctx context.Context, engrams []*graph.Engram) ([][]*graph.Engram, error) {
	if len(engrams) == 0 {
		return nil, nil
	}

	minClusters := 2
	maxClusters := len(engrams)/3 + 1
	if maxClusters < minClusters {
		maxClusters = minClusters
	}

	var sb strings.Builder
	sb.WriteString("You are clustering memory engrams by recurring activity type for schema induction.\n\n")
	sb.WriteString("GOAL: Each cluster should represent one specific, recurring type of activity or event — not a broad subject area.\n\n")
	sb.WriteString("Good cluster examples: \"daily standup meeting\", \"debugging a production issue\", \"code review session\", \"1:1 with manager\"\n")
	sb.WriteString("Bad cluster examples: \"work\", \"engineering\", \"meetings\", \"problems\" (too broad/domain-based)\n\n")
	sb.WriteString("A good cluster:\n")
	sb.WriteString("- Groups engrams that describe the SAME TYPE of recurring event or workflow\n")
	sb.WriteString("- Has a label you could use to answer \"what kind of thing was this?\" in 2-4 words\n")
	sb.WriteString("- Is distinct enough that another cluster couldn't absorb it\n\n")
	sb.WriteString("ENGRAMS:\n")
	for i, en := range engrams {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i, en.Summary))
	}
	sb.WriteString(fmt.Sprintf(`
Rules:
- Create %d–%d clusters (aim for %d)
- Each engram belongs to exactly one cluster
- Group by what TYPE of activity happened, not what subject area it belongs to
- Each cluster must represent an activity that occurs REPEATEDLY over time (at least monthly), not a single project or incident
- If engrams don't share a clear recurring activity type, assign them cluster_id -1 (noise — they will be discarded)
- Do NOT force unrelated engrams into a cluster just to fill quota
- Do NOT create clusters that would produce overlapping schemas
- Clusters with fewer than %d engrams will be discarded — still assign them

Output JSON only, no other text:
[
  {"cluster_id": 0, "label": "short activity-type label", "indices": [0, 3, 7]},
  {"cluster_id": 1, "label": "short activity-type label", "indices": [1, 2, 5, 6]},
  {"cluster_id": -1, "label": "noise", "indices": [4, 8]}
]`, minClusters, maxClusters, (minClusters+maxClusters)/2, si.MinClusterSize))

	raw, err := si.llm.Generate(ctx, sb.String())
	if err != nil {
		return nil, fmt.Errorf("LLM clustering call failed: %w", err)
	}

	// Parse JSON (strip code fences if present)
	jsonStr := extractClusterJSON(raw)
	var clusterDefs []struct {
		ClusterID int    `json:"cluster_id"`
		Label     string `json:"label"`
		Indices   []int  `json:"indices"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &clusterDefs); err != nil {
		return nil, fmt.Errorf("failed to parse LLM cluster JSON: %w (raw: %.200s)", err, raw)
	}

	result := make([][]*graph.Engram, 0, len(clusterDefs))
	for _, cd := range clusterDefs {
		// cluster_id -1 is the noise bucket — skip it
		if cd.ClusterID < 0 {
			if si.verbose {
				log.Printf("[schema-inductor] discarding %d noise engrams", len(cd.Indices))
			}
			continue
		}
		cluster := make([]*graph.Engram, 0, len(cd.Indices))
		for _, idx := range cd.Indices {
			if idx >= 0 && idx < len(engrams) {
				cluster = append(cluster, engrams[idx])
			}
		}
		if len(cluster) > 0 {
			if si.verbose {
				log.Printf("[schema-inductor] LLM cluster %d %q: %d engrams", cd.ClusterID, cd.Label, len(cluster))
			}
			result = append(result, cluster)
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("LLM returned no valid clusters")
	}
	return result, nil
}

// extractClusterJSON extracts JSON array from LLM output, stripping code fences.
func extractClusterJSON(s string) string {
	s = strings.TrimSpace(s)
	// Strip ```json ... ``` or ``` ... ``` fences
	for _, fence := range []string{"```json", "```"} {
		if start := strings.Index(s, fence); start != -1 {
			inner := s[start+len(fence):]
			if end := strings.Index(inner, "```"); end != -1 {
				inner = strings.TrimSpace(inner[:end])
				// Skip language tag line
				if idx := strings.Index(inner, "\n"); idx != -1 {
					first := strings.TrimSpace(inner[:idx])
					if !strings.Contains(first, "[") && !strings.Contains(first, "{") {
						inner = strings.TrimSpace(inner[idx+1:])
					}
				}
				return inner
			}
		}
	}
	// Find the JSON array directly
	if start := strings.Index(s, "["); start != -1 {
		if end := strings.LastIndex(s, "]"); end > start {
			return s[start : end+1]
		}
	}
	return s
}

// clusterByEmbedding groups engrams into clusters using greedy single-linkage:
// each new engram joins the first cluster whose centroid is within threshold.
// Falls back to putting everything in one cluster if embeddings are missing.
func (si *SchemaInductor) clusterByEmbedding(engrams []*graph.Engram, threshold float64) [][]*graph.Engram {
	if len(engrams) == 0 {
		return nil
	}

	// If no embeddings, put everything in one cluster
	hasEmbedding := false
	for _, e := range engrams {
		if len(e.Embedding) > 0 {
			hasEmbedding = true
			break
		}
	}
	if !hasEmbedding {
		return [][]*graph.Engram{engrams}
	}

	type cluster struct {
		members  []*graph.Engram
		centroid []float64
	}

	var clusters []cluster

	for _, en := range engrams {
		if len(en.Embedding) == 0 {
			continue
		}
		placed := false
		for i := range clusters {
			if cosineSim(en.Embedding, clusters[i].centroid) >= threshold {
				clusters[i].members = append(clusters[i].members, en)
				clusters[i].centroid = centroidOf(clusters[i].members)
				placed = true
				break
			}
		}
		if !placed {
			clusters = append(clusters, cluster{
				members:  []*graph.Engram{en},
				centroid: en.Embedding,
			})
		}
	}

	result := make([][]*graph.Engram, len(clusters))
	for i, c := range clusters {
		result[i] = c.members
	}
	return result
}

func (si *SchemaInductor) centroidEmbedding(engrams []*graph.Engram) []float64 {
	var all []*graph.Engram
	for _, e := range engrams {
		if len(e.Embedding) > 0 {
			all = append(all, e)
		}
	}
	return centroidOf(all)
}

// --- Prompt builders ---

func (si *SchemaInductor) buildInductionPrompt(cluster []*graph.Engram) string {
	var sb strings.Builder
	sb.WriteString(`You are extracting a reusable schema from a cluster of memory engrams.

A schema is a compact pattern template capturing what is reliably true across a recurring type of activity.

FIRST: Assess whether this cluster is schema-worthy. Ask: "Is this a genuinely recurring activity type — something that happens repeatedly over time, not a single project or incident?"
- If NO (e.g., these engrams all describe the same one-time project or event), respond with exactly: SKIP: [one sentence explaining why]
- If YES, proceed to generate the schema below.

ENGRAMS IN CLUSTER:
`)
	for i, en := range cluster {
		sb.WriteString(fmt.Sprintf("\n[%d] %s\n", i+1, en.Summary))
	}

	sb.WriteString(`
Use this exact format. Strict length limits apply — cut anything that doesn't add new information.

SCHEMA: {2-5 word name for the activity type, e.g. "Production Incident Response"}

PATTERN
{Exactly 1-2 sentences. Name the specific recurring activity type. What makes an instance instantly recognizable? NO vague adjectives.}

TRIGGERS
{Exactly 2-3 bullet points. Concrete preconditions — not "a problem occurs" but what specific signal starts this activity.}

WHAT WORKS
{Exactly 2-3 bullet points. Specific actions or approaches with observable good outcomes. Max 15 words each.}

WHAT DOESN'T WORK
{1-2 bullet points, or omit if no evidence. Specific failure modes, not generic warnings.}

GENERALIZATIONS
{Exactly 3 numbered claims. Each must be falsifiable and ≤ 20 words. Bad: "Communication is important." Good: "Parallel debugging cuts resolution time vs sequential hypothesis testing."}

OPEN QUESTIONS
{Exactly 1-2 questions that would most sharpen or refute this schema if answered.}

Quality rules:
- Every bullet point must be specific to THIS activity type, not generic advice
- If a claim would apply to any knowledge work, delete it
- Prefer concrete nouns and verbs; ban "effectively", "properly", "important", "relevant"

Output only the schema text. Start with "SCHEMA: ".`)

	return sb.String()
}

func (si *SchemaInductor) buildReconsolidationPrompt(s *graph.Schema, cluster []*graph.Engram, anomalies []*graph.Engram) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are updating an existing memory schema.

EXISTING SCHEMA:
%s

`, s.Content))

	if len(anomalies) > 0 {
		sb.WriteString("ANOMALOUS INSTANCES (matched the schema but behaved unexpectedly — these triggered this update):\n")
		for i, en := range anomalies {
			sb.WriteString(fmt.Sprintf("[anomaly %d] %s\n", i+1, en.Summary))
		}
		sb.WriteString("\n")
	}

	if len(cluster) > 0 {
		sb.WriteString("RECENT INSTANCES:\n")
		for i, en := range cluster {
			sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, en.Summary))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`Update the schema based on the evidence above. The schema uses these sections:
TRIGGERS: What activates or initiates this pattern
WHAT WORKS: Reliable successful approaches
WHAT DOESN'T WORK: Known failures, pitfalls, edge cases
GENERALIZATIONS: Distilled lessons that hold broadly
OPEN QUESTIONS: Unresolved uncertainties to investigate

If anomalous instances reveal edge cases or failure modes, incorporate them into WHAT DOESN'T WORK or OPEN QUESTIONS. If anomalies are common enough to be their own pattern, expand WHAT WORKS / WHAT DOESN'T WORK accordingly. Do NOT list individual anomalies verbatim — synthesize what they tell you about the pattern's limits.

Output the complete updated schema. Start with "SCHEMA: ".`)

	return sb.String()
}

// --- Schema output parsing ---

// parseSchemaOutput extracts name and content from LLM output.
// Returns ("", "", err) if the output is too malformed to use.
func parseSchemaOutput(raw string) (name, content string, err error) {
	raw = strings.TrimSpace(raw)

	// Strip markdown code fences if present
	if strings.HasPrefix(raw, "```") {
		if end := strings.Index(raw[3:], "```"); end != -1 {
			raw = strings.TrimSpace(raw[3 : 3+end])
			// Skip language tag line if present
			if idx := strings.Index(raw, "\n"); idx != -1 {
				firstLine := strings.TrimSpace(raw[:idx])
				if !strings.Contains(firstLine, " ") {
					raw = strings.TrimSpace(raw[idx+1:])
				}
			}
		}
	}

	if !strings.HasPrefix(raw, "SCHEMA:") {
		return "", "", fmt.Errorf("output does not start with SCHEMA:")
	}

	firstLine := strings.SplitN(raw, "\n", 2)[0]
	name = strings.TrimSpace(strings.TrimPrefix(firstLine, "SCHEMA:"))
	if name == "" {
		return "", "", fmt.Errorf("empty schema name")
	}

	// Remove "ID: (leave blank...)" line if present
	lines := strings.Split(raw, "\n")
	var filtered []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "ID:") {
			continue
		}
		filtered = append(filtered, line)
	}
	content = strings.Join(filtered, "\n")
	content = strings.TrimSpace(content)

	return name, content, nil
}

// extractPatternText returns the text under the PATTERN section header.
func extractPatternText(content string) string {
	lines := strings.Split(content, "\n")
	inPattern := false
	var patternLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "PATTERN" {
			inPattern = true
			continue
		}
		if inPattern {
			// Stop at next all-caps header (blank line tolerance)
			if trimmed != "" && isHeader(trimmed) {
				break
			}
			patternLines = append(patternLines, line)
		}
	}
	return strings.TrimSpace(strings.Join(patternLines, "\n"))
}

// isHeader returns true if a line looks like a schema section header (all caps, no sentence punctuation).
func isHeader(s string) bool {
	if len(s) < 3 {
		return false
	}
	upper := strings.ToUpper(s)
	return s == upper && !strings.ContainsAny(s, ".,;:?!")
}

// --- Embedding helpers ---

func cosineSim(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrt64(normA) * sqrt64(normB))
}

func centroidOf(engrams []*graph.Engram) []float64 {
	if len(engrams) == 0 {
		return nil
	}
	dim := len(engrams[0].Embedding)
	if dim == 0 {
		return nil
	}
	centroid := make([]float64, dim)
	count := 0
	for _, e := range engrams {
		if len(e.Embedding) != dim {
			continue
		}
		for i, v := range e.Embedding {
			centroid[i] += v
		}
		count++
	}
	if count == 0 {
		return nil
	}
	for i := range centroid {
		centroid[i] /= float64(count)
	}
	return centroid
}

// ShouldRun returns true if schema induction should run:
// - there are enough L2+ engrams, OR
// - it has been long enough since the last induction.
func (si *SchemaInductor) ShouldRun() (bool, error) {
	engrams, err := si.graph.GetEngramsAtMinDepth(1, si.MinClusterSize)
	if err != nil {
		return false, err
	}
	return len(engrams) >= si.MinClusterSize, nil
}

// ParseInductionResponse parses a structured JSON response from a schema matching call.
// Used by the ForwardMatcher; defined here to keep LLM response parsing in one file.
type MatchResponse struct {
	Matches   bool              `json:"matches"`
	Slots     map[string]string `json:"slots,omitempty"`
	Anomalous bool              `json:"anomalous"`
	Reason    string            `json:"reason,omitempty"`
}

func ParseMatchResponse(raw string) (*MatchResponse, error) {
	raw = strings.TrimSpace(raw)
	// Strip code fences
	if start := strings.Index(raw, "```json"); start != -1 {
		start += 7
		if end := strings.Index(raw[start:], "```"); end != -1 {
			raw = strings.TrimSpace(raw[start : start+end])
		}
	} else if start := strings.Index(raw, "```"); start != -1 {
		start += 3
		if end := strings.Index(raw[start:], "```"); end != -1 {
			raw = strings.TrimSpace(raw[start : start+end])
		}
	}

	var resp MatchResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse match response: %w", err)
	}
	return &resp, nil
}

// sqrt64 is a simple square root for float64 (avoids importing math).
func sqrt64(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// Newton's method
	z := x / 2
	for i := 0; i < 30; i++ {
		z -= (z*z - x) / (2 * z)
		if z*z-x < 1e-15 && x-z*z < 1e-15 {
			break
		}
	}
	return z
}
