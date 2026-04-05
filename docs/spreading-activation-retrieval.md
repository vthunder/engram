---
topic: Spreading Activation Retrieval
repo: engram
generated_at: 2026-04-06T02:00:00Z
commit: fe776454
key_modules: [internal/graph]
score: 0.98
---

# Spreading Activation Retrieval

> Repo: `engram` | Generated: 2026-04-06 | Commit: fe776454

## Summary

Engram's retrieval system locates relevant memories by seeding a graph with candidate engrams found via three concurrent signals (embedding KNN, BM25 keyword search, and entity lookup), then propagating activation through typed edges for three iterations using a Synapse-style algorithm. The result is ranked by post-inhibition, post-sigmoid activation score — with a "Feeling of Knowing" gate that returns nothing rather than confabulating when confidence is too low.

## Key Data Structures

### `Engram` (`internal/graph/types.go`)
The core memory unit (Tier 3). Key fields: `Activation float64` (current salience, persisted across queries), `Depth int` (0 = L1 from episodes, 1 = L2 from L1s, etc.), `EngramType EngramType` (either `"knowledge"` or `"operational"` — affects post-retrieval scoring), `Strength int` (reinforcement counter), `AccessCount int` and `LastAccessed time.Time` (drive the ACT-R base-level bias). `LabileUntil time.Time` marks a reconsolidation window after write.

### `Neighbor` (`internal/graph/types.go`)
The adjacency list entry used by the spreading activation loop. Holds `ID string`, `Weight float64`, and `Type EdgeType`. Weight determines how strongly a node's activation propagates across an edge.

### `EdgeType` (`internal/graph/types.go`)
Typed directed edges stored in `engram_relations`. For retrieval the key types are:
- `SIMILAR_TO` — semantic similarity ≥ 0.85; created at consolidation time
- `SHARED_ENTITY` — implicit bridge when two engrams co-mention the same entity
- `SOURCED_FROM` — engram → source episode
- `CONSOLIDATED_FROM` — L2+ engram → constituent L1 engrams

### `DB` (`internal/graph/db.go`)
Top-level service object. Key fields: `db *sql.DB` (main DB — MaxOpenConns=1 so ATTACH persists), `vectors *sql.DB` (embedding store), `cache *sql.DB` (pyramid summaries), `vecAvailable bool` / `vecDim int` (sqlite-vec state), `embeddingInMain bool` (pre-v31 schema compat flag). The entity lookup cache (`entityCache`, guarded by `entityCacheMu sync.RWMutex`) is lazily rebuilt on first access and invalidated on entity writes.

### `RetrievalResult` (`internal/graph/types.go`)
Output of `Retrieve()`: `Engrams []*Engram`, `Episodes []*Episode`, `Entities []*Entity`. In practice only `Engrams` is populated by the spreading activation path; the other fields are available for callers that need raw sources.

## Lifecycle

1. **Entry point — `Retrieve()`** (`activation.go:741`): Called with `queryEmb []float64`, `queryText string`, `limit int`, and optional `extraSeeds ...string`. Creates an empty `RetrievalResult` and delegates seed discovery to `SpreadActivationFromEmbedding()`.

2. **Concurrent trigger seeding — `SpreadActivationFromEmbedding()`** (`activation.go:317`): Launches three goroutines simultaneously and merges their results into a seed set:
   - **Semantic trigger**: `FindSimilarEngrams(queryEmb, topK=20)` — queries `vectors.engram_vec` (vec0 virtual table) for L2 KNN; falls back to O(n) cosine scan if sqlite-vec is unavailable or embedding dimensions mismatch. Only returns engrams with cosine similarity ≥ `MinSimilarityThreshold` (0.3).
   - **Lexical trigger**: `FindEngramsWithKeywords(queryText, topK=20)` — queries `engram_fts` FTS5 virtual table using BM25 ranking with OR-joined keywords; falls back to Go-side full table scan if FTS5 is unavailable. Stop-words and short tokens (< 3 chars) are filtered before matching.
   - **Entity trigger**: `FindEntitiesByText(queryText, 5)` performs regex word-boundary matching against the entity name cache (lazily loaded, invalidated on writes), then `GetEngramsForEntitiesBatch(entityIDs, 5)` retrieves up to 5 engrams per matched entity in a single SQL query.
   
   Caller-supplied `extraSeeds` (e.g. NER-derived entity engrams from the API handler) are merged into the seed set after goroutine results arrive.

