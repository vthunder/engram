---
topic: Schema Induction & Forward Matching
repo: engram
generated_at: 2026-04-06T02:30:00Z
commit: d4be284a
key_modules: [internal/schema, internal/consolidate, internal/graph]
score: 0.82
---

# Schema Induction & Forward Matching

> Repo: `engram` | Generated: 2026-04-06 | Commit: d4be284a

## Summary

Schema induction extracts recurring behavioral patterns ("schemas") from clusters of L2+ engrams — the consolidated memories that have already been abstracted once from raw episodes. Forward matching is the complementary runtime hook: whenever a new L1 engram is created, it is asynchronously matched against the current schema library to annotate it with any matching patterns and to flag anomalies that should trigger schema reconsolidation. Together they form Phase 2 of engram's fractal memory hierarchy, sitting above the consolidation pipeline and providing a cross-cutting template layer for understanding what _classes_ of events recur.

## Key Data Structures

### `Schema` (`internal/graph/types.go`)

The core schema record. `Content` is semi-structured prose with named sections:
- `PATTERN` — one-paragraph description of the recurring event class
- `GENERALIZATIONS` — bullet list of extracted cross-instance patterns

`Embedding` is computed from the `PATTERN` section _only_ (not the full content). `IsLabile` flags schemas that need reconsolidation — set when a forward match detects an anomalous instance.

```
ID        string    — BLAKE3 hex (via GenerateSchemaID)
Name      string    — short descriptive name from LLM output
Content   string    — PATTERN + GENERALIZATIONS sections
Embedding []float64 — cosine-search key; derived from PATTERN text only
IsLabile  bool      — true = reconsolidate at next InduceSchemas run
Instances []SchemaInstance — populated on retrieval only
```

### `SchemaInstance` (`internal/graph/types.go`)

Records that a specific engram matches a specific schema, with extracted slot values (e.g. `{"trigger": "...", "fix": "..."}`). Also stored denormalized in `schema_annotations` for fast `engram_id → schema_id` lookups.

```
SchemaID   string            — FK to schemas.id
EngramID   string            — FK to engrams.id
SlotValues map[string]string — JSON extracted by LLM slot-filling
IsAnomaly  bool              — true = unexpected instance, triggered labile marking
```

### `SchemaInductor` (`internal/schema/inductor.go`)

Batch induction runner. Key fields:
- `InductionThreshold float64` — default 0.82; cosine similarity above which an existing schema is considered a match (skip re-induction)
- `MinClusterSize int` — default 3; clusters smaller than this are skipped

### `ForwardMatcher` (`internal/schema/matcher.go`)

Async per-engram matcher. Key field:
- `MatchThreshold float64` — default 0.70; cosine similarity threshold to even attempt slot-filling

### `MatchResponse` (`internal/schema/inductor.go`)

LLM slot-filling response (parsed from JSON):
```
Matches   bool
Slots     map[string]string — filled schema slots
Anomalous bool              — true if instance doesn't fit the pattern well
Reason    string            — explanation from LLM
```

## Lifecycle

### Schema Induction (batch, ~every 6 hours)

1. **Fetch L2+ engrams**: `InduceSchemas` calls `graph.GetEngramsAtMinDepth(minDepth=2)`. Only L2+ engrams are used — L1 engrams are too granular to exhibit recurring patterns.

2. **Cluster by LLM**: `clusterByLLM` sends all engram summaries to the LLM in a single prompt asking for thematic grouping. Returns JSON cluster assignments with `cluster_id=-1` as the noise bucket (silently excluded). Falls back to `clusterByEmbedding` if the LLM response can't be parsed.

3. **Embedding fallback clustering**: `clusterByEmbedding` uses greedy single-linkage — each engram joins the first cluster whose centroid embedding is within 0.75 cosine similarity. If no embeddings exist, everything goes into one cluster.

4. **Process each cluster** (`processCluster`):
   - Skips clusters below `MinClusterSize` (3)
   - Computes centroid embedding for the cluster
   - Calls `graph.FindSimilarSchemas(centroid, InductionThreshold=0.82)` to find matching existing schemas
   - If a non-labile schema matches: skip (cluster already captured)
   - If a labile schema matches: `reconsolidateSchema` (re-induces with anomaly context)
   - If no match: `induceNewSchema`

5. **Induce new schema** (`induceNewSchema`):
   - Builds an induction prompt from cluster engram summaries
   - LLM can respond with `SKIP` if the cluster isn't a recurring pattern
   - Parses `PATTERN` and `GENERALIZATIONS` sections from the response
   - Embeds only the `PATTERN` text (via `extractPatternText`)
   - Calls `graph.AddSchema`, `graph.GenerateSchemaSummaries`, `graph.AddSchemaInstance` for each engram

6. **Reconsolidate labile schemas** (`reconsolidateSchema`):
   - Fetches anomalous instances that triggered the labile flag
   - Builds a reconsolidation prompt that includes both the current cluster _and_ the anomalous instances — so the LLM can update the schema's GENERALIZATIONS to accommodate what was previously unexpected
   - Calls `graph.UpdateSchemaContent` and regenerates summaries

7. **Post-induction deduplication**: `DeduplicateSchemas` (embedding-based, threshold 0.75) and optionally `DeduplicateSchemasWithLLM` (pre-filters at 0.60, then single batch LLM call) merge near-duplicate schemas. Duplicate resolution always keeps the older schema and reassigns instances.

### Forward Matching (async, per new L1 engram)

1. **Hook fires**: `Consolidator.consolidateGroup` (in `internal/consolidate/consolidate.go`) fires `NewEngramHook(engram)` asynchronously as a goroutine after each new L1 engram is created. The hook is wired in `cmd/engram/main.go` (set on the `Consolidator` struct) to avoid import cycles.

