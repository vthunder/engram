---
topic: Engram Decay & Activation Mechanics
repo: engram
generated_at: 2026-04-06T12:00:00Z
commit: e76b3cb
key_modules: [internal/graph, cmd/engram]
score: 0.88
---

# Engram Decay & Activation Mechanics

> Repo: `engram` | Generated: 2026-04-06 | Commit: e76b3cb

## Summary

This subsystem implements two distinct but interacting dynamics: background exponential decay of persisted activation levels (erasing memories that aren't reinforced) and query-time spreading activation (propagating relevance through the engram graph to retrieve the most contextually appropriate memories). Together they model a biologically-inspired memory system where frequently accessed, recently relevant memories remain "hot" while stale ones fade — and where retrieval surfaces associatively connected memories, not just exact matches.

## Key Data Structures

### `Engram` (`internal/graph/types.go`)

The central record. Key fields for this subsystem:

| Field | Type | Role |
|-------|------|------|
| `Activation` | `float64` | Persisted long-term "hotness" — boosted on access, decayed hourly |
| `AccessCount` | `int` | Cumulative retrieval count; drives ACT-R base-level bias |
| `Strength` | `int` | Reinforcement counter; incremented by `ReinforceEngram` |
| `LastAccessed` | `time.Time` | Timestamp of last retrieval; used for recency scoring and decay |
| `EngramType` | `EngramType` | `knowledge` or `operational`; operational engrams decay 3× faster |
| `LabileUntil` | `time.Time` | Reconsolidation window expiry; while labile, new episodes extend the engram |

Invariant: `Activation` is in `[0.0, 1.0]` (decay has a floor; `BoostEngramAccess` clamps to 1.0).

### `EngramType` (`internal/graph/types.go`)

```go
EngramTypeKnowledge  EngramType = "knowledge"   // facts, decisions, preferences
EngramTypeOperational EngramType = "operational" // meeting reminders, state syncs, deploys
```

The type controls decay rate. Operational engrams are expected to become irrelevant quickly (a "deploy succeeded" note from last week has no value); knowledge engrams are durable.

### Activation constants (`internal/graph/activation.go`)

All algorithmic parameters are named constants, attributed to the Synapse paper (arxiv:2601.02744) where applicable:

| Constant | Value | Meaning |
|----------|-------|---------|
| `DecayRate` (δ) | 0.5 | Fraction of activation lost per spreading iteration |
| `SpreadFactor` (S) | 0.8 | Coefficient controlling how much activation propagates to neighbors |
| `DefaultIters` (T) | 3 | Spreading iterations before stabilization |
| `InhibitionStrength` (β) | 0.15 | How strongly top winners suppress competitors |
| `InhibitionTopM` (M) | 7 | Number of top-activation nodes that apply suppression |
| `SigmoidGamma` (γ) | 5.0 | Sigmoid steepness — sharpens the winners vs. losers distinction |
| `SigmoidTheta` (θ) | 0.3 | Sigmoid firing threshold (lowered from 0.5 for wider dynamic range) |
| `FoKThreshold` (τ) | 0.12 | Minimum max-activation below which retrieval returns empty ("Feeling of Knowing") |
| `BaseActivationWeight` | 0.5 | Scales ACT-R recency-frequency bias on seed nodes |
| `BaseActivationDecayDays` | 30.0 | Recency half-life in days for base-level scoring |

### `Neighbor` (`internal/graph/types.go`)

```go
type Neighbor struct {
    ID     string
    Weight float64
    Type   EdgeType
}
```

Used in spreading: each neighbor carries the edge weight `w_ji` applied in the spread formula. `GetEngramNeighborsBatch` returns these for a set of engram IDs in 2 SQL queries.

## Lifecycle

### Background Decay

1. **Goroutine launch** (`cmd/engram/main.go:runDecay`): At startup, a goroutine runs `runDecay` on a configurable interval (typically 1 hour). It calls `DB.DecayActivationByAge(lambda, floor, intervalHours)`.

2. **Per-engram decay** (`internal/graph/engrams.go:DecayActivationByAge`): Loads all engrams with their `activation`, `last_accessed`, and `engram_type`. For each engram it computes a new activation using exponential decay over the elapsed time. Operational engrams apply a 3× multiplier to the decay coefficient (they fade 3× faster for the same lambda). The result is clamped to `floor` (preventing activation from reaching zero). Updates are batched and written in a transaction.

3. **Access reinforcement** (`internal/graph/engrams.go:BoostEngramAccess`): After a successful retrieval, `Retrieve` (via the API handler) calls `BoostEngramAccess` on the returned engram IDs. This increments `access_count`, updates `last_accessed` to now, and applies a small additive boost to the persisted `activation`. The boost is capped at 1.0. This counteracts decay: frequently retrieved engrams stay hot.

4. **Explicit reinforcement** (`internal/graph/engrams.go:ReinforceEngram`): A separate `POST /v1/engrams/:id/reinforce` endpoint triggers `ReinforceEngram`, which increments `strength` and blends the new embedding with the existing one via EMA: `new_emb = alpha * new + (1-alpha) * old`. This is distinct from access-based boosting and intended for deliberate reinforcement, not just retrieval.

### Query-Time Spreading Activation

1. **Entry point** (`internal/graph/activation.go:DB.Retrieve`): The search handler calls `Retrieve(queryEmb, queryText, limit, extraSeeds...)`, which delegates to `SpreadActivationFromEmbedding`.

2. **Parallel triggering** (`SpreadActivationFromEmbedding`): Three independent seed-finding triggers run concurrently (Go goroutines, safe because SQLite is in WAL mode):
   - **Trigger 1 — Semantic**: `FindSimilarEngrams(queryEmb, topK)` → tries `findSimilarEngramsVec` (sqlite-vec KNN using L2 distance on normalized vectors); falls back to `findSimilarEngramsScan` (O(n) cosine scan). Only returns engrams above `MinSimilarityThreshold = 0.3`.
   - **Trigger 2 — Lexical**: `FindEngramsWithKeywords(queryText, topK)` → tries FTS5 BM25 ranking on `engram_fts`; falls back to Go-side keyword counting scan.
   - **Trigger 3 — Entity**: `FindEntitiesByText(queryText, maxResults)` → uses a cached list of pre-compiled word-boundary regexes (rebuilt lazily on entity writes) to match names/aliases; then `GetEngramsForEntitiesBatch` fetches engrams linked to those entities.
   
   Results are merged into a deduplicated seed set. Caller-supplied `extraSeeds` (e.g., NER-derived entity engrams from the API handler) are added last.

3. **Seed initialization** (`SpreadActivation`): Each seed node is assigned a base boost:
   ```
   boost = SeedBoost + BaseActivationWeight * baseLevel(seed)
   ```
   where `baseLevel` is an ACT-R-inspired recency-frequency score:
   ```
   score = log(access_count + 1) * exp(-decay * days_since_access)
   ```
   Scores are normalized relative to the maximum in the seed set, so the bias is proportional, not absolute. Seeds that have been accessed often and recently get a higher initial activation.

4. **Iteration** (T=3 loops in `SpreadActivation`): Each iteration:
   - Batch-loads neighbors for nodes not yet in the cache (2 SQL queries: direct engram edges + entity-bridged edges).
   - For each active node `j`, propagates to each neighbor `i`:
     ```
     a_i(t+1) += S * w_ji * a_j(t) / fan(j)
     ```
     where `fan(j)` is the count of j's neighbors (fan effect: hub nodes don't dominate).
   - Applies self-retention: `a_i(t+1) += (1 - δ) * a_i(t)`.
   - Seed nodes are protected from dropping below their initial boost (prevents isolated seeds from vanishing through pure decay).

