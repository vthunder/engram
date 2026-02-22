# API Reference

## REST API

All `/v1/*` endpoints require `Authorization: Bearer <api_key>`. The `/health` endpoint is public.

A full OpenAPI 3.0 specification is at [`openapi.yaml`](../openapi.yaml).

### Endpoint summary

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Service health check (public) |
| `POST` | `/v1/episodes` | Ingest a raw episode |
| `GET` | `/v1/episodes` | List episodes; `?query=` for substring search |
| `GET` | `/v1/episodes/count` | Total episode count |
| `GET` | `/v1/episodes/{id}` | Get episode by ID or 5-char prefix |
| `POST` | `/v1/episodes/summaries` | Batch fetch pyramid summaries for episode IDs |
| `POST` | `/v1/episodes/{id}/edges` | Add a typed edge between two episodes |
| `POST` | `/v1/thoughts` | Ingest a free-form thought (shorthand for episodes) |
| `GET` | `/v1/engrams` | List engrams; `?query=` triggers spreading activation |
| `GET` | `/v1/engrams/{id}` | Get engram by ID; `?level=N` for pyramid compression |
| `DELETE` | `/v1/engrams/{id}` | Delete an engram |
| `GET` | `/v1/engrams/{id}/context` | Engram + source episodes + linked entities |
| `POST` | `/v1/engrams/{id}/reinforce` | Boost activation and optionally blend embedding |
| `POST` | `/v1/engrams/boost` | Batch activation boost |
| `GET` | `/v1/entities` | List entities; `?query=`, `?type=` filters |
| `GET` | `/v1/entities/{id}` | Get entity by canonical ID |
| `GET` | `/v1/entities/{id}/engrams` | All engrams linked to an entity |
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
  "target_id": "b5c8d1e4f7a0b3c6",
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
- `query` — substring search over episode content
- `detail=full` — return all fields
- `limit` — max results (default 100; default 10 when using `?query=`)

#### `GET /v1/episodes/{id}`

Get an episode by full ID or 5-char short ID (e.g. `a3f2b`). Returns `{id, content}` by default.

Add `?detail=full` to include all fields.

#### `GET /v1/episodes/count`

Returns the total episode count.

```json
{"count": 1482}
```

#### `POST /v1/episodes/summaries`

Batch fetch pyramid summaries for a list of episode IDs.

Request:
```json
{"ids": ["a3f2b9c1", "b5c8d1e4"], "level": 8}
```

Response:
```json
[
  {"id": "a3f2b9c1...", "summary": "Alice prefers mornings"},
  {"id": "b5c8d1e4...", "summary": "Bob deadline follow-up"}
]
```

---

### Engrams

#### `GET /v1/engrams`

List consolidated engrams. Returns `[{id, summary}]` by default.

Query params:
- `query` — semantic search via spreading activation; returns ranked results
- `detail=full` — return all fields
- `level` — pyramid compression level applied to every returned engram (default `0` = verbatim)
- `limit` — max results when using `?query=` (default 10)
- `threshold` — filter by minimum activation level (list-all mode only)

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

Response `200`:
```json
{"deleted": true}
```

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
{"ids": ["a3f2b9c1", "b5c8d1e4"], "boost": 0.1}
```

---

### Entities

#### `GET /v1/entities`

List extracted named entities. Returns `[{id, name}]` by default.

Query params:
- `query` — text search over entity names and aliases
- `detail=full` — return all fields
- `type` — filter by entity type
- `level` — pyramid compression level (same semantics as engrams)
- `limit` — max results (default 100; default 10 when using `?query=`)

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

Apply exponential decay to all engram activations. Run periodically to implement forgetting. Operational engrams decay at 3× the base rate.

Request (all optional):
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
