# Engram ID Design

## Goals

- Single ID per entity type (no dual ID system)
- Git-like prefix resolution in APIs and CLIs
- Human-readable canonical IDs for entities
- No external dependencies for ID generation

---

## Naming: Traces → Engrams

The consolidated memory objects are renamed from **traces** to **engrams** throughout the system.

**Rationale:** "Engram" is the neuroscience term for a consolidated memory trace stored in neural tissue — exactly what this system produces. The domain model maps cleanly:

| Concept | Role |
|---------|------|
| **Episodes** | Raw sensory/event records (input) |
| **Engrams** | Consolidated memories (the durable thing) |
| **Entities** | Extracted actors and concepts (graph layer) |

The relationship reads: _episodes consolidate into engrams_. This matches the actual memory science and is more evocative than a generic "trace".

---

## ID Strategy by Type

### Episodes and Engrams — BLAKE3 Content Hash (32 hex chars)

| Type | Hash Input |
|------|-----------|
| Episode | `content + source + created_at_ns` |
| Engram | `aggregated_content + created_at_ns` |

**Format:** 32 lowercase hex characters (128 bits of BLAKE3 output)

Example: `a3f2b8c1d4e9f0123456789abcdef012`

Same content ingested at different times produces different IDs (timestamp is part of input), so no accidental deduplication.

### Entities — Canonical Key (no hashing)

**Format:** `{lowercase_type}:{lowercase_name}`

Examples:
- `person:alice`
- `organization:anthropic`
- `location:amsterdam`

The canonical key *is* the identity. Hashing it would add nothing except obscuring the meaning. The ID is the deduplication key: two ingestions of "Alice" (PERSON) resolve to the same entity row.

**Validation rules:**
- `name` must be non-empty, printable ASCII/UTF-8, no colon characters
- `type` must be one of the known NER types (PERSON, ORG, LOC, etc.)

**Renaming:** changing an entity's name produces a new ID. An optional `aliases` column can track former names, but that's a future concern.

---

## Database Schema

### Episodes

```sql
CREATE TABLE episodes (
  id        TEXT PRIMARY KEY CHECK(length(id) = 32),
  content   TEXT NOT NULL,
  source    TEXT NOT NULL DEFAULT '',
  author    TEXT NOT NULL DEFAULT '',
  metadata  TEXT,              -- JSON blob
  embedding BLOB,
  created_at INTEGER NOT NULL  -- Unix nanoseconds
);
```

### Engrams

```sql
CREATE TABLE engrams (
  id           TEXT PRIMARY KEY CHECK(length(id) = 32),
  content      TEXT NOT NULL,
  summary_l1   TEXT,
  summary_l2   TEXT,
  summary_l4   TEXT,
  summary_l8   TEXT,
  summary_l16  TEXT,
  embedding    BLOB,
  created_at   INTEGER NOT NULL
);
```

### Entities

```sql
CREATE TABLE entities (
  id          TEXT PRIMARY KEY,  -- e.g. "person:alice"
  name        TEXT NOT NULL,
  type        TEXT NOT NULL,
  description TEXT,
  metadata    TEXT,              -- JSON blob
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
```

### Junction Tables

```sql
CREATE TABLE episode_entities (
  episode_id  TEXT NOT NULL REFERENCES episodes(id),
  entity_id   TEXT NOT NULL REFERENCES entities(id),
  PRIMARY KEY (episode_id, entity_id)
);

CREATE TABLE engram_episodes (
  engram_id  TEXT NOT NULL REFERENCES engrams(id),
  episode_id TEXT NOT NULL REFERENCES episodes(id),
  PRIMARY KEY (engram_id, episode_id)
);

CREATE TABLE engram_entities (
  engram_id TEXT NOT NULL REFERENCES engrams(id),
  entity_id TEXT NOT NULL REFERENCES entities(id),
  PRIMARY KEY (engram_id, entity_id)
);
```

---

## Prefix Resolution

All APIs and CLI tools accept any prefix of an episode or engram ID. Entity IDs (canonical keys) are not prefix-resolved — they must be provided exactly.

### Algorithm

```go
func resolveID(db *sql.DB, table, prefix string) (string, error) {
    if len(prefix) == 32 {
        // Full ID — direct lookup, no range scan needed
        return directLookup(db, table, prefix)
    }

    upper := nextHexPrefix(prefix) // "a3f2b" → "a3f2c"
    rows, _ := db.Query(
        "SELECT id FROM "+table+" WHERE id >= ? AND id < ? LIMIT 3",
        prefix, upper,
    )

    switch len(matches) {
    case 0: return "", fmt.Errorf("not found: %s", prefix)
    case 1: return matches[0], nil
    default: return "", &AmbiguousRefError{Ref: prefix, Matches: matches}
    }
}
```

`nextHexPrefix` increments the last hex digit, propagating carry. The one edge case (prefix ending in `f...f`) resolves to an open-ended query `WHERE id >= ?`.

### Error Responses

```json
// Not found
{ "error": "not found", "ref": "a3f2b" }

// Ambiguous (returns partial match list)
{
  "error": "ambiguous ref",
  "ref": "a3f2",
  "matches": ["a3f2b8c1d4e9f012...", "a3f24d9ef0123456..."]
}
```

APIs always return full 32-char IDs in responses.

---

## Display Convention

**Default display prefix: 5 hex chars**

5 chars = 16⁵ ≈ 1M combinations. Birthday collision risk is ~0.05% at 1,000 items — acceptable for a personal corpus for the foreseeable future. If the corpus grows significantly, the display prefix can be bumped to 6 without any schema change.

Rationale for 5 over git's typical 7: git repos routinely have hundreds of thousands of commits; a personal memory system is orders of magnitude smaller.

---

## API Routes

```
POST /v1/episodes              # ingest, returns full 32-char ID
GET  /v1/episodes/{id}         # id = full or prefix
GET  /v1/entities/{id}         # id = exact canonical key
GET  /v1/engrams               # list all engrams
GET  /v1/engrams/{id}          # id = full or prefix
GET  /v1/engrams/{id}/context  # engram + source episodes + linked entities
POST /v1/engrams/{id}/reinforce
POST /v1/engrams/boost
POST /v1/search
POST /v1/consolidate
```

---

## MCP Tool Conventions

| Old Name | New Name |
|----------|----------|
| `list_traces` | `list_engrams` |
| `get_trace` | `get_engram` |
| `get_trace_context` | `get_engram_context` |
| `query_trace` | `query_engram` |

Tools accept any ID prefix ≥ 4 chars. Documentation recommends 5+ to minimize ambiguity in practice.

The `memory_eval` format in `signal_done` uses 5-char BLAKE3 prefixes of engram IDs:
```json
{ "memory_eval": {"a3f2b": 5, "c91d4": 1} }
```

---

## Open Questions

- **Entity aliases:** When an entity is renamed, do we track the old canonical key as an alias? Probably a future feature.
- **Cross-type entity collision:** Is `person:paris` and `location:paris` a problem? No — type is part of the key, so they're distinct rows.
- **Minimum prefix length:** Should the API enforce a minimum (e.g., 4 chars) to prevent catastrophic ambiguity? Worth a config option.
