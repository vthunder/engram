---
topic: Pyramid Compression
repo: engram
generated_at: 2026-04-06T12:51:32Z
commit: 5c62c687
key_modules: [internal/graph, internal/consolidate]
score: 0.78
---

# Pyramid Compression

> Repo: `engram` | Generated: 2026-04-06 | Commit: 5c62c687

## Summary

Pyramid Compression is Engram's multi-level summarization subsystem that pre-computes word-count-bounded summaries (4, 8, 16, 32, and 64 words) for episodes, engrams, and entities, storing them in a dedicated cache database. It lets callers request a specific compression level at retrieval time to control token budget without re-invoking the LLM on the hot path.

## Key Data Structures

### `EpisodeSummary` (`internal/graph/compression.go`)
Stores one compressed version of an episode. `CompressionLevel` is the target word count integer (4, 8, 16, 32, 64) — level 0 means verbatim. Inserted into `cache.episode_summaries` via `AddEpisodeSummary`.

### `EngramSummary` (`internal/graph/compression.go`)
Same shape as `EpisodeSummary` but for engrams (`cache.engram_summaries`). Used extensively during retrieval — Phase 1 scoring in `Retrieve` loads L8 summaries for all activation candidates before fetching full detail for the shortlisted top-N.

### `EntitySummary` (`internal/graph/compression.go`)
Same shape, stored in `cache.entity_summaries`. Generated from assembled entity metadata (name, type, aliases, relations) rather than from LLM inference over raw content.

### `EpisodeCompressQueue` (`internal/graph/compress_queue.go`)
A channel-based work queue (capacity 256) that serializes episode pyramid generation — at most one episode is compressed at a time. Holds a `needsScan atomic.Bool` that is set when the channel overflows or on startup, triggering a periodic database scan every 5 minutes for missed episodes.

### `DB` (`internal/graph/db.go`)
Manages three physical SQLite files. The `cache` field (`memory-cache.db`) is the pyramid store — attached to the main connection as the `"cache"` schema so cross-table queries work without cross-DB joins.

## Lifecycle

### Episode Compression

1. **Enqueue**: After an episode is ingested, `EpisodeCompressQueue.Enqueue(episodeID)` is called. If the 256-slot channel is full, the ID is dropped and `needsScan` is set to `true` so the background scan will pick it up.

2. **Worker loop** (`Start` → `compress`): A single goroutine drains the channel. For each episode ID, it calls `DB.GenerateEpisodeSummaries(episode, compressor)`, which spawns `generateCompressedSummaries` asynchronously.

3. **generateCompressedSummaries**: Generates all five levels (L4 → L64) in sequence by calling `compressToTarget` for each. If the episode content is already shorter than the target word count, verbatim text is stored directly (no LLM call). Otherwise, the LLM (Ollama/Qwen2.5:7b by default) compresses to the target.

