---
topic: Multi-Database Architecture
repo: engram
generated_at: 2026-04-06T00:00:00Z
commit: 44a57aec
key_modules: [internal/graph, config, cmd/engram]
score: 0.76
---

# Multi-Database Architecture

> Repo: `engram` | Generated: 2026-04-06 | Commit: 44a57aec

## Summary

Engram splits its persistent state across three physical SQLite files: a main source-of-truth database for episodes, engrams, entities, and schemas; a vectors database for embedding BLOBs and the sqlite-vec KNN index; and a cache database for pre-computed summary pyramids. The vectors and cache files are gitignored and fully recomputable, keeping the committed database lean while still enabling fast vector search and budget-aware retrieval via a single `DB` struct that holds all three connections simultaneously.

## Key Data Structures

### `DB` (`internal/graph/db.go`)
```go
type DB struct {
    db          *sql.DB  // memory.db — MaxOpenConns=1, primary connection
    vectors     *sql.DB  // memory-vectors.db — independent connection for isolated writes
    cache       *sql.DB  // memory-cache.db — independent connection for isolated writes
    path        string
    vectorsPath string
    cachePath   string
    vecAvailable    bool
    vecDim          int  // embedding dimension; 0 = not yet determined
    embeddingInMain bool // true if pre-v31 engrams.embedding column still exists
    entityCacheMu   sync.RWMutex
    entityCache     []entityCacheEntry
}
```
The two independent connections (`vectors`, `cache`) exist for isolated maintenance writes. The ATTACH statements on `db` (the main connection) make the same files accessible as schemas `"vectors"` and `"cache"` for cross-schema SQL queries.

### `StorageConfig` (`config/config.go`)
```go
type StorageConfig struct {
    Path        string `yaml:"path"`         // directory for memory.db
    VectorsPath string `yaml:"vectors_path"` // override for memory-vectors.db
    CachePath   string `yaml:"cache_path"`   // override for memory-cache.db
}
```
All three paths are independently configurable. If `VectorsPath`/`CachePath` are empty, they default to `<Path>/memory-vectors.db` and `<Path>/memory-cache.db`.

### `LLMConfig` with per-function resolution (`config/config.go`)
Three separate LLM configs: `CompressionLLM`, `ConsolidationLLM`, `InferenceLLM`. Resolved via `ResolvedCompressionLLM()` etc., with fallback to the deprecated `LLM` field. Defaults: compression → ollama/qwen2.5:7b, consolidation/inference → anthropic/haiku.

## Lifecycle

### Database Open (`DB.Open`)

1. **Derive paths**: If `vectorsPath`/`cachePath` are empty in `extraPaths`, default to `<statePath>/memory-vectors.db` and `<statePath>/memory-cache.db`. Ensure the directory exists.

2. **Open main DB** (`g.db`): Opens `memory.db` with `MaxOpenConns(1)`. The single-connection limit is critical — ATTACH statements only persist on the connection that executed them. Multiple connections would each need their own ATTACH and can't share the same attached schemas.

3. **Open secondary DBs**: `g.vectors` and `g.cache` are opened as independent `*sql.DB` connections to their respective files. These are used for bulk write operations (embedding backfill, pyramid generation) that should not share the main connection.

4. **ATTACH to main**: Both secondary DBs are ATTACHed to `g.db` under schema names `"vectors"` and `"cache"`:
   ```sql
   ATTACH DATABASE '<vectors_path>' AS vectors
   ATTACH DATABASE '<cache_path>' AS cache
   ```
   After this, SQL on `g.db` can reference `vectors.engram_embeddings` and `cache.engram_summaries` directly.

5. **WAL mode**: Enabled on all three connections. WAL allows concurrent readers with a single writer — important because consolidation (write-heavy) runs in a background goroutine while the API serves reads.

6. **Initialize schemas**: `initVectorsSchema()` and `initCacheSchema()` create tables idempotently. Cache tables have **no foreign key constraints** because SQLite FKs only work within a single DB file; cross-attached FKs are silently ignored.