3. **Initialization — `SpreadActivation()`** (`activation.go:210`): Each seed node receives a boost of `SeedBoost * (1 + BaseActivationWeight * baseScore)` where `baseScore` is an ACT-R-inspired recency-frequency score: `log(access_count+1) * exp(-days_since_access / 30)`, normalized across seeds. Unaccessed seeds get `SeedBoost = 0.5`; frequently accessed, recently used seeds can reach up to 0.75.

4. **Neighbor loading**: Batch-loads all seed neighbors in 2 SQL queries via `GetEngramNeighborsBatch()`. Each node's `Neighbor` list merges: (a) direct `engram_relations` edges (bidirectional), and (b) entity-bridge neighbors via `GetEngramNeighborsThroughEntities()` — a three-way join through `engram_entities` that finds engrams sharing at least one entity, weighted by `sharedCount * 0.3` (capped at 1.0). Combined list is capped at `MaxEdgesPerNode = 15` by weight descending.

5. **Iteration loop (T=3)**: Each iteration:
   - Any node activated in the previous round but not yet in `neighborCache` is batch-loaded.
   - For each active node with activation `a`, contribution to each neighbor is `SpreadFactor * w * a / fan(j)` where `fan(j)` is the out-degree of the spreading node (fan effect — high-degree nodes dilute activation). `SpreadFactor = 0.8`.
   - Self-activation decays: `(1 - DecayRate) * a` with `DecayRate = 0.5` — each node retains half its activation per iteration absent input.
   - Seed nodes are floor-clamped at 0.3 to prevent isolated seeds from vanishing.
   - The activation map is fully replaced each iteration (not additive across iterations).

6. **Lateral inhibition** (`activation.go:1069`): After T=3 iterations, the top-M=7 nodes by activation are identified as "winners". Non-winners have their activation suppressed: `suppressed = act - β * Σ(winnerAct - act for each winner above act)` with `β = InhibitionStrength = 0.15`. Nodes suppressed to ≤ 0 are dropped from the map entirely.

7. **Sigmoid transform** (`activation.go:1122`): `σ(x) = 1 / (1 + exp(-γ(x - θ)))` with `γ = 5.0`, `θ = 0.3` firing threshold. Converts raw activation values into firing rates in [0, 1]. Nodes below θ approach 0; nodes above approach 1 steeply.

8. **Feeling of Knowing gate** (`activation.go:753`): If the maximum activation across all nodes is below `FoKThreshold = 0.12`, `Retrieve()` returns an empty result immediately. This prevents the service from returning low-confidence guesses.

9. **Two-phase funnel** (`activation.go:781`):
   - Phase 1: Take top-50 candidates by activation, load L8 (8-word) pyramid summaries via `GetEngramsBatchAtLevel()`. Rescore with `combinedScore = activation + textKeywordMatchCount * 0.1` against query keywords.
   - Phase 2: Load full engram detail (`GetEngramsBatch()`) for top-N from Phase 1. Phase 1 reduces I/O for large result sets by pre-filtering with cheap summaries before loading full verbatim text.

10. **Operational bias and result assembly** (`activation.go:856`): For non-status queries (determined by `isStatusQuery(queryText)` — checks for keywords like "recent", "current", "status"), engrams of type `EngramTypeOperational` have their activation multiplied by 0.5. Results are re-sorted and returned in `RetrievalResult.Engrams`.

## Design Decisions

- **Three concurrent triggers**: Semantic similarity alone misses exact-name or keyword matches; lexical search alone misses paraphrases. Running all three in parallel (T≈10–30ms per trigger on WAL-mode SQLite) gives broader recall without serialization cost.

- **MaxOpenConns(1) on main DB**: SQLite `ATTACH` statements are connection-scoped. If the pool allocates multiple connections, the `vectors.engram_vec` virtual table is invisible on all but the attaching connection. MaxOpenConns=1 ensures all queries see the attached vector schema.

- **FTS5 keyed against level-32 summaries, not verbatim content**: The `engram_fts` table is content-indexed over `engram_summaries WHERE compression_level = 32`. This means BM25 keyword matching operates over 32-word pyramid summaries, not raw text. Verbatim content is often hundreds of words and would hurt BM25 discrimination; the 32-word level is the best trade-off between signal density and recall breadth.

- **Entity bridge as implicit edges**: Rather than materializing SHARED_ENTITY edges into `engram_relations` at write time (which would explode edge count for common entities), entity bridging is computed at query time via the `GetEngramNeighborsThroughEntities()` join. Weight is capped at 1.0 so highly-shared entities don't artificially dominate activation.

