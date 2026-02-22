# MCP Tools Reference

Engram exposes five tools via the [Model Context Protocol](https://modelcontextprotocol.io/) for use by Claude agents (Claude Desktop, Claude Code, or any MCP-compatible host).

Enable with `ENGRAM_MCP=1`. See [API reference](api.md#mcp-server) for connection setup.

---

## Tool summary

| Tool | Required params | Description |
|------|----------------|-------------|
| `search_memory` | `query` | Semantic search via spreading activation — the primary retrieval tool |
| `list_engrams` | — | List all engrams with IDs and summaries |
| `get_engram` | `engram_id` | Fetch a single engram by ID, with optional compression |
| `get_engram_context` | `engram_id` | Fetch an engram plus its source episodes and linked entities |
| `query_episode` | `id` | Fetch a raw source episode by ID |

---

## `search_memory`

The primary way to retrieve relevant memories. Runs a full spreading activation search: semantic vector similarity, BM25 full-text, and NER-entity seeding all run in parallel and their results are merged and propagated through the engram graph. Returns engrams ranked by activation score.

Use this when you want to answer "what do I know about X?" or surface context relevant to a situation.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | yes | Natural language query |
| `limit` | number | no | Max results to return (default: 10) |

**Returns:** Array of engrams ranked by relevance, each with `id`, `summary`, `engram_type`, and `activation`.

**Example:**
```
search_memory(query="Alice's preferences for team meetings", limit=5)
```

---

## `list_engrams`

Returns all engrams in the memory store. Useful for getting an overview of what has been consolidated, or when you want to enumerate memories without a specific query in mind.

**Parameters:** none

**Returns:** Array of all engrams with `id` and `summary`.

---

## `get_engram`

Fetches a single engram by its ID. Supports pyramid compression levels to retrieve summaries at different verbosity levels — useful when token budget is a concern.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `engram_id` | string | yes | Full 32-char ID or a 5-char prefix (e.g. `a3f2b`) |
| `level` | number | no | Compression level: `0` = verbatim, `4`/`8`/`16`/`32`/`64` = approximate word count (default: `0`) |

**Returns:** Engram object with `id`, `summary`, `engram_type`, `activation`, `strength`, and timestamps.

**Example:**
```
get_engram(engram_id="a3f2b", level=16)
```

---

## `get_engram_context`

Fetches an engram together with the raw source episodes it was consolidated from and the named entities linked to it. Use this when you need to verify or cite the underlying observations behind a memory, or when you want to understand who was involved.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `engram_id` | string | yes | Full 32-char ID or 5-char prefix |

**Returns:**
```json
{
  "engram": { "id": "...", "summary": "...", ... },
  "source_episodes": [{ "id": "...", "content": "...", "author": "...", ... }],
  "linked_entities": [{ "id": "person:alice", "name": "Alice", "type": "PERSON" }]
}
```

---

## `query_episode`

Fetches a single raw episode by its ID. Episodes are the unprocessed source observations — verbatim messages, events, or thoughts as originally ingested. Use this to look up the exact wording of something when an engram summary isn't sufficient.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `id` | string | yes | Full 32-char ID or 5-char prefix |

**Returns:** Episode object with `id`, `content`, `source`, `author`, `channel`, `timestamp_event`, and `dialogue_act`.

---

## Suggested usage patterns

**Answer a question from memory:**
1. `search_memory(query=<question or topic>)`
2. If a result looks relevant but you need more detail: `get_engram_context(engram_id=<id>)`

**Cite a specific source:**
1. `get_engram_context(engram_id=<id>)` to see source episodes
2. `query_episode(id=<episode_id>)` to get the verbatim original

**Browse available memory:**
1. `list_engrams()` to see all consolidated memories at a glance
2. `get_engram(engram_id=<id>)` to expand any that look relevant
