---
topic: Memory Consolidation Pipeline
repo: engram
generated_at: 2026-04-06T09:49:57Z
commit: dd3c9c29
key_modules: [internal/consolidate, internal/graph]
score: 0.96
---

# Memory Consolidation Pipeline

> Repo: `engram` | Generated: 2026-04-06 | Commit: dd3c9c29

## Summary

The consolidation pipeline converts raw episodes (individual observations) into durable engrams (consolidated memories) by clustering semantically related episodes via LLM inference, then summarizing each cluster into a single engram. It runs in the background every 15 minutes and includes a recursive pass that aggregates L1 engrams into higher-depth L2/L3 summaries.

## Key Data Structures

### `Consolidator` (`internal/consolidate/consolidate.go`)
The top-level orchestrator. Holds a `*graph.DB`, an `LLMClient` (embedding + generation), and a `*ClaudeInference` (relationship inference). Key config fields: `MinGroupSize` (default 1), `MaxGroupSize` (default 10), `episodeBatchSize` (default 20), `episodeBatchOverlap` (default 0.5). `IncrementalMode` skips LLM inference for batches where all episode-episode edges already exist. `NewEngramHook func(engram *graph.Engram)` is fired asynchronously after each L1 engram is created — wired to the schema forward-matcher by `cmd/engram/main.go`.

