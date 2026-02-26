# API Reference

## REST API

If `server.api_key` is set in config, all `/v1/*` endpoints require `Authorization: Bearer <api_key>`. If no key is configured, auth is disabled and requests are accepted without a header — suitable for local or trusted-network deployments. The `/health` endpoint is always public.

A full OpenAPI 3.0 specification is at [`openapi.yaml`](../openapi.yaml).

### Endpoint summary

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Service health check (public) |
| `POST` | `/v1/episodes` | Ingest a raw episode |
| `GET` | `/v1/episodes` | List episodes; `?channel=`, `?unconsolidated=`, `?before={id}`, `?level=N` |
| `POST` | `/v1/episodes/search` | Text or ID-lookup search over episodes |
| `GET` | `/v1/episodes/count` | Episode count; `?channel=`, `?unconsolidated=` filters |
| `GET` | `/v1/episodes/{id}` | Get episode by ID or 5-char prefix; `?level=N`, `?detail=full` |
| `POST` | `/v1/episodes/summaries` | Batch fetch pyramid summaries for episode IDs |
| `POST` | `/v1/episodes/{id}/edges` | Add a typed edge between two episodes |
| `POST` | `/v1/thoughts` | Ingest a free-form thought (shorthand for episodes) |
| `GET` | `/v1/engrams` | List engrams; `?threshold=` filter |
| `POST` | `/v1/engrams/search` | Semantic search or ID-lookup; returns `schema_ids` per engram |
| `GET` | `/v1/engrams/{id}` | Get engram by ID; `?level=N` for pyramid compression |
| `DELETE` | `/v1/engrams/{id}` | Delete an engram |
| `GET` | `/v1/engrams/{id}/context` | Engram + source episodes + linked entities |
| `POST` | `/v1/engrams/{id}/reinforce` | Boost activation and optionally blend embedding |
| `POST` | `/v1/engrams/boost` | Batch activation boost |
| `GET` | `/v1/entities` | List entities; `?type=` filter |
| `POST` | `/v1/entities/search` | Text search over entity names and aliases |
| `GET` | `/v1/entities/{id}` | Get entity by canonical ID |
| `GET` | `/v1/entities/{id}/engrams` | All engrams linked to an entity |
| `GET` | `/v1/schemas` | List all schemas |
| `POST` | `/v1/schemas/search` | ID-lookup returning precomputed schema summaries |
| `GET` | `/v1/schemas/{id}` | Get schema by ID; `?level=N` for precomputed summary |
| `DELETE` | `/v1/schemas/{id}` | Delete a schema |
| `POST` | `/v1/schemas/induce` | Trigger schema induction from L2+ engrams (async) |
| `POST` | `/v1/consolidate` | Trigger consolidation pipeline manually |
| `POST` | `/v1/activation/decay` | Apply exponential decay to all engram activations |
| `POST` | `/v1/memory/flush` | Alias for `/v1/consolidate` |
| `DELETE` | `/v1/memory/reset` | **Destructive** — wipe all memory |

---

### Health

```
GET /health
```

Response:
```json
{"status": "ok", "time": "2026-02-21T07:00:00Z"}
```

---

### Response verbosity

All read endpoints return **minimal responses by default** — just the fields needed to identify and display each object:

| Type | Default fields |
|------|---------------|
| Engram | `id`, `summary` |
| Episode | `id`, `content` |
| Entity | `id`, `name` |

Add `?detail=full` to any endpoint to get all fields. Embedding vectors are never returned.

---

### Ingest

#### `POST /v1/episodes`

Ingest a raw episode (message, event, observation).

Request:
```json
{
  "content": "Alice mentioned she prefers morning meetings.",
  "source": "discord",
  "author": "alice",
  "author_id": "u123",
  "channel": "general",
  "timestamp_event": "2026-02-21T09:00:00Z",
  "reply_to": "a3f2b9c1"
}
```

`author`, `author_id`, `channel`, `timestamp_event`, and `reply_to` are optional. If an embedding server is configured, the embedding is computed automatically. Named entities are extracted in the background if NER is configured.

Response `201`:
```json
{"id": "a3f2b9c1d4e7f0a2b5c8d1e4f7a0b3c6"}
```

#### `POST /v1/thoughts`

Shorthand for ingesting a free-form thought with `source: "thought"`. When `identity` is configured, `author` and `author_id` are set automatically.

Request:
```json
{"content": "Need to follow up with Bob about the project deadline."}
```

Response `201`:
```json
{"id": "b5c8d1e4f7a0b3c6a9f2b9c1d4e7f0a2"}
```

#### `POST /v1/episodes/{id}/edges`

Add a typed edge between two episodes.

