# Implementation Plan: BLAKE3 IDs + Traces → Engrams

Two changes land together since both touch the schema and all call sites:
1. Replace dual-ID system with single BLAKE3 IDs for episodes and engrams
2. Rename `traces` → `engrams` throughout

**Total scope:** ~15 files, ~40 function renames, 1 new dependency.

## Status

**Completed in commit `b753bb8`:** Steps 1–15 (all code changes, DB migration, tests pass)

**Remaining:** Steps 16 (openapi.yaml — 31 trace refs), 17 (README.md — 23 trace refs)

---

## Preflight Notes (verified against codebase)

- **Step 1 is already done.** `github.com/zeebo/blake3 v0.2.4` is already in `go.mod`/`go.sum`. It's used in `episodes.go` for `generateShortID`. Skip the `go get` — just remove that helper.

- **Episode IDs are generated in handlers, not in `AddEpisode`.** Both `handleIngestEpisode` (handlers.go:68) and `handleIngestThought` (handlers.go:128) do `id := fmt.Sprintf("ep-%s", uuid.New().String())`. Step 7 should say to update the *handlers* to call `graph.GenerateEpisodeID(content, source, timestampEvent.UnixNano())` — `AddEpisode` itself stays ID-agnostic (caller provides it).

- **The ingest response includes `"short_id"`** (handlers.go:109). Remove this from the response in Step 12.

- **Migration target is v21.** Current schema is at version 20 (confirmed in db.go).

---

## ✅ Step 1: Add BLAKE3 Dependency

Already done — `github.com/zeebo/blake3 v0.2.4` is present in `go.mod`. No action needed.

~~```bash
go get github.com/zeebo/blake3
```~~

---

## ✅ Step 2: Create `internal/graph/id.go`

New file containing all ID generation and prefix resolution logic. Nothing else should generate IDs.

```go
package graph

import (
    "database/sql"
    "fmt"
    "strconv"

    "github.com/zeebo/blake3"
)

// GenerateEpisodeID returns a 32-char BLAKE3 hex ID for an episode.
// Input: content + source + created_at (nanoseconds).
func GenerateEpisodeID(content, source string, createdAtNs int64) string {
    h := blake3.New()
    h.Write([]byte(content))
    h.Write([]byte(source))
    h.Write([]byte(strconv.FormatInt(createdAtNs, 10)))
    sum := h.Sum(nil)
    return fmt.Sprintf("%x", sum[:16]) // 128 bits = 32 hex chars
}

// GenerateEngramID returns a 32-char BLAKE3 hex ID for a consolidated engram.
func GenerateEngramID(content string, createdAtNs int64) string {
    h := blake3.New()
    h.Write([]byte(content))
    h.Write([]byte(strconv.FormatInt(createdAtNs, 10)))
    sum := h.Sum(nil)
    return fmt.Sprintf("%x", sum[:16])
}

// ResolveID resolves a full or prefix ID against the given table.
// Returns the full 32-char ID or an error.
func ResolveID(db *sql.DB, table, prefix string) (string, error) {
    if len(prefix) == 32 {
        var id string
        err := db.QueryRow("SELECT id FROM "+table+" WHERE id = ?", prefix).Scan(&id)
        if err == sql.ErrNoRows {
            return "", fmt.Errorf("not found: %s", prefix)
        }
        return id, err
    }

    upper := nextHexPrefix(prefix)
    var query string
    var args []any
    if upper == "" {
        query = "SELECT id FROM " + table + " WHERE id >= ? LIMIT 3"
        args = []any{prefix}
    } else {
        query = "SELECT id FROM " + table + " WHERE id >= ? AND id < ? LIMIT 3"
        args = []any{prefix, upper}
    }

    rows, err := db.Query(query, args...)
    if err != nil {
        return "", err
    }
    defer rows.Close()

    var matches []string
    for rows.Next() {
        var id string
        rows.Scan(&id)
        matches = append(matches, id)
    }

    switch len(matches) {
    case 0:
        return "", fmt.Errorf("not found: %s", prefix)
    case 1:
        return matches[0], nil
    default:
        return "", &AmbiguousRefError{Ref: prefix, Matches: matches}
    }
}

// AmbiguousRefError is returned when a prefix matches more than one ID.
type AmbiguousRefError struct {
    Ref     string
    Matches []string
}

func (e *AmbiguousRefError) Error() string {
    return fmt.Sprintf("ambiguous ref: %s matches %d objects", e.Ref, len(e.Matches))
}

// nextHexPrefix returns the smallest string greater than all strings
// with the given prefix. Returns "" if the prefix is all 'f' chars (open upper bound).
func nextHexPrefix(prefix string) string {
    b := []byte(prefix)
    for i := len(b) - 1; i >= 0; i-- {
        if b[i] < 'f' {
            b[i]++
            return string(b[:i+1])
        }
    }
    return "" // all 'f' — open upper bound
}
```