4. **CJK guard**: If compressed output contains CJK characters but the input did not, `compressToTarget` re-runs with a fallback model (Mistral, English-focused) to prevent language leakage. The reverse case (input has CJK but output doesn't) is also detected.

5. **Periodic scan** (`scan`): Runs every 5 minutes when `needsScan` is true. Calls `GetEpisodesWithoutSummaries(100)` to find episodes with no `episode_summaries` rows and enqueues them. Processes the batch synchronously so logs reflect completed work.

### Engram Pyramid Generation

1. **Immediate C8**: When `consolidateGroup` creates a new L1 engram, it immediately generates only the C8 summary (8-word level) via `GenerateEngramSummaryLevel`. This gives retrieval a usable short summary without blocking consolidation.

2. **Deferred full pyramid**: The full L64→L32→L16→L8→L4 pyramid is deferred to a background "compress-traces" pass that calls `GenerateEngramPyramid(engramID, sourceEpisodes, compressor, fromSource)`.

3. **Cascade vs. fromSource**:
   - Default (`fromSource=false`): cascading — L64 compresses source episodes; L32 compresses L64 output; L16 compresses L32; L8/L4 cascade from L16.
   - `fromSource=true`: L64, L32, L16 each compress the original source context directly (avoids quality degradation from repeated compression at those larger sizes); L8 and L4 still cascade from L16 (extreme ratios from raw source degrade at short targets).

4. **L2+ engrams** (`GenerateEngramPyramidFromEngrams`): Recursive engrams (depth ≥ 2) have other engrams as sources, not episodes. Uses the full uncompressed summary of each source engram as context. `fromSource=true` applies the same L32/L16 direct-from-source optimization.

5. **Entity pyramids** (`GenerateEntityPyramid`): Assembles entity metadata (name, type, aliases, known relations) into a description, stores it as L0 (verbatim), then generates L64→L32→L16→L8→L4 cascade. Called asynchronously from `consolidateGroup` whenever an entity is linked to a new engram.

### Retrieval

1. **Level-aware fetch** (`GetEngramSummary`, `GetEpisodeSummary`): Both fall back to progressively higher compression levels if the requested level doesn't exist, so callers that request L4 but only have L8 stored will still get a result.

2. **Batch retrieval** (`GetEngramSummariesBatch`, `GetEpisodeSummariesBatch`): Loads summaries for a list of IDs in one SQL query, keeping only the first (lowest available) level per item. Used in retrieval Phase 1 and by `buildEntityContext` in consolidation.

3. **Phase 1 funnel** (`Retrieve` in `activation.go`): After spreading activation produces up to 50 candidates, L8 summaries are loaded for all of them and scored against the query text. Only the top-N candidates proceed to Phase 2 (full detail fetch). This avoids loading full content for every activated trace.

## Design Decisions

- **Level number = word count**: `CompressionLevel4 = 4`, `CompressionLevel8 = 8`, etc. The constant value is the target directly, not an opaque enum. This makes comparisons and fallback logic (`level >= 4`) straightforward.

- **Separate cache database**: Summaries live in `memory-cache.db`, not `memory.db`. The file is gitignored and entirely recomputable. This means operators can delete it to reclaim disk space, and the system regenerates lazily. It also avoids bloating the source-of-truth database with derived data.

- **Serialized episode compression, parallel engram compression**: `EpisodeCompressQueue` enforces at-most-one concurrent episode compression to prevent Ollama from being overwhelmed by bursts of ingested episodes. Engram pyramid generation (called from consolidation) is fire-and-forget and can overlap with other operations.

- **needsScan instead of retry**: When the channel overflows, the ID is dropped rather than blocking. The `atomic.Bool` flag ensures eventual consistency via the scan tick, trading immediate consistency for non-blocking ingestion throughput.

- **C8 immediate, full pyramid deferred**: Consolidation generates C8 synchronously so retrieval has a working short summary immediately. The full five-level pyramid is a background operation, accepting that L4/L16/L32/L64 may be unavailable briefly after an engram is created.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `internal/graph.EpisodeCompressQueue` | `internal/graph.DB` | Calls `GenerateEpisodeSummaries` per episode; reads `GetEpisodesWithoutSummaries` for backfill scans |
| `internal/consolidate.Consolidator` | `internal/graph.DB` | Calls `GenerateEngramSummaryLevel` (C8 only) after creating each L1 engram; calls `GenerateEntityPyramid` async after entity links |
| `internal/graph.DB.Retrieve` | `cache.engram_summaries` | Loads L8 summaries in batch for Phase 1 scoring; loads full detail (L0) for Phase 2 |
| `internal/consolidate.Consolidator` | `internal/graph.DB` | `buildEntityContext` calls `GetEngramSummariesBatch` to fetch prior engram context (prefers pyramid levels over raw summary) |
| `cmd/engram/main.go` | `internal/graph.EpisodeCompressQueue` | Wires `NewEpisodeCompressQueue`, starts it as a goroutine, passes it to the API handler and consolidator as the `Compressor` implementation |

## Non-Obvious Behaviors

- **Fallback direction is upward**: `GetEngramSummary(id, 4)` will return an L8 or L16 summary if L4 doesn't exist — it never falls back to a *smaller* (more compressed) level. This means callers requesting tight budgets may silently receive more tokens than expected if the lower levels haven't been generated yet.

- **C8 is generated twice**: `consolidateGroup` generates L8 immediately after creating an engram. If `GenerateEngramPyramid` is later called, it regenerates L8 again as part of the cascade. The second write is an upsert that silently overwrites the first — this is intentional (the cascade version may differ from the standalone one because cascading from L64 gives the LLM more context).

- **memory-cache.db is not committed to git**: The cache file is gitignored. Any deployment that doesn't seed it will start with no pyramid summaries and rely on the scan/backfill mechanisms to regenerate them. There is no pre-population step in the startup sequence.

- **fromSource=true vs cascade quality tradeoff**: At L64 (64 words from potentially thousands of words of source), cascading from shorter levels tends to lose nuance. `fromSource=true` exists specifically because empirical testing showed that going directly from full source to each larger level produces better summaries at L64/L32/L16 — but at L8/L4 the direct-from-source ratio is too extreme, so those still cascade from L16.

- **Entity pyramids are regenerated, not appended**: `GenerateEntityPyramid` overwrites all existing pyramid levels for an entity each time it's called. It's invoked whenever an entity is linked to a new engram, so an entity with many linked engrams will have its pyramid regenerated many times. The entity description assembled as input grows over time (more relations), so later pyramids reflect more context.

- **CJK guard uses model switching, not prompt changes**: When the LLM emits CJK characters for non-CJK input, the code switches to a different model name ("mistral") and retries with the same prompt. This is a model-level workaround, not a prompt-level fix — it assumes the configured primary model has a CJK tendency that a fallback model doesn't share.

## Start Here

- `internal/graph/compression.go` — all compression types, level constants, generation functions, and retrieval with fallback; start here to understand the full data model
- `internal/graph/compress_queue.go` — the episode async queue; explains the overflow/scan pattern and why episodes may be briefly uncompressed after ingestion
- `internal/consolidate/consolidate.go` (`consolidateGroup`) — how engrams enter the pyramid pipeline; shows the C8-immediate / full-pyramid-deferred split
- `internal/graph/db.go` (`DB`, `Open`) — three-database setup; explains why summaries live in a separate attached database and how the "cache" schema is accessible
- `internal/graph/activation.go` (`Retrieve`) — shows how L8 summaries are used in Phase 1 retrieval scoring, making clear why L8 matters more than other levels for query performance