Request:
```json
{
  "to_id": "b5c8d1e4f7a0b3c6",
  "edge_type": "REPLIES_TO",
  "confidence": 0.9
}
```

Edge types: `REPLIES_TO`, `FOLLOWS`, `RELATED_TO`.

---

### Episodes

#### `GET /v1/episodes`

List episodes. Returns `[{id, content}]` by default.

Query params:
- `channel` — filter by channel value
- `unconsolidated=true` — only return episodes not yet part of any engram
- `before={id}` — return only episodes older than the given episode ID (full or 5-char prefix); used for cursor-based pagination. Returns `400` if the ID is not found.
- `level=N` — apply pyramid compression to the `content` field before returning. Same levels as engrams: `4`, `8`, `16`, `32`, `64`. Episodes without a pre-generated summary at the requested level return raw content.
- `detail=full` — return all fields (applies after `?level=N` compression)
- `limit` — max results (default 50)

All filters compose freely: `?channel=X&unconsolidated=true&before={id}&level=8` is a valid combination.

#### `POST /v1/episodes/search`

Search over episode content. Returns `[{id, content}]` by default. Supports two modes:

**Text search mode:**
```json
{"query": "morning standup", "limit": 10, "detail": "full", "level": 8}
```

- `query` — required in text mode; substring search over episode content
- `limit` — max results (default 10)
- `detail` — set to `"full"` for all fields
- `level` — pyramid compression level applied to returned content

**ID lookup mode:**
```json
{"ids": ["a3f2b9c1", "b5c8d1e4"], "level": 8}
```

- `ids` — list of episode IDs (full or 5-char prefix) to fetch in bulk
- `level` — compression level applied to returned content
- Preserves the order of the `ids` list; unknown IDs are silently skipped

#### `GET /v1/episodes/{id}`

Get an episode by full ID or 5-char short ID (e.g. `a3f2b`). Returns `{id, content}` by default.

Query params:
- `level=N` — apply pyramid compression to the `content` field before returning. Same levels as the list endpoint: `4`, `8`, `16`, `32`, `64`. Episodes without a pre-generated summary at the requested level return raw content.
- `detail=full` — return all fields (applies after `?level=N` compression)

#### `GET /v1/episodes/count`

Returns the episode count matching optional filters.

Query params:
- `channel` — filter by channel
- `unconsolidated=true` — count only unconsolidated episodes

| Request | Returns |
|---------|---------|
| `/v1/episodes/count` | Total episode count |
| `/v1/episodes/count?unconsolidated=true` | Global unconsolidated count |
| `/v1/episodes/count?channel=X` | Total for channel |
| `/v1/episodes/count?channel=X&unconsolidated=true` | Unconsolidated count for channel |

Response: `{"count": 1482}`

#### `POST /v1/episodes/summaries`

Batch fetch pyramid summaries for a list of episode IDs.

Request:
```json
{"episode_ids": ["a3f2b9c1", "b5c8d1e4"], "level": 8}
```

Response: map of episode ID → summary string. Episodes with no pre-generated summary at the requested level are absent from the result.

```json
{"a3f2b9c1...": "Alice prefers mornings", "b5c8d1e4...": "Bob deadline follow-up"}
```

---

### Conversation context window

A bot maintaining a tiered conversation buffer queries episodes in tranches — most recent raw, older compressed, unconsolidated-only beyond that:

```bash
# 1. Most recent 10 episodes — raw
GET /v1/episodes?channel=guild:general&limit=10

# 2. Episodes 11–30 — compressed to ~8 words each
#    (use the ID of the oldest episode from step 1 as cursor)
GET /v1/episodes?channel=guild:general&limit=20&before={ep10_id}&level=8

# 3. Up to 70 unconsolidated episodes beyond the recent window — compressed
#    (use the ID of the oldest episode from step 2 as cursor)
GET /v1/episodes?channel=guild:general&limit=70&before={ep30_id}&unconsolidated=true&level=8

# Optional: check buffer size before fetching
GET /v1/episodes/count?channel=guild:general&unconsolidated=true
```

The third query returns an empty list once all older episodes have been consolidated — they are then reachable via `POST /v1/engrams/search` (spreading activation search). Setting `consolidation.max_buffer` to match the bot's fetch limit (e.g. `100`) ensures episodes are always accessible via one path or the other.

---

### Engrams

#### `GET /v1/engrams`

List consolidated engrams. Returns `[{id, summary}]` by default.

Query params:
- `detail=full` — return all fields
- `level` — pyramid compression level applied to every returned engram (default `0` = verbatim)
- `threshold` — filter by minimum activation level

#### `POST /v1/engrams/search`

Search or ID-lookup for engrams. Returns `[{id, summary, schema_ids}]` by default. Supports two modes:

