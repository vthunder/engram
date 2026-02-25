package schema

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/vthunder/engram/internal/graph"
)

// ForwardMatcher runs async schema matching when new L1 engrams are created.
// It embeds each new engram and checks it against existing schemas; if a match
// is found, it creates a schema_instances row (and flags anomalies).
type ForwardMatcher struct {
	graph   *graph.DB
	llm     Generator
	embedder Embedder
	verbose bool

	// MatchThreshold is the minimum embedding similarity to try slot-filling. Default: 0.70.
	MatchThreshold float64
}

// NewForwardMatcher creates a new ForwardMatcher.
func NewForwardMatcher(db *graph.DB, llm Generator, embedder Embedder, verbose bool) *ForwardMatcher {
	return &ForwardMatcher{
		graph:          db,
		llm:            llm,
		embedder:       embedder,
		verbose:        verbose,
		MatchThreshold: 0.70,
	}
}

// MatchAndUpdate checks a newly created engram against all schemas and records any matches.
// Designed to run asynchronously — errors are logged, not returned.
func (fm *ForwardMatcher) MatchAndUpdate(ctx context.Context, engram *graph.Engram) {
	if len(engram.Embedding) == 0 {
		return
	}

	// Find schemas with embedding similarity above threshold
	candidates, err := fm.graph.FindSimilarSchemas(engram.Embedding, fm.MatchThreshold, 5)
	if err != nil {
		log.Printf("[forward-matcher] FindSimilarSchemas failed: %v", err)
		return
	}
	if len(candidates) == 0 {
		return
	}

	if fm.verbose {
		log.Printf("[forward-matcher] engram %s: %d schema candidates", engram.ID[:8], len(candidates))
	}

	for _, s := range candidates {
		select {
		case <-ctx.Done():
			return
		default:
		}

		resp, err := fm.checkMatch(ctx, s, engram)
		if err != nil {
			log.Printf("[forward-matcher] slot-fill failed for schema %s: %v", s.ID[:8], err)
			continue
		}
		if !resp.Matches {
			continue
		}

		inst := &graph.SchemaInstance{
			SchemaID:  s.ID,
			EngramID:  engram.ID,
			SlotValues: resp.Slots,
			IsAnomaly: resp.Anomalous,
			MatchedAt: time.Now(),
		}
		if err := fm.graph.AddSchemaInstance(inst); err != nil {
			log.Printf("[forward-matcher] failed to record instance: %v", err)
			continue
		}

		if resp.Anomalous {
			log.Printf("[forward-matcher] anomaly detected: engram %s in schema %q", engram.ID[:8], s.Name)
			fm.graph.MakeSchemaLabile(s.ID)
		} else if fm.verbose {
			log.Printf("[forward-matcher] engram %s matched schema %q", engram.ID[:8], s.Name)
		}
	}
}

// checkMatch runs the slot-filling prompt and returns the match response.
func (fm *ForwardMatcher) checkMatch(ctx context.Context, s *graph.Schema, engram *graph.Engram) (*MatchResponse, error) {
	prompt := fm.buildSlotFillPrompt(s, engram)
	raw, err := fm.llm.Generate(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM slot-fill failed: %w", err)
	}
	return ParseMatchResponse(raw)
}

// buildSlotFillPrompt builds the schema matching prompt for a single engram.
func (fm *ForwardMatcher) buildSlotFillPrompt(s *graph.Schema, engram *graph.Engram) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are checking whether a memory engram matches an existing schema.

SCHEMA:
%s

NEW ENGRAM:
%s

TASK: Does this engram match the schema pattern? If yes, extract slot values for any named sections
in the schema (e.g., TRIGGER, DIAGNOSIS, FIX, CONTEXT, etc.). If the engram is anomalous — it
matches the schema but behaves unexpectedly (missing a required step, extreme values, surprising outcome) — set anomalous=true.

Prefer NOT matching over incorrect matching. If uncertain, set matches=false.

OUTPUT FORMAT (JSON only, no explanation):
{
  "matches": true|false,
  "slots": {"section_name": "extracted value", ...},
  "anomalous": true|false,
  "reason": "brief explanation"
}
`, s.Content, engram.Summary))
	return sb.String()
}
