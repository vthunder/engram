---
topic: Embedding & Vector KNN
repo: engram
generated_at: 2026-04-06T00:00:00Z
commit: ce017d4e
key_modules: [internal/embed, internal/graph]
score: 0.63
---

# Embedding & Vector KNN

> Repo: `engram` | Generated: 2026-04-06 | Commit: `ce017d4e`

## Summary

Engram embeds text via an Ollama-backed client and stores those vectors in a dedicated SQLite file (`memory-vectors.db`) using the sqlite-vec `vec0` virtual table for fast approximate nearest-neighbor search. At query time, semantic KNN is one of three concurrent triggers that seed the spreading activation algorithm — alongside BM25 lexical search and entity matching — and falls back gracefully to an O(n) Go scan if sqlite-vec is not compiled in.

## Key Data Structures

### `embed.Client` (`internal/embed/client.go`)
Thin HTTP client wrapping Ollama's `/api/embeddings` and `/api/generate` endpoints. Holds a fixed-size FIFO embedding cache (`embeddingCache`, default 128 entries keyed by SHA-256 of the input text). Also carries a `generationModel` separate from `model` — the embed model defaults to `nomic-embed-text` (768 dims); the generation model defaults to `llama3.2`. HTTP timeout is 300 s to accommodate long compression calls.

### `embeddingCache` (`internal/embed/client.go`)
Thread-safe FIFO cache backed by a `map[string][]float64` + insertion-order slice. Keys are the first 16 hex bytes of SHA-256(`text`). When full, the oldest entry is evicted. Only the text content is hashed — not the model name — so swapping models without recreating the client would silently serve stale embeddings.

### `graph.DB` (`internal/graph/db.go`)
Holds three `*sql.DB` connections: `db` (main, `memory.db`), `vectors` (`memory-vectors.db`), and `cache` (`memory-cache.db`). Both secondary connections are also ATTACHed to `db` under schema names `"vectors"` and `"cache"` so cross-table SQL works transparently. Key fields for the embedding subsystem: `vecAvailable bool` (guards all vec code paths), `vecDim int` (0 until first embedding determines dimension), `embeddingInMain bool` (true for pre-v31 databases where embeddings still live in `main.engrams.embedding`).