**Semantic search mode:**
```json
{"query": "Alice meeting preferences", "limit": 10, "detail": "full", "level": 0}
```

- `query` — required in semantic mode; natural language search. Seeds spreading activation via semantic KNN, lexical BM25, and entity matching.
- `limit` — max results (default 10)
- `detail` — set to `"full"` for all fields
- `level` — pyramid compression level applied to returned engrams

**ID lookup mode:**
```json
{"ids": ["a3f2b9c1", "b5c8d1e4"], "level": 32}
```

- `ids` — list of engram IDs (full or 5-char prefix) to fetch in bulk
- `level` — compression level applied to returned summaries
- Preserves the order of the `ids` list; unknown IDs are silently skipped

Both modes populate `schema_ids` — the IDs of any schemas associated with each returned engram. Use `POST /v1/schemas/search` with those IDs to retrieve compact schema summaries for context assembly.

#### `GET /v1/engrams/{id}`

Get an engram by full ID or 5-char short ID. Returns `{id, summary}` by default.

Query param `level` selects a pyramid summary:

| `level` | Description |
|---------|-------------|
| `0` | Verbatim full summary (default) |
| `4` | ~4-word summary |
| `8` | ~8-word summary |
| `16` | ~16-word summary |
| `32` | ~32-word summary |
| `64` | ~64-word summary |

Any positive integer is accepted; the nearest available level is returned.

Add `?detail=full` to include all fields alongside the (possibly compressed) summary.

#### `DELETE /v1/engrams/{id}`

Delete an engram by full ID or 5-char short ID.

Response `204`: No content.

#### `GET /v1/engrams/{id}/context`

Get an engram with its source episodes and linked entities. Verbosity follows `?detail=full` as on other endpoints.

Response:
```json
{
  "engram": {"id": "...", "summary": "..."},
  "source_episodes": [{"id": "...", "content": "..."}],
  "linked_entities": [{"id": "person:alice", "name": "Alice"}]
}
```

#### `POST /v1/engrams/{id}/reinforce`

Boost an engram's activation and optionally blend its embedding (simulates memory reconsolidation on re-exposure).

Request (all optional):
```json
{"embedding": [...], "alpha": 0.3}
```

`alpha` defaults to `0.3`. Higher values = stronger pull toward the new embedding.

#### `POST /v1/engrams/boost`

Batch boost activations for multiple engrams.

Request:
```json
{"engram_ids": ["a3f2b9c1", "b5c8d1e4"], "boost": 0.1, "threshold": 0.0}
```

- `engram_ids` — required; list of engram IDs to boost
- `boost` — additive activation increase (default `0.1`)
- `threshold` — optional; only boost engrams above this activation level

---

### Entities

#### `GET /v1/entities`

List extracted named entities. Returns `[{id, name}]` by default.

Query params:
- `detail=full` — return all fields
- `type` — filter by entity type
- `level` — pyramid compression level (same semantics as engrams)
- `limit` — max results (default 100)

#### `POST /v1/entities/search`

Text search over entity names and aliases. Returns `[{id, name}]` by default.

Request:
```json
{"query": "Alice", "limit": 10, "detail": "full", "level": 0}
```

- `query` — required; text search over entity names and aliases
- `limit` — max results (default 10)
- `detail` — set to `"full"` for all fields
- `level` — pyramid compression level applied to returned entities

#### `GET /v1/entities/{id}`

Get an entity by canonical ID (e.g. `person:alice`). Returns `{id, name}` by default.

Add `?level=N` for a pyramid summary. Add `?detail=full` for all fields.

#### `GET /v1/entities/{id}/engrams`

Get all engrams linked to an entity.

Response:
```json
[{"id": "...", "summary": "..."}, ...]
```

**Entity types** (OntoNotes + extensions):

| Type | Description |
|------|-------------|
| `PERSON` | People, including fictional |
| `ORG` | Organizations |
| `GPE` | Geopolitical entities (countries, cities, states) |
| `LOC` | Non-GPE locations |
| `FAC` | Facilities (buildings, airports) |
| `PRODUCT` | Products |
| `EVENT` | Named events |
| `WORK_OF_ART` | Titles of books, songs, etc. |
| `TECHNOLOGY` | Software, frameworks, AI models _(custom)_ |
| `EMAIL` | Email addresses _(custom)_ |
| `PET` | Pet names _(custom)_ |
| `DATE`, `TIME`, `MONEY`, `PERCENT`, `QUANTITY`, `CARDINAL`, `ORDINAL` | Numeric / temporal |
| `OTHER` | Unclassified |

---

### Schemas

Schemas are recurring behavioural patterns extracted from L2+ engrams via LLM-based induction. Each schema captures a generalization: a problem type, a typical approach, and what has worked or not. They are precomputed at multiple compression levels for efficient context injection.