7. **Run migrations**: `runMigrations()` applies incremental DDL changes to the main DB. Version 31 is the landmark multi-DB split migration (see below).

8. **Check vec availability**: Attempts to query the sqlite-vec extension version. Sets `vecAvailable=true` and initializes the `engram_vec` virtual table in the vectors schema if successful.

9. **Detect pre-v31 layout**: `embeddingInMain` is set if the `engrams.embedding` column still exists in the main DB. This preserves read compatibility during and after the v31 migration.

### Migration v31: The Multi-DB Split

This was the migration that introduced the three-DB layout. Prior to v31, embeddings were stored as BLOB columns in `main.engrams` and summaries in `main.engram_summaries`. The migration:

1. Copies embedding BLOBs from `main.engrams.embedding` → `vectors.engram_embeddings` via the ATTACHed connection.
2. Copies pyramid summaries from `main.engram_summaries` → `cache.engram_summaries`.
3. Drops FTS5 triggers that referenced `main.engram_summaries`.
4. Drops summary tables from main.
5. Drops the `engram_vec` virtual table from main (rebuilt in vectors schema).
6. NULLs out the embedding columns in `main.engrams` (data is now in vectors).
7. Runs `VACUUM` to reclaim freed pages — embedding BLOBs were ~130MB.

The migration is idempotent: it checks for column/table existence before acting. `embeddingInMain=true` indicates the NULL'ing step hasn't run yet.

### Write paths post-v31

- **New embeddings**: Written to `vectors.engram_embeddings` via the ATTACHed main connection (`INSERT INTO vectors.engram_embeddings`), and also to the `engram_vec` virtual table.
- **New pyramid summaries**: Written to `cache.engram_summaries` / `cache.episode_summaries` / `cache.entity_summaries` via the main connection's ATTACH.
- **Independent bulk writes**: When `GenerateEngramPyramid` or `EpisodeCompressQueue` do batch writes, they use `g.cache` (the independent connection) to avoid serializing through the main connection.
- **Main DB**: Episodes, engrams, entities, schema instances, and all relational edges (engram_relations, engram_entities, etc.) stay in `memory.db`.

### Retrieval path

1. Phase 1 of `Retrieve` loads L8 summaries for top-50 activation candidates via `cache.engram_summaries`.
2. Vector KNN queries use `vectors.engram_vec` (a vec0 virtual table) with L2 distance on normalized float32 vectors (L2 on unit vectors is equivalent to cosine distance: `cosine_dist = L2²/2`).
3. FTS5 keyword search uses `engram_fts` in the main DB, indexed from `cache.engram_summaries` level-32 content via INSERT triggers.

## Design Decisions

- **MaxOpenConns(1) on main is non-negotiable**: ATTACH binds to a connection. SQLite's `database/sql` driver may use any pooled connection for a given query; with multiple connections, an ATTACH on conn-A is invisible to conn-B. Single-connection mode ensures the vectors and cache schemas are always visible.

- **Independent connections for bulk writes**: Even though the ATTACHed schemas are accessible via `g.db`, `g.vectors` and `g.cache` as independent connections avoid blocking the main connection during long bulk operations. If pyramid generation used `g.db` exclusively, a 200-engram backfill would hold the sole main connection for seconds.

- **No FK constraints in cache/vectors**: SQLite enforces FK constraints only within a single DB file; cross-DB FKs via ATTACH silently do nothing. The cache tables reference `engram_id` values from main.db but cannot declare real FK constraints — application code is responsible for consistency.

- **vectors and cache are gitignored**: Both files are fully recomputable from the source data in `memory.db`. This keeps the committed DB small (text + metadata only) and allows deployments to start fresh and build the indexes lazily. There is no restore or seed step for these files.

- **vec0 uses integer rowid + auxiliary column**: `ensureVecTable` creates `engram_vec` with a rowid that matches `main.engrams.rowid` (not the string ID). This works around vec0's behavior with TEXT PRIMARY KEY which breaks KNN queries. The `+engram_id` auxiliary column maps back to the string ID after KNN returns rowids.