2. **Find candidate schemas**: `ForwardMatcher.MatchAndUpdate` calls `graph.FindSimilarSchemas(engram.Embedding, MatchThreshold=0.70)`. This is a full-scan cosine similarity search (no vec0 for schemas).

3. **Slot-filling per candidate**: For each candidate schema above threshold, `checkMatch` calls the LLM with a slot-filling prompt containing the schema content and the engram summary.

4. **Parse response**: `ParseMatchResponse` parses the JSON `MatchResponse`. If `Matches=false`, skip. If `Matches=true`:
   - `graph.AddSchemaInstance` records the match with slot values
   - If `Anomalous=true`: `graph.MakeSchemaLabile(schema.ID)` sets `is_labile=true` for next induction

5. **No blocking**: errors are logged but not returned — the hook runs in a goroutine and never blocks engram creation.

## Design Decisions

- **L2+ only for induction**: Schemas represent recurring _classes_ of events across multiple sessions. L1 engrams (single conversation clusters) are often too narrow; L2 engrams (which consolidate multiple L1 engrams) provide the cross-temporal signal needed for generalization.

- **Two thresholds, deliberately ordered**: `MatchThreshold` (0.70) < dedup threshold (0.75) < `InductionThreshold` (0.82). Match is looser because missing a valid instance is worse than a false positive (the LLM slot-fill step filters false positives). Induction skip threshold is tighter to prevent fragmenting one real schema into near-duplicates.

- **PATTERN section embedding only**: Full schema content includes GENERALIZATIONS which are verbose and may contain noisy phrases. The PATTERN paragraph is the canonical semantic fingerprint used for cosine search. `extractPatternText` specifically isolates this section.

- **Forward hook is async to avoid latency**: Schema matching requires an LLM call. Making it synchronous would add latency to every consolidation run. The hook fires in a goroutine; `consolidateGroup` does not wait for it.

- **Anomaly → labile → reconsolidation loop**: Rather than invalidating a schema immediately on anomaly, the labile flag defers reconsolidation to the next batch induction. This batches multiple anomalous instances into one reconsolidation call, avoiding churn if several anomalies arrive in quick succession.

- **`schema_annotations` denormalization**: Schema instances are stored in both `schema_instances` (normalized) and `schema_annotations` (engram_id → schema_id lookup). The annotations table exists purely for fast lookup during context assembly, where the question "which schemas annotate this engram?" is hot.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `internal/consolidate` | `internal/schema` | `NewEngramHook` callback fires `ForwardMatcher.MatchAndUpdate` after each new L1 engram |
| `internal/schema` | `internal/graph` | Schema/SchemaInstance CRUD (`AddSchema`, `AddSchemaInstance`, `GetSchemaIDsForEngram`, `FindSimilarSchemas`, `GetEngramsAtMinDepth`, `MakeSchemaLabile`) |
| `internal/schema.SchemaInductor` | LLM (`Generator`) | Induction prompts, reconsolidation prompts, deduplication batch prompt |
| `internal/schema.ForwardMatcher` | LLM (`Generator`) + Embedder | Per-engram slot-filling prompt and embedding for similarity search |
| `cmd/engram/main.go` | `internal/schema` | Wires `NewEngramHook` on `Consolidator`; schedules periodic `InduceSchemas` call |

## Non-Obvious Behaviors

- **`clusterByLLM` noise bucket is silent**: Engrams assigned `cluster_id=-1` are excluded from schema induction without logging. If the LLM groups most engrams as noise, induction will silently produce fewer schemas than expected.

- **Schema embedding ≠ schema content embedding**: `FindSimilarSchemas` searches by the `PATTERN`-derived embedding. If you update a schema's `Content` but not its `Embedding` (e.g., via a direct DB write), similarity search will use stale data. Only `UpdateSchemaContent` keeps them in sync.

- **`DeduplicateSchemasWithLLM` pre-filters at 0.60**, which is _lower_ than the induction skip threshold (0.82). This is intentional — the LLM dedup step is specifically designed to catch schemas that have similar meaning but different wording (e.g., "deployment" vs "release process"), so a wider cosine net is cast first.

- **Reconsolidation prompt includes anomalous instances explicitly**: `reconsolidateSchema` fetches the actual anomalous engrams (via `GetSchemaInstances` + anomaly filter) and passes them to the LLM with context about _why_ they were anomalous. This is different from just passing the new cluster — the prompt tells the LLM "these are the cases that didn't fit; update the schema to accommodate or explain them."

- **Schema summaries are extracted, not compressed**: `GenerateSchemaSummaries` uses `FormatSchemaSummary` which parses the `GENERALIZATIONS` section and builds level-specific summaries by truncating to word limits — no LLM call. This is unlike episode/engram pyramids which use an LLM compressor.

- **`Consolidator.NewEngramHook` is set via field injection to avoid import cycles**: `internal/consolidate` cannot import `internal/schema` (schema imports graph, graph is imported by consolidate — circular via the hook). The hook is wired as a `func(engram *graph.Engram)` field in `cmd/engram/main.go` after both packages are initialized.

## Start Here

- `internal/schema/inductor.go` — `SchemaInductor`, `InduceSchemas`, `processCluster`, `induceNewSchema`: the full batch induction pipeline
- `internal/schema/matcher.go` — `ForwardMatcher`, `MatchAndUpdate`: async per-engram matching
- `internal/graph/schemas.go` — all schema/instance CRUD, `FindSimilarSchemas`, `GenerateSchemaSummaries`
- `internal/graph/types.go:4231` — `Schema` and `SchemaInstance` struct definitions
- `cmd/engram/main.go` — where `NewEngramHook` is wired and the 6-hour induction schedule is set up