### `vec0` virtual table (`vectors.engram_vec`)
Created inside `memory-vectors.db` by `ensureVecTable()`. Schema: `rowid INTEGER, +engram_id TEXT, embedding FLOAT[<dim>]`. Uses integer rowid (mapped to the engram's rowid in `main.engrams`) rather than TEXT PRIMARY KEY — vec0's TEXT PK partitioning breaks KNN queries. The `+engram_id` auxiliary column lets queries retrieve the engram ID without a join.

### `SimilarEngram` (`internal/graph/activation.go`)
Return type for threshold-filtered similarity searches: `{ID string, Similarity float64}`. Used when building `SIMILAR_TO` edges between engrams during consolidation.

## Lifecycle

1. **Extension registration**: `graph.init()` calls `sqlite_vec.Auto()`, which registers the vec0 virtual table driver with the `go-sqlite3` CGO driver. This runs once per process before any `sql.Open` call.

2. **DB open and vec table setup** (`graph.Open()`): opens three DB files, ATTACHes vectors and cache to the main connection, runs schema migrations. Migration v18 calls `initVecTableFromEngrams()` to detect the embedding dimension from existing data and create `vectors.engram_vec`. Migration v31 moves all embeddings from `main.engrams.embedding` to `vectors.engram_embeddings`, then NULLs the old column and runs `VACUUM` (reclaims ~130 MB). If no embeddings exist yet, `vecDim` stays 0 and table creation is deferred.

3. **Embedding generation** (`embed.Client.Embed(text)`): computes a cache key from SHA-256 of the input, returns cached result if present. Otherwise POSTs `{"model": "<model>", "prompt": "<text>"}` to `<baseURL>/api/embeddings`, decodes the `[]float64` embedding, stores it in the FIFO cache, and returns it.

4. **Storing an engram's embedding** (`graph.DB.AddEngram()`): after inserting into `main.engrams`, inserts the raw `[]float64` as JSON BLOB into `vectors.engram_embeddings`, then calls `ensureVecTable(dim)` (lazy — creates the vec0 table on first use), normalizes the vector to float32 unit length, and upserts the rowid into `vectors.engram_vec`. Pre-v31 databases take the legacy path: embedding stored in `main.engrams.embedding` instead.

5. **KNN query** (`graph.DB.FindSimilarEngrams(queryEmb, topK)`): tries `findSimilarEngramsVec()` first. This normalizes the query to float32, converts the cosine similarity threshold (0.3) to an L2 distance threshold via `sqrt(2 * (1 - threshold))`, queries `vectors.engram_vec` with `distance < L2_threshold ORDER BY distance LIMIT topK*3`, then converts returned L2 distances back to cosine similarity via `1 - L2²/2`. Falls back to `findSimilarEngramsScan()` (O(n) Go-side cosine scan over JSON BLOBs) if vec is unavailable or returns no results.

6. **Threshold-filtered similarity** (`FindSimilarEngramsAboveThreshold()`): same vec/scan dual-path but returns all engrams above a threshold rather than top-K. Used by consolidation to build `SIMILAR_TO` edges between newly created engrams and existing ones.

7. **Full retrieval seeding** (`SpreadActivationFromEmbedding()`): launches three goroutines concurrently — (1) `FindSimilarEngrams` (semantic/vec), (2) `FindEngramsWithKeywords` (BM25/FTS5 lexical), (3) entity name matching against query text. SQLite WAL mode allows concurrent reads from separate connections. Results are merged into a unified seed set, de-duplicated, then passed to `SpreadActivation()`.

8. **Reinforcement** (`ReinforceEngram(id, newEmbedding, alpha)`): updates `strength` in main and upserts the blended embedding (exponential moving average via `UpdateCentroid`) into the vectors DB and vec table.

## Design Decisions

- **float32 normalization before storage**: sqlite-vec's `vec0` stores `FLOAT[N]` as float32. Vectors are normalized to unit length before insertion so that L2 distance is equivalent to cosine distance (`cosine_dist = L2² / 2` for unit vectors). This avoids a separate cosine operation inside sqlite-vec and lets the threshold filter use a simple distance cutoff.

- **Integer rowid instead of TEXT PK in `vec0`**: vec0 partitions KNN search by text PK values, which breaks when the PK is a random hex string. Using integer rowid (the engram's SQLite rowid) avoids this and keeps the auxiliary `+engram_id` column for ID retrieval without a join.

- **`vecDim` is dynamically inferred**: the embedding dimension is not in config — it is read from the first stored embedding. This means fresh databases self-configure on first `AddEngram` call. It also means changing models between runs without clearing the vectors DB will silently mix dimensions and likely fail at vec insertion.

- **Graceful fallback throughout**: `vecAvailable` is checked before every sqlite-vec call; on failure the O(n) Go scan takes over. This ensures the service starts and operates even without CGO or a compatible sqlite-vec build.

- **Separate `memory-vectors.db`**: embeddings are gitignored and recomputable. Splitting them out keeps `memory.db` (committed) small and allows `VACUUM` after deletion without locking the main DB.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `internal/embed` | Ollama HTTP server | `POST /api/embeddings` — text in, `[]float64` out |
| `internal/graph` | `internal/embed` | `Client.Embed()` called in `AddEngram` and `SpreadActivationFromEmbedding` |
| `internal/graph` | sqlite-vec CGO extension | vec0 virtual table queries via registered `go-sqlite3` driver |
| `internal/consolidate` | `internal/embed` | embeds episodes/engrams for consolidation; also uses `Client.Generate()` for LLM calls |
| `internal/api` | `internal/graph` | `DB.Retrieve()` / `DB.RetrieveWithContext()` called from search handler |
| `internal/graph` | `internal/graph` (cross-DB) | main connection ATTACHes vectors and cache schemas; cross-DB subqueries used in KNN and batch loads |

## Non-Obvious Behaviors

- **L2 ↔ cosine conversion is manual**: sqlite-vec reports L2 distance, not cosine similarity. The conversion `cosine_sim = 1 - L2² / 2` is only valid for unit-normalized vectors — if a vector is stored without normalization, the returned similarity will be wrong. `normalizeFloat32()` is called before every insert and every query.

- **`vecDim = 0` means the vec table doesn't exist yet**: calling `FindSimilarEngrams` on an empty database returns empty rather than erroring — `vecAvailable` is false until `ensureVecTable` succeeds. The first `AddEngram` call triggers table creation and backfill.

- **Pre-v31 compatibility via `embeddingInMain` flag**: `AddEngram` and `ReinforceEngram` both branch on this flag. Databases migrated from before v31 have the old `engrams.embedding` column; writes go to both old and new locations during the migration window.

- **FIFO cache key ignores the model name**: if `Client.SetGenerationModel()` is called (changing the generation model), cache entries from a previous model are still returned for embed calls. In practice `SetGenerationModel` only changes the *generation* model; `model` (embed model) is fixed at construction, so this doesn't cause bugs today but is a latent footgun.

- **Three concurrent DB reads in `SpreadActivationFromEmbedding`**: the goroutines use the same `g *DB` receiver. SQLite WAL mode allows concurrent readers on separate connections — but the main connection has `MaxOpenConns(1)`. The secondary `g.vectors` and FTS5 (on `g.db`) reads are fine because WAL readers don't block each other; the single-connection limit on `g.db` means only one FTS/lexical query runs at a time inside that goroutine.

- **`topK*3` over-fetch in vec search**: `findSimilarEngramsVec` fetches `topK * 3` candidates before applying the cosine threshold filter. This compensates for cases where many near-neighbors are just below the threshold — the threshold filter may reduce the result set significantly, and fetching 3× avoids returning too few results.

## Start Here

- `internal/embed/client.go` — embedding client: caching, Ollama wire protocol, math helpers (`CosineSimilarity`, `AverageEmbeddings`, `UpdateCentroid`)
- `internal/graph/activation.go` — `FindSimilarEngrams`, `findSimilarEngramsVec`, `SpreadActivationFromEmbedding`; all KNN and retrieval seeding logic
- `internal/graph/db.go` — `DB` struct, `Open`, `ensureVecTable`, migrations v18 and v31; the authoritative place to understand the three-DB layout and vec table lifecycle
- `internal/graph/engrams.go` — `AddEngram`, `ReinforceEngram`; shows exactly how embeddings are written post- and pre-v31