---

## ✅ Step 3: Database Schema Migration (`internal/graph/db.go`)

Add a new migration (increment the current highest version, e.g. `v21`) that:

### 3a. Drop old trace tables

```sql
DROP TABLE IF EXISTS trace_fts;
DROP TABLE IF EXISTS trace_vec;
DROP TABLE IF EXISTS trace_summaries;
DROP TABLE IF EXISTS trace_sources;
DROP TABLE IF EXISTS trace_entities;
DROP TABLE IF EXISTS trace_relations;
DROP TABLE IF EXISTS episode_trace_edges;
DROP TABLE IF EXISTS traces;
```

### 3b. Remove `short_id` from `episodes`

Since SQLite doesn't support `DROP COLUMN` before 3.35, use the standard table-rebuild approach:

```sql
CREATE TABLE episodes_new (
    id         TEXT PRIMARY KEY CHECK(length(id) = 32),
    content    TEXT NOT NULL,
    token_count INTEGER DEFAULT 0,
    source     TEXT NOT NULL DEFAULT '',
    author     TEXT DEFAULT '',
    author_id  TEXT DEFAULT '',
    channel    TEXT DEFAULT '',
    timestamp_event    DATETIME NOT NULL,
    timestamp_ingested DATETIME NOT NULL,
    dialogue_act  TEXT,
    entropy_score REAL,
    embedding     BLOB,
    reply_to      TEXT,
    authorization_checked INTEGER DEFAULT 0,
    has_authorization     INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO episodes_new SELECT
    id, content, token_count, source, author, author_id, channel,
    timestamp_event, timestamp_ingested, dialogue_act, entropy_score,
    embedding, reply_to, authorization_checked, has_authorization, created_at
FROM episodes;
DROP TABLE episodes;
ALTER TABLE episodes_new RENAME TO episodes;
-- Recreate indexes
CREATE INDEX idx_episodes_timestamp ON episodes(timestamp_event);
CREATE INDEX idx_episodes_channel ON episodes(channel);
CREATE INDEX idx_episodes_author ON episodes(author);
CREATE INDEX idx_episodes_reply_to ON episodes(reply_to);
```

> Note: If the existing `episodes.id` is already TEXT and values happen to be 32-char BLAKE3, the INSERT above works as-is. If old rows have non-conformant IDs (short_ids or integers), the migration must compute BLAKE3 on the fly — see the note at end of this file.

### 3c. Create `engrams` table

```sql
CREATE TABLE engrams (
    id           TEXT PRIMARY KEY CHECK(length(id) = 32),
    summary      TEXT,
    topic        TEXT,
    engram_type  TEXT DEFAULT 'knowledge',
    activation   REAL DEFAULT 0.5,
    strength     INTEGER DEFAULT 1,
    embedding    BLOB,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_accessed DATETIME,
    labile_until  DATETIME,
    needs_reconsolidation BOOLEAN DEFAULT 0
);
CREATE INDEX idx_engrams_activation ON engrams(activation);
CREATE INDEX idx_engrams_last_accessed ON engrams(last_accessed);
CREATE INDEX idx_engrams_type ON engrams(engram_type);
```

### 3d. Create `engram_summaries` table

```sql
CREATE TABLE engram_summaries (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    engram_id        TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
    compression_level INTEGER NOT NULL,
    summary          TEXT NOT NULL,
    tokens           INTEGER NOT NULL,
    created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(engram_id, compression_level)
);
```

### 3e. Create `engram_episodes` junction table

```sql
CREATE TABLE engram_episodes (
    engram_id  TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
    episode_id TEXT NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    PRIMARY KEY (engram_id, episode_id)
);
CREATE INDEX idx_engram_episodes_episode ON engram_episodes(episode_id);
```