#### `GET /v1/schemas`

List all schemas ordered by most recently updated. Returns `[{id, name, content, created_at, updated_at}]`.

#### `POST /v1/schemas/search`

Fetch compact schema summaries for a list of IDs. This is the primary path for context assembly — call it with the `schema_ids` collected from engram search results to inject relevant patterns into your prompt.

Request:
```json
{"ids": ["a3f2b", "c9d1e"], "level": 32}
```

- `ids` — required; list of schema IDs (full UUID or 5-char prefix)
- `level` — precomputed compression level to return (default `32`). Valid levels: `4`, `8`, `16`, `32`, `64`.

Response: array of `{id, name, summary, level}`, in `ids` order. Unknown IDs are silently skipped. Falls back to the schema `name` if no precomputed summary exists at the requested level.

```json
[
  {"id": "a3f2b...", "name": "Memory System Debugging", "summary": "...", "level": 32},
  {"id": "c9d1e...", "name": "Async Pipeline Design", "summary": "...", "level": 32}
]
```

Note: text search mode (`{"query": "..."}`) is not yet implemented — use ID lookup only.

#### `GET /v1/schemas/{id}`

Get a single schema by full UUID or 5-char prefix.

Query params:
- `level=N` — return a precomputed summary instead of the full content. Valid levels: `4`, `8`, `16`, `32`, `64`. Falls back to full content if no summary exists at the requested level.

Response: full schema object `{id, name, content, is_labile, created_at, updated_at}`. With `?level=N`, the `content` field is replaced with the precomputed summary.

#### `DELETE /v1/schemas/{id}`

Delete a schema. Returns `{"status": "deleted"}`.

#### `POST /v1/schemas/induce`

Trigger schema induction asynchronously from L2+ engrams. Engram runs this automatically after each consolidation cycle if enough L2+ engrams exist; use this endpoint to force an immediate run (e.g. after first deployment to backfill summaries for existing schemas).

Response `202` if started:
```json
{"started": true}
```

Response `200` if skipped (not enough L2+ engrams):
```json
{"started": false, "reason": "not enough L2+ engrams (need at least 3)"}
```

Returns `503` if schema induction is not configured (no LLM).

---

### Consolidation

#### `POST /v1/consolidate`

Trigger the consolidation pipeline manually. Clusters recent episodes by semantic similarity, generates summaries via LLM, and promotes clusters to engrams.

Response `200`:
```json
{"engrams_created": 3, "duration_ms": 1240}
```

Returns `503` if consolidation is not configured.

#### `POST /v1/memory/flush`

Alias for `POST /v1/consolidate`.

---

### Activation

#### `POST /v1/activation/decay`

Trigger an immediate exponential decay pass over all engram activations. Engram runs this automatically in the background on the configured `decay.interval` (default: every hour) — clients no longer need to schedule it. Use this endpoint to force an immediate pass, or to apply custom lambda/floor values for a one-off run. Operational engrams decay at 3× the base rate.

Request (all optional; omit to use server-configured defaults):
```json
{"lambda": 0.01, "floor": 0.05}
```

Response:
```json
{"updated": 47}
```

---

### Management

#### `DELETE /v1/memory/reset`

**Destructive.** Clears all episodes, entities, engrams, and edges. Cannot be undone.

---

## MCP Server

Enable alongside the REST API with `ENGRAM_MCP=1`:

```bash
ENGRAM_MCP=1 ./engram --config engram.yaml
```

Engram serves MCP over stdio (for Claude Desktop / Claude Code) while the REST server continues on the configured port.

### Claude Desktop

Add to `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "engram": {
      "command": "/path/to/engram",
      "args": ["--config", "/path/to/engram.yaml"],
      "env": {
        "ENGRAM_MCP": "1",
        "ANTHROPIC_API_KEY": "sk-ant-..."
      }
    }
  }
}
```

### Claude Code

Add to `.mcp.json` in your project root:

```json
{
  "mcpServers": {
    "engram": {
      "type": "stdio",
      "command": "/path/to/engram",
      "args": ["--config", "/path/to/engram.yaml"],
      "env": {
        "ENGRAM_MCP": "1",
        "ANTHROPIC_API_KEY": "sk-ant-..."
      }
    }
  }
}
```

### MCP tools

| Tool | Description |
|------|-------------|
| `search_memory` | Semantic search via spreading activation — primary retrieval tool |
| `list_engrams` | List all consolidated engrams |
| `get_engram` | Get an engram by ID; supports `level` for pyramid compression |
| `get_engram_context` | Get an engram with its source episodes and linked entities |
| `query_episode` | Get a raw episode by ID |