- **ACT-R base-level bias**: Seeds with high `access_count` and recent `last_accessed` receive a proportionally higher initial boost. This models human memory recency-frequency effects — often-consulted memories start with more activation and are more likely to propagate. The bias is normalized within the seed set so it doesn't dominate; it only breaks ties between otherwise-equal seeds.

- **FoK threshold at 0.12**: The threshold was chosen (and commented as `lowered from 0.5 for better dynamic range`) to be below the sigmoid firing threshold of 0.3 but above negligible noise. A query that seeds no engrams at all will produce zero activation, while a weak but real match will typically reach 0.12 post-sigmoid.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `internal/api` | `internal/graph` | HTTP handler calls `DB.Retrieve()` and `DB.RetrieveWithContext()` with pre-computed query embedding |
| `internal/mcp` | `internal/graph` | MCP tool calls dispatch to the same `DB.Retrieve()` path over stdio |
| `internal/api` | `internal/embed` | Handler computes query embedding via `embed.Client` before calling graph retrieval |
| `internal/consolidate` | `internal/graph` | Consolidation writes new engrams via `DB.AddEngram()` with SIMILAR_TO edges; these become traversable nodes in future retrievals |
| `internal/api` | `internal/ner` | Handler optionally calls NER on query text to extract entities; resolved entity engram IDs are passed as `extraSeeds` to `SpreadActivationFromEmbedding()` |

## Non-Obvious Behaviors

- **FTS5 keyword matching is on compressed summaries, not content**: The `engram_fts` virtual table is backed by `engram_summaries` at compression_level=32. A query for "Paris climate accord" will match the 32-word summary, not the verbatim episode content. If an engram's 32-word summary doesn't include those words, it won't be lexically seeded even if the verbatim source does.

- **L2-to-cosine conversion for KNN**: The `vectors.engram_vec` table stores L2-normalized embeddings. Since L2-normalized vectors satisfy `cosine_dist = L2²/2`, the code converts the cosine threshold to L2 threshold as `maxL2 = sqrt(2 * cosine_dist)` for filtering. The raw `distance` column from vec0 is L2; callers receive cosine similarity back via `l2ToCosineSim()`.

- **Activation is stateful and persisted**: `SpreadActivation()` writes results back to the `engrams.activation` column via `PersistActivations()`. Repeated queries for the same content gradually increase a node's stored activation. This means activation-ordered reads (`GetActivatedEngrams()`) reflect cumulative query history, not just current state.

- **Seed floor prevents seed nodes from vanishing in later iterations**: Iteration 2 and 3 apply the same `DecayRate = 0.5` self-decay to seeds as non-seeds. Without the `if seedSet[id] && newActivation[id] < 0.3 { ... }` floor, an isolated seed (no in-graph neighbors) would decay to 0.5 → 0.25 → 0.125 across 3 iterations and potentially fall below the FoK threshold, causing queries with accurate seeds to return empty.

- **Entity bridge weight is computed on shared count, not edge weight**: `GetEngramNeighborsThroughEntities()` assigns weight as `min(sharedEntityCount * 0.3, 1.0)`. Two engrams that co-mention 4 entities get weight 1.0 — same as two engrams directly related with weight 1.0. High-salience entity matches don't get additional weight beyond this cap.

- **Operational engram downweighting is query-context-sensitive**: `isStatusQuery()` checks for keywords like "recent", "last", "current", "status", "today". If the query includes these words, operational engrams (meeting reminders, deploys, state syncs) are returned at full activation. For all other queries they're downweighted 0.5× post-retrieval — meaning they can still appear but only if their activation substantially exceeds knowledge engrams.

## Start Here

- `internal/graph/activation.go` — the entire spreading activation algorithm: `Retrieve()`, `SpreadActivation()`, `SpreadActivationFromEmbedding()`, `applyLateralInhibition()`, `applySigmoid()`, and all three seed trigger implementations
- `internal/graph/engrams.go` — CRUD for engrams, `GetEngramNeighbors()` and `GetEngramNeighborsBatch()` which define the graph traversal structure, and the two-phase funnel batch fetch methods
- `internal/graph/types.go` — all core types (`Engram`, `Episode`, `Entity`, `Neighbor`, `EdgeType`, `RetrievalResult`) and the `EngramType` classification constants
- `internal/graph/db.go` — multi-DB setup (why MaxOpenConns=1 matters), schema migrations (v17 for FTS5, v18 for sqlite-vec, v21 for traces→engrams rename), the `DB` struct fields that influence retrieval behavior
- `internal/api/handlers.go` — how `Retrieve()` is called in practice: embedding computation, NER-seed injection, and the HTTP response assembly