### 3f. Create `engram_entities` junction table

```sql
CREATE TABLE engram_entities (
    engram_id TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
    entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    PRIMARY KEY (engram_id, entity_id)
);
```

### 3g. Create `engram_relations` table

```sql
CREATE TABLE engram_relations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    from_id      TEXT NOT NULL REFERENCES engrams(id),
    to_id        TEXT NOT NULL REFERENCES engrams(id),
    relation_type TEXT NOT NULL,
    weight       REAL DEFAULT 1.0
);
```

### 3h. Create `episode_engram_edges` table

```sql
CREATE TABLE episode_engram_edges (
    episode_id        TEXT NOT NULL REFERENCES episodes(id),
    engram_id         TEXT NOT NULL REFERENCES engrams(id),
    relationship_desc TEXT NOT NULL,
    confidence        REAL DEFAULT 1.0,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (episode_id, engram_id)
);
```

### 3i. Create FTS and vector tables for engrams

```sql
CREATE VIRTUAL TABLE engram_fts USING fts5(
    engram_id UNINDEXED,
    summary,
    content=engram_summaries,
    content_rowid=id
);

-- Vector table created at runtime once embedding dimension is known
-- (same pattern as existing trace_vec initialization)
```

---

## ✅ Step 4: Rename `internal/graph/types.go`

### Changes

- `Trace` struct → `Engram`
- `TraceType` → `EngramType`
- `TraceTypeKnowledge` → `EngramTypeKnowledge`
- `TraceTypeOperational` → `EngramTypeOperational`
- Remove `ShortID` field from `Episode` struct
- Remove `ShortID` field from `Engram` (was `Trace`) struct
- Update field `TraceType` → `EngramType` inside `Engram`
- Update JSON tags: `"trace_type"` → `"engram_type"`, `"short_id"` removed
- Update any `RetrievalResult` or response structs that embed `[]Trace` → `[]Engram`

---

## ✅ Step 5: Rename `internal/graph/traces.go` → `engrams.go`

Rename file, then update all function signatures and SQL:

| Old Name | New Name |
|----------|----------|
| `AddTrace` | `AddEngram` |
| `GetTrace` | `GetEngram` |
| `GetTraceByShortID` | (delete) |
| `GetAllTraces` | `GetAllEngrams` |
| `DeleteTrace` | `DeleteEngram` |
| `UpdateTrace` | `UpdateEngram` |
| `GetActivatedTraces` | `GetActivatedEngrams` |
| `GetTracesBatch` | `GetEngramsBatch` |
| `GetTracesBatchAtLevel` | `GetEngramsBatchAtLevel` |
| `UpdateTraceActivation` | `UpdateEngramActivation` |
| `ReinforceTrace` | `ReinforceEngram` |
| `DecayActivation` | (keep name — operates on engrams table) |
| `DecayActivationByAge` | (keep name) |
| `BoostTraceAccess` | `BoostEngramAccess` |
| `LinkTraceToSource` | `LinkEngramToSource` |
| `LinkTraceToEntity` | `LinkEngramToEntity` |
| `AddTraceRelation` | `AddEngramRelation` |
| `GetTraceNeighbors` | `GetEngramNeighbors` |
| `GetTraceNeighborsBatch` | `GetEngramNeighborsBatch` |
| `GetTraceEntities` | `GetEngramEntities` |
| `GetTraceSources` | `GetEngramSources` |
| `GetEpisodeTraces` | `GetEpisodeEngrams` |
| `MarkTraceForReconsolidation` | `MarkEngramForReconsolidation` |
| `GetTracesNeedingReconsolidation` | `GetEngramsNeedingReconsolidation` |
| `ClearReconsolidationFlag` | (keep name) |

**SQL table name changes in all queries:**
- `traces` → `engrams`
- `trace_summaries` → `engram_summaries`
- `trace_sources` → `engram_episodes`
- `trace_entities` → `engram_entities`
- `trace_relations` → `engram_relations`
- `trace_fts` → `engram_fts`
- `trace_vec` → `engram_vec`
- Column: `trace_id` → `engram_id`

**ID generation in `AddEngram`:**
- Remove `short_id` generation
- Call `GenerateEngramID(content, createdAtNs)` to produce the `id`

---

## ✅ Step 6: Rename `internal/graph/episode_trace_edges.go` → `episode_engram_edges.go`