5. **Post-iteration transforms**:
   - **Lateral inhibition** (`applyLateralInhibition`): The top M=7 winners suppress all other nodes: `û_i = max(0, u_i - β * Σ(u_k - u_i) for u_k > u_i)`. Nodes suppressed to ≤ 0 are dropped from the result.
   - **Sigmoid transform** (`applySigmoid`): `σ(x) = 1 / (1 + exp(-γ(x - θ)))` converts activations to "firing rates". γ=5 creates a sharp threshold; θ=0.3 determines where the midpoint falls.

6. **Feeling of Knowing gate** (`Retrieve`): After spreading, if `max(activation) < FoKThreshold (0.12)`, the retrieval returns empty rather than confabulating low-confidence results. For "status queries" (e.g., "what did I do today"), this gate is bypassed because operational engrams may have genuinely low cosine similarity to the query embedding.

7. **Two-phase funnel** (`Retrieve`):
   - Phase 1: Take top-50 engrams by activation; load only L8 summaries (cheap). Score = `activation (dominant) + text_relevance_to_query (tiebreaker)`.
   - Phase 2: Load full detail (L0 verbatim summary, embedding, metadata) for the top-N shortlisted candidates.

8. **Post-retrieval update**: The handler calls `BoostEngramAccess` on the returned engram IDs, updating `last_accessed`, `access_count`, and persisted `activation`.

## Design Decisions

- **Fan effect in spreading**: Dividing by `fan(j)` prevents high-degree nodes (hub engrams connected to many memories) from flooding the activation map. Without this, one highly-connected engram would dominate every query.

- **Seeds protected from full decay**: During spreading iterations, seeds maintain a minimum activation floor (their initial boost). This prevents a seed node with no in-graph connections from vanishing by iteration 2 even though it had a strong initial signal.

- **Operational engrams decay 3× faster**: The separation of `knowledge` vs `operational` types with differentiated decay rates reflects the different time-value curves of these memory classes. A "deploy started" event is irrelevant the next day; a fact about a person's preferences is durable.