- **WAL mode on all three DBs**: SQLite in WAL mode allows unlimited concurrent readers alongside one writer. This is important for Engram: the API handles reads continuously while consolidation (background) writes engrams and the decay loop updates activations.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `cmd/engram/main.go` | `internal/graph.DB.Open` | Passes `statePath` + optional path overrides from `StorageConfig`; receives single `*DB` wrapping all three connections |
| `internal/graph.DB` (main conn ATTACH) | `vectors.engram_embeddings`, `vectors.engram_vec` | Embedding reads/writes and KNN queries via ATTACHed schema |
| `internal/graph.DB` (main conn ATTACH) | `cache.engram_summaries`, `cache.episode_summaries` | Pyramid summary reads/writes via ATTACHed schema |
| `internal/graph.DB` (g.vectors independent) | `memory-vectors.db` | Bulk embedding backfill without blocking main connection |
| `internal/graph.DB` (g.cache independent) | `memory-cache.db` | Bulk pyramid write operations (GenerateEngramPyramid, EpisodeCompressQueue) |
| `config.StorageConfig` | `internal/graph.DB.Open` | Path configuration for all three DB files |

## Non-Obvious Behaviors

- **MaxOpenConns(1) causes a deadlock risk**: The `ensureVecTable` function explicitly collects all rows into memory before closing a query, then opens a transaction. This is because `g.db.Begin()` needs the sole connection that is already held by the open `Rows`. This pattern must be followed in any function that needs to query and then write on `g.db` in sequence.

- **ATTACH is on the main connection only, not on g.vectors/g.cache**: Cross-schema SQL (e.g., `SELECT * FROM vectors.engram_vec`) only works on queries routed through `g.db`. Queries on `g.vectors` see only the `memory-vectors.db` tables (unqualified names). Mixing up which connection to use produces "no such table" errors, not panics.

- **FTS5 index lives in main but indexes cache content**: The `engram_fts` virtual table is in `memory.db` and its INSERT/UPDATE/DELETE triggers fire when rows are added to `cache.engram_summaries` via the ATTACHed connection. If the cache is deleted and rebuilt (wiped + regenerated), the FTS triggers do not fire for the rebuild — a subsequent migration (v20-pattern) or manual rebuild is needed to resync FTS.

- **embeddingInMain=true is a transitional state**: If the v31 migration ran but `memory.db` was backed up pre-v31 and restored, `embeddingInMain` will incorrectly be true. The code reads embeddings from `main.engrams.embedding` when this flag is set, which would return NULLs for rows that were migrated. Production databases should have `embeddingInMain=false` after v31 completes.

- **Recomputable files start empty, degrade gracefully**: On a fresh deploy without the vectors/cache files, `vecAvailable` may be false (vec0 tables not yet created), and all pyramid summary fetches return nil (callers fall back to raw summary text). The system works at reduced performance until the background indexing catches up.

- **StorageConfig paths can split across disks**: `VectorsPath` and `CachePath` can point to a different filesystem than `Path`. This is occasionally useful in production to put the recomputable files on a fast NVMe while keeping `memory.db` on a more durable storage tier, but the same ATTACH mechanism works regardless.

## Start Here

- `internal/graph/db.go` (`DB`, `Open`) — the authoritative source for the three-DB structure, ATTACH logic, MaxOpenConns(1) reasoning, and the v31 migration; read this first
- `config/config.go` (`StorageConfig`, `ResolvedCompressionLLM`) — path configuration and LLM resolution; shows how paths default and how per-function LLM configs layer over the deprecated `llm` key
- `cmd/engram/main.go` — startup wiring; shows the call to `graph.Open` and how the single `*DB` is passed to all components
- `internal/graph/compression.go` — all cache-schema write operations (pyramid generation); cross-references with `db.go` to understand which connection each write uses
- `internal/graph/activation.go` (`findSimilarEngramsVec`) — vectors-schema read path; shows the L2-on-normalized-float32 pattern and how KNN rowids map back to engram IDs