| Old Name | New Name |
|----------|----------|
| `AddEpisodeTraceEdge` | `AddEpisodeEngramEdge` |
| `GetEpisodeTraceEdges` | `GetEpisodeEngramEdges` |
| (any other functions in file) | Update similarly |

All SQL queries: `episode_trace_edges` → `episode_engram_edges`, `trace_id` → `engram_id`.

---

## ✅ Step 7: Update `internal/graph/episodes.go`

- **Remove `generateShortID`** — delete the function entirely.
- **Remove `ShortID` field usage** — remove all `ep.ShortID` assignments and `short_id` SQL column references from `AddEpisode`, `GetAllEpisodes`, `GetEpisode`, `GetEpisodeByShortID` (delete that function), `GetEpisodes`, `GetRecentEpisodes`, `GetEpisodeReplies`, `scanEpisode`, `scanEpisodeRow`, `scanEpisodeRowNoEmbedding`.
- **ID generation stays in the handler** (see Step 12) — `AddEpisode` remains caller-provided-ID; just enforce `if ep.ID == ""` check.
- Update any SQL that references `trace_sources` → `engram_episodes`.
- Update `GetUnconsolidatedEpisodeCount`, `GetUnconsolidatedEpisodeIDsForChannel`, `GetUnconsolidatedEpisodes`, `GetConsolidatedEpisodesWithEmbeddings` — all join `trace_sources` → `engram_episodes`, column `trace_id` → `engram_id`.

---

## ✅ Step 8: Update `internal/graph/activation.go`

All spreading activation logic references `traces`. Update:

- Every SQL query: `traces` → `engrams`, `trace_id` → `engram_id`, junction table names
- Every Go variable and type reference: `Trace` → `Engram`, function calls updated to new names
- The vec table reference: `trace_vec` → `engram_vec`
- Return types: `[]Trace` → `[]Engram`

This file is the largest trace consumer (~1,047 lines). Do a careful search-replace and verify each query.

---

## ✅ Step 9: Update `internal/graph/entities.go`

- Any query that joins to `traces` or `trace_entities` → update to `engrams` / `engram_entities`
- Return types or structs that embed trace data

---

## ✅ Step 10: Update `internal/consolidate/consolidate.go` (and related)

The consolidation pipeline creates traces. Update:

- `AddTrace(...)` → `AddEngram(...)`
- `Trace` type references → `Engram`
- `GetTracesNeedingReconsolidation` → `GetEngramsNeedingReconsolidation`
- `MarkTraceForReconsolidation` → `MarkEngramForReconsolidation`
- Any field access like `.TraceType` → `.EngramType`
- `consolidate_test.go` and `consolidate_integration_test.go`: same renames

---

## ✅ Step 11: Update `internal/api/router.go`

Route changes:

| Old Route | New Route |
|-----------|-----------|
| `GET /v1/traces` | `GET /v1/engrams` |
| `GET /v1/traces/{id}` | `GET /v1/engrams/{id}` |
| `GET /v1/traces/{id}/context` | `GET /v1/engrams/{id}/context` |
| `POST /v1/traces/{id}/reinforce` | `POST /v1/engrams/{id}/reinforce` |
| `POST /v1/traces/boost` | `POST /v1/engrams/boost` |

Update handler references to match new names from step 12.

---

## ✅ Step 12: Update `internal/api/handlers.go`

Handler function renames and JSON key changes:

| Old Name | New Name |
|----------|----------|
| `handleGetTraces` | `handleGetEngrams` |
| `handleGetTrace` | `handleGetEngram` |
| `handleGetTraceContext` | `handleGetEngramContext` |
| `handleReinforceTrace` | `handleReinforceEngram` |
| `handleBoostTraces` | `handleBoostEngrams` |

JSON response key changes:
- `"traces": [...]` → `"engrams": [...]`
- Any struct field `Traces` → `Engrams`
- Strip embedding from search response: response should be `{"engrams": [...]}` with embedding omitted (already done in previous commit — verify it targets `engrams` key after rename)

Graph function call sites: update all `GetAllTraces`, `GetTrace`, `ReinforceTrace`, etc. to new names.

Prefix resolution: in handlers that accept `{id}`, call `ResolveID(db, "engrams", id)` instead of direct lookup or short_id lookup.