### `episodeGroup` (`internal/consolidate/consolidate.go`)
Internal grouping unit: a slice of `*graph.Episode`, a union set of `entityIDs`, and an optional `priorContext` string (the previous sub-group's raw content or a prior engram summary). Passed to `consolidateGroup` for LLM summarization.

### `Engram` (`internal/graph/types.go`)
The persisted output of consolidation. Key fields:
- `ID` — 32-char BLAKE3 hex of `summary + createdAt`
- `Depth` — 0 = L1 (from episodes), 1 = L2 (from L1 engrams), etc.
- `EngramType` — `"knowledge"` or `"operational"` (affects decay rate)
- `Activation` — starts at 0.5; decays hourly, boosted on retrieval
- `Strength` — set to `len(group.episodes)` at creation
- `LabileUntil` — 24h after creation; within this window, new related episodes extend the engram via reconsolidation
- `EventTime` — `MAX(timestamp_event)` of source episodes (when events occurred, not when consolidated)

### `EpisodeEdge` / `EngramEdge` (`internal/consolidate/claude_inference.go`, `recursive.go`)
LLM-inferred relationship between two nodes. Fields: `FromID`, `ToID`, `Relationship` (string label), `Confidence` (float64). Only edges with `Confidence >= 0.7` are used for clustering.

### `ClaudeInference` (`internal/consolidate/claude_inference.go`)
Wraps a `Generator` interface (Anthropic API, `claude` CLI, or Ollama). Provides `InferEpisodeEdges` and `InferEngramEdges` — both take batches of nodes and return typed edges via JSON-structured LLM prompts. Also provides `InferEngramRelationship` for one-to-one engram-episode relationship checks.

### `LLMClient` interface (`internal/consolidate/consolidate.go`)
Two methods: `Embed(text string) ([]float64, error)` and `Generate(prompt string) (string, error)`. Distinct from `Generator` (generate-only). Used for embedding centroids and LLM summarization prompts.

## Lifecycle

### Phase 0 — Duplicate detection
`detectDuplicateEpisodes` compares all pairs of unconsolidated episodes by cosine similarity on their embeddings. Pairs above 0.95 receive a synthetic edge with `Confidence: 1.0`. If both have C16 summaries, those are also compared — embedding-similar but content-different pairs are skipped. These synthetic edges are merged with LLM-inferred edges before clustering.

### Phase 1 — Episode-episode edge inference
`Run()` fetches unconsolidated episodes per channel (up to 500 per iteration). It then either:
- **Loads existing edges** via `loadExistingEdges` if they already exist (`IncrementalMode` or non-incremental with prior data). Critically, only `inferred_by_llm=1` edges qualify — structural `REPLIES_TO` edges created at ingestion are excluded (migration v23 added the `inferred_by_llm` flag to prevent these from permanently blocking re-inference).
- **Calls `inferEpisodeEpisodeLinks`** if no LLM edges exist. This uses a sliding window of size 20 with 50% overlap (`episodeBatchOverlap = 0.5`), sorted by timestamp. Each window is sent to `ClaudeInference.InferEpisodeEdges` as a batch; the 50% overlap ensures episodes near window boundaries appear in two windows, catching cross-boundary relationships.

Inferred edges are deduplicated (same `fromID + toID + relationship` key), merged with duplicate-detection edges, and persisted to the DB.

### Phase 2 — Connected-component clustering
`clusterEpisodesByEdges` builds an adjacency list from high-confidence edges (≥ 0.7) and runs DFS to find connected components. It also does a cross-batch lookup: if an episode in the current batch is connected (via edges) to an already-consolidated episode, the cluster is assigned to that episode's engram for reconsolidation. Returns: `[]*episodeGroup` (new groups to consolidate) and `map[string][]*graph.Episode` (existing engram IDs → new episodes to add).

### Phase 3a — Labile routing
For each existing engram with new episodes:
- If `now < LabileUntil` (labile): new episodes are appended to the engram's group and `reconsolidateEngram` is called.
- If labile window expired (non-labile): new episodes form a fresh `episodeGroup` with the prior engram's summary injected as `priorContext` (so the LLM can reference what came before without re-summarizing it).

Large groups (> `MaxGroupSize`) are split by `splitEpisodeGroup` in timestamp order. Each sub-group except the first receives the previous sub-group's formatted episodes as `priorContext`. All entity IDs from the parent group propagate to every sub-group.

### Phase 3b — Engram creation
`consolidateGroup` handles each new group:
1. **Filter ephemeral content**: `isEphemeralContent` checks for meeting countdown messages and "starting in N minutes" patterns → group is discarded.
2. **Filter low-info content**: `isAllLowInfo` checks if every episode is a backchannel/greeting → group is linked to the `_ephemeral` sentinel engram so episodes aren't retried, but no real engram is created.
3. **LLM summarization**: `buildConsolidationPrompt` assembles role-labeled episode fragments, optional `entityContext` (prior engrams for the same entities, capped at 8), and `priorContext`. The LLM generates a first-person memory summary.
4. **Engram write**: BLAKE3 ID from `summary + createdAt`, embedding = centroid of source episode embeddings, `classifyEngramType` assigns `"knowledge"` or `"operational"`, `Strength = len(episodes)`, `LabileUntil = now + 24h`.
5. **Link to sources**: each source episode and entity gets a junction row. Entity pyramids are regenerated asynchronously. Episodes without a matching entity are silently skipped.
6. **SIMILAR_TO edges**: `linkToSimilarEngrams` finds existing engrams with cosine similarity ≥ 0.85 and creates `SIMILAR_TO` edges.
7. **Pyramid**: only C8 is generated inline; the full L64→L4 pyramid is deferred to the background `compress-traces` process.
8. **Hook**: `NewEngramHook` is called in a goroutine for forward schema matching.

### Phase 3c — Cross-reference linking
`linkEpisodesToRelatedEngrams` creates `episode_engram_edges` for each episode that is semantically similar (≥ 0.80) to existing engrams it doesn't already belong to. This is a lower threshold than `SIMILAR_TO` (0.85) and captures weaker cross-references between individual observations and historical memories.

### Phase 4 — Batch reconsolidation
Labile engrams that received new episodes (from Phase 3a) are reconsolidated. `reconsolidateEngram` re-fetches all source episodes, regenerates the LLM summary, recalculates the centroid embedding, reclassifies the type, and updates `event_time`. C8 is regenerated; the reconsolidation flag is cleared.

### No-progress guard
If the same set of episode IDs appears in successive `Run()` iterations (clustering made no progress), the loop terminates to prevent an infinite loop. Each iteration processes at most 500 episodes.

### Recursive consolidation
`RunRecursive` (called separately, triggered when ≥N new L1 engrams exist or hourly):
1. `runRecursiveDepth(depth=0)` fetches all depth-0 engrams not yet assigned to a depth-1 parent.
2. Fetches C16 summaries (or falls back to the full summary) and infers engram-engram edges via `ClaudeInference.InferEngramEdges` using a sliding window of 15 with 50% overlap.
3. Clusters by DFS on high-confidence edges; singletons are skipped.
4. `consolidateEngramGroup` generates a new engram at `depth+1` with `CONSOLIDATED_FROM` edges pointing to the sources, plus a compressed pyramid (`GenerateEngramPyramidFromEngrams`).
5. Repeats at depth 1, 2, … until no multi-node clusters form (max depth: 5).

## Design Decisions

- **Sliding window O(kn) inference**: Claude edge inference is O(n²) if run on all pairs; the sliding window with 50% overlap approximates O(kn) where k is the window size, at the cost of potentially missing relationships between temporally distant episodes. The 50% overlap is the key correctness mechanism — it ensures every episode shares a window with its immediate neighbors in both directions.

- **Confidence threshold 0.7 for clustering**: Low-confidence edges are never used to merge episodes. This prevents LLM hallucinations from creating false clusters. The duplicate-detection synthetic edges use `Confidence: 1.0` to override this filter for near-identical content.

- **`_ephemeral` sentinel**: Rather than deleting or ignoring low-info episodes, they are linked to a sentinel engram with a reserved ID. This marks them as "processed" so `GetUnconsolidatedEpisodes` skips them on future runs, preserving idempotency without polluting the engram table.

- **`inferred_by_llm` flag on episode edges**: Structural `REPLIES_TO` edges (created at ingestion time from Discord reply chains) must not block LLM inference — a reply relationship is structural, not semantic. Migration v23 added this flag so `loadExistingEdges` only returns LLM-inferred edges, preventing structural edges from permanently suppressing the inference pass.

- **Dual `LLMClient` / `Generator` interfaces**: `LLMClient` is used for consolidation (needs both `Embed` and `Generate`); `ClaudeInference` uses a narrower `Generator` interface (generate-only). This lets both Anthropic and `claude-code` CLI serve as inference backends without needing embedding support.

- **`priorContext` chaining in splits**: When a group exceeds `MaxGroupSize` and is split, each sub-group's `priorContext` is the formatted episode list of the previous sub-group (not the LLM summary). This gives the LLM raw continuity rather than a compressed relay, at the cost of more tokens per prompt.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `internal/consolidate` | `internal/graph` | All DB reads/writes via `graph.DB` (episodes, engrams, edges, entities, pyramids) |
| `internal/consolidate` | `internal/filter` | `isEphemeralContent` and `isAllLowInfo` filter calls to suppress low-signal groups |
| `cmd/engram/main.go` | `internal/consolidate` | Wires `NewEngramHook` to `internal/schema` forward-matcher; schedules `Run()` and `RunRecursive()` |
| `internal/consolidate` | Anthropic/ClaudeCode/Ollama | LLM calls for edge inference (`ClaudeInference`) and summarization (`LLMClient.Generate`) |
| `internal/schema` | `internal/consolidate` | Receives `NewEngramHook` callback; matches each new L1 engram against existing schemas asynchronously |
| `internal/graph` | `internal/embed` | Embedding dimension and vector storage in `memory-vectors.db` (sqlite-vec KNN) |

## Non-Obvious Behaviors

- **Labile window gates reconsolidation, not creation**: A new related episode that arrives after the 24h `LabileUntil` does NOT extend the original engram — it creates a new engram with the original summary injected as `priorContext`. This means a conversation resumed the next day produces a distinct engram that references the prior one, not a merged one.

- **`ShouldRun` has two independent triggers**: Consolidation fires if a channel has been idle for `idleTime` OR if it has ≥ `maxBuffer` unconsolidated episodes — whichever comes first. These are checked per-channel, so a high-volume channel consolidates more aggressively than a quiet one.

- **Only C8 is generated inline**: `consolidateGroup` calls `generateC8Summary` synchronously, giving the engram an 8-word headline immediately. The full L64→L4 pyramid is generated by a background worker (`EpisodeCompressQueue` / `compress-traces`). Retrieval at higher precision levels will miss detail until the backfill completes.

- **Entity context is capped asymmetrically**: `buildEntityContext` fetches at most 2 prior engrams per entity and 8 total. This biases context toward the most recently accessed engrams for the most frequently mentioned entities, not the most semantically relevant ones.

- **`NewEngramHook` is not awaited**: Schema forward-matching fires in a goroutine with no error propagation back to the consolidation caller. Schema annotation failures are silent — schema_annotations may lag behind engram creation.

- **Cross-batch reconsolidation works via edge lookup, not episode state**: If episode A (in a previous batch, now consolidated) has an edge to episode B (in the current batch), `clusterEpisodesByEdges` finds A's engram via a cross-batch lookup and routes B to that engram for reconsolidation. This relies on the episode-episode edge table being populated for A even though A is already consolidated — the edge table is the join point, not the episode's consolidation state.

## Start Here

- `internal/consolidate/consolidate.go` — the `Run()` function (line 644) is the authoritative pipeline entry point; read `consolidateGroup` next for the per-group creation logic
- `internal/consolidate/recursive.go` — `RunRecursive` and `runRecursiveDepth` for the L2/L3 hierarchy; constants at the top define the sliding window parameters
- `internal/consolidate/claude_inference.go` — `InferEpisodeEdges` and `buildEpisodeInferencePrompt` for how LLM inference actually works and what the JSON contract looks like
- `internal/graph/types.go` — `Engram`, `Episode`, `EpisodeEdge`, `EdgeType` constants; the canonical data model
- `cmd/engram/main.go` — wiring: how `NewEngramHook`, scheduling, and LLM provider selection are assembled at startup