- **Spreading activation is stateless at query time**: `SpreadActivation` always starts from a fresh activation map. The persisted `activation` column is NOT loaded as the starting state for graph traversal. Instead, it feeds into the ACT-R base-level bias on seeds only. This keeps retrieval deterministic given the same query, regardless of accumulated boost values.

- **Neighbor batch-loading with `GetEngramNeighborsBatch`**: Loads neighbors for all active nodes in 2 SQL queries (direct edges + entity-bridged edges) rather than N×2 per-node queries. Added explicitly to fix a previous O(N) query pattern that slowed spreading on large graphs.

- **FTS triggers dropped at v31**: When `engram_summaries` moved to `memory-cache.db` (v31 migration), SQLite DB-side triggers could no longer maintain the FTS index across databases. The codebase now does this via application-level manual syncs in `AddEngramSummary` for level-32 summaries. Engineers adding summary writes must sync manually.

- **Two-phase retrieval funnel**: Phase 1 loads cheap L8 (8-word) summaries to score 50 candidates; only the top-N then get full-detail loads. This avoids the cost of loading full verbatim summaries and embeddings for every activated engram.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `cmd/engram` | `internal/graph` | `runDecay` calls `DB.DecayActivationByAge` hourly |
| `internal/api` | `internal/graph` | Search handlers call `DB.Retrieve` / `DB.RetrieveWithContext` |
| `internal/api` | `internal/graph` | After retrieval, handler calls `DB.BoostEngramAccess` on result IDs |
| `internal/consolidate` | `internal/graph` | `AddEngram` writes new engrams; `AddEngramRelation(SIMILAR_TO)` creates edges that become paths for spreading |
| `internal/graph` (main DB) | `vectors` DB | `findSimilarEngramsVec` queries `vectors.engram_vec` (sqlite-vec KNN); embeddings stored in `vectors.engram_embeddings` |
| `internal/graph` (main DB) | `cache` DB | Phase 1 funnel loads L8 summaries from `cache.engram_summaries` via `GetEngramsBatchAtLevel` |
| `internal/mcp` | `internal/graph` | MCP `search_memory` tool calls `DB.Retrieve` (same path as REST API) |

## Non-Obvious Behaviors

- **Persisted activation and spreading activation are independent**: The `activation` column is boosted by access and decayed hourly. The spreading activation computed during a query does NOT write back to this column. `GetActivatedEngrams(threshold, limit)` queries the persisted column — it reflects historical access frequency, not the last retrieval's spread result.

- **Operational engram retrieval bias**: When `isStatusQuery(queryText)` returns true (query contains words like "status", "today", "what did", "recent"), the FoK gate is bypassed AND operational engrams get an additional bias boost in the Phase 1 scoring. This is the only place where query intent changes the scoring path.

- **Entity-bridged neighbors**: `GetEngramNeighborsBatch` returns two classes of neighbors: direct `engram_relations` edges AND entity-bridged neighbors (`GetEngramNeighborsThroughEntities`). Two engrams that share a named entity (e.g., "Alice") become neighbors with a weight proportional to the number of shared entities: `weight = min(1.0, sharedCount * 0.3)`. There is no explicit "entity edge" in `engram_relations` — the bridge is computed at query time from the junction table.

- **`labileUntil` is separate from activation**: The labile window controls reconsolidation eligibility (whether new related episodes extend an existing engram), not retrieval probability. A labile engram with low activation is still retrieved if relevant; an expired labile engram with high activation does not absorb new episodes.

- **`ReinforceEngram` blends embeddings via EMA**: With alpha=0.3, `new_emb = 0.3 * new + 0.7 * old`. This shifts the engram's position in embedding space toward the new information without wholesale replacement. The vector must be re-normalized before KNN lookups because `engram_vec` stores normalized vectors; callers are responsible for this.

- **Seed batch-loading has a fallback path**: `SpreadActivation` batch-loads all seed neighbors upfront in 2 queries. If the batch query fails, it falls back to per-node loading. This fallback exists but is not tested under normal conditions — if the batch fails silently, spreading degrades to O(N) queries without error, which may appear as slow retrieval without obvious errors.

## Start Here

- `internal/graph/activation.go` — all spreading activation logic and constants; read this first to understand any retrieval behavior
- `internal/graph/engrams.go` (`DecayActivationByAge`, `BoostEngramAccess`) — the two sides of the persisted activation lifecycle: decay and reinforcement
- `internal/graph/types.go` (`Engram`, `EngramType`, constants) — data model; understand `EngramType` before working on decay or type-differentiated behavior
- `cmd/engram/main.go` (`runDecay`) — wiring of the decay goroutine; shows config keys and interval
- `internal/graph/graph_test.go` (`TestDecayActivationByAge`, `TestSpreadingActivation`) — executable specifications of decay and spreading behavior; run these to verify changes