**Episode ID generation (in `handleIngestEpisode` and `handleIngestThought`):** Replace:
```go
id := fmt.Sprintf("ep-%s", uuid.New().String())
```
with:
```go
id := graph.GenerateEpisodeID(req.Content, req.Source, req.TimestampEvent.UnixNano())
```
Remove the `github.com/google/uuid` import if it's no longer used after this change.

**Remove `"short_id"` from ingest response** (currently at handlers.go:109):
```go
// Remove this:
"short_id": ep.ShortID,
```

---

## ✅ Step 13: Update `internal/mcp/server.go`

Tool definition renames:

| Old Tool | New Tool |
|----------|----------|
| `list_traces` | `list_engrams` |
| `get_trace` | `get_engram` |
| `get_trace_context` | `get_engram_context` |

Parameter names inside tools:
- `trace_id` → `engram_id`
- All description strings mentioning "trace" → "engram"
- Call sites: `GetTrace(...)` → `GetEngram(...)`, etc.

---

## ✅ Step 14: Update `cmd/engram/main.go`

Check for any trace-specific startup logic, initialization calls, or log messages. Update as needed.

---

## ✅ Step 15: Update Tests

### `internal/graph/graph_test.go`
- All `AddTrace` → `AddEngram`
- All `GetTrace` → `GetEngram`
- All `Trace{...}` literals → `Engram{...}`
- All `.TraceType` → `.EngramType`
- All `trace_` table assertions in SQL strings

### `internal/graph/graph_crud_test.go`
- Same renames as above

### `internal/graph/retrieval_quality_test.go`
- Same renames; check spreading activation result types

### `internal/api/handlers_test.go`
- Route strings: `/v1/traces/` → `/v1/engrams/`
- Response JSON key assertions: `"traces"` → `"engrams"`

### `internal/consolidate/consolidate_test.go` and `consolidate_integration_test.go`
- All trace function call renames

---

## ⬜ Step 16: Update `openapi.yaml`

- Rename all `/v1/traces` paths to `/v1/engrams`
- Rename `Trace` schema → `Engram`
- Rename `TraceType` → `EngramType`
- Rename `trace_id` fields → `engram_id`
- Update descriptions

---

## ⬜ Step 17: Update `README.md` and other docs

- Replace "trace" with "engram" in conceptual descriptions
- Update curl examples to use `/v1/engrams`
- Update any references to `short_id`

---

## ✅ Step 18: Build and Verify

```bash
make build     # must produce zero errors, zero warnings about undefined symbols

# Run unit tests
go test ./internal/graph/... -tags fts5
go test ./internal/api/... -tags fts5
go test ./internal/consolidate/... -tags fts5

# Smoke test
./bin/engram &

curl -X POST http://localhost:8080/v1/episodes \
  -H "X-API-Key: ..." \
  -d '{"content": "test", "source": "test"}'

curl -X POST http://localhost:8080/v1/consolidate \
  -H "X-API-Key: ..."

curl -X POST http://localhost:8080/v1/search \
  -H "X-API-Key: ..." \
  -d '{"query": "test"}'

curl http://localhost:8080/v1/engrams \
  -H "X-API-Key: ..."
```

Expected: `/v1/engrams` returns engrams with 32-char BLAKE3 IDs; no `short_id` fields; search returns `{"engrams": [...]}`.

---

## Notes on Existing Data

The existing SQLite database (`state/system/memory.db`) has episodes and traces with the old ID scheme. **Do not migrate** — drop the old tables in the migration and start clean. If there's data you want to preserve, back up the database file before running the migration:

```bash
cp state/system/memory.db state/system/memory.db.backup
```

---

## Commit Sequence (suggested)

1. `chore: add blake3 dependency`
2. `feat: add ID generation and prefix resolution (id.go)`
3. `feat(db): migration vN - engrams schema, remove traces and short_ids`
4. `refactor: rename Trace→Engram types (types.go)`
5. `refactor: rename traces.go→engrams.go, all CRUD functions`
6. `refactor: rename episode_trace_edges→episode_engram_edges`
7. `refactor: update episodes, activation, entities, consolidation`
8. `refactor(api): /v1/traces→/v1/engrams, handler and response renames`
9. `refactor(mcp): list/get/context tool renames`
10. `test: update all test files for engram rename`
11. `docs: update openapi.yaml, README, id-design.md`
