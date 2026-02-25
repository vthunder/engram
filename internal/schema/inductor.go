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
	prompt := si.buildReconsolidationPrompt(s, cluster)

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

// clusterByLLM asks the LLM to group engrams into thematic clusters.
// The LLM receives all summaries and returns JSON cluster assignments.
// Falls back to embedding clustering if the LLM response can't be parsed.
func (si *SchemaInductor) clusterByLLM(ctx context.Context, engrams []*graph.Engram) ([][]*graph.Engram, error) {
	if len(engrams) == 0 {
		return nil, nil
	}

	var sb strings.Builder
	sb.WriteString("You are grouping memory engrams into thematic clusters for schema induction.\n\n")
	sb.WriteString("Group the numbered engrams below by recurring thematic domain — what type of work or pattern does each represent?\n\n")
	sb.WriteString("ENGRAMS:\n")
	for i, en := range engrams {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i, en.Summary))
	}
	sb.WriteString(fmt.Sprintf(`
Rules:
- Create at least 2 clusters (do not put everything in one cluster)
- Each engram belongs to exactly one cluster
- Cluster by domain or theme, not surface wording
- Clusters with fewer than %d engrams will be discarded — still assign them

Output JSON only, no other text:
[
  {"cluster_id": 0, "label": "short label", "indices": [0, 3, 7]},
  {"cluster_id": 1, "label": "short label", "indices": [1, 2, 5, 6]}
]`, si.MinClusterSize))

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
	sb.WriteString(`You are analyzing a cluster of memory engrams to extract a reusable schema — a pattern template representing what class of event these engrams share.

A schema is NOT a summary of what happened. It is a template: what makes instances of this pattern recognizable? What generalizations hold across all instances?

ENGRAMS IN CLUSTER:
`)
	for i, en := range cluster {
		sb.WriteString(fmt.Sprintf("\n[%d] %s\n", i+1, en.Summary))
	}

	sb.WriteString(`
OUTPUT FORMAT (required sections marked with *):

SCHEMA: *{concise name, 2-5 words}*
ID: (leave blank — will be assigned)

PATTERN
*[1-3 sentence description of the recurring pattern. What makes instances recognizable?]*

[Domain-appropriate sections — you decide the headers based on content]
[Examples: TRIGGER / DIAGNOSIS / FIX / VALIDATION for bug patterns]
[Or: CONTEXT / DYNAMIC / WHAT WORKS / WHAT FAILS for social patterns]
[Or: DECISION / CRITERIA / OUTCOME / RETROSPECTIVE for decisions]

GENERALIZATIONS
*[Numbered list of things true across all instances — the insight layer]*

OPEN QUESTIONS
[What data would refine or refute this schema? What's still uncertain?]

ANOMALIES
[Any instances that matched but behaved unexpectedly]

Output only the schema text. Start directly with "SCHEMA: ".`)

	return sb.String()
}

func (si *SchemaInductor) buildReconsolidationPrompt(s *graph.Schema, newInstances []*graph.Engram) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are updating an existing memory schema with new instances.

EXISTING SCHEMA:
%s

NEW INSTANCES TO INCORPORATE:
`, s.Content))

	for i, en := range newInstances {
		sb.WriteString(fmt.Sprintf("\n[%d] %s\n", i+1, en.Summary))
	}

	sb.WriteString(`
Update the schema to incorporate insights from the new instances. Preserve the existing structure but refine GENERALIZATIONS and add ANOMALIES if warranted.

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
