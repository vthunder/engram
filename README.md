# Engram

Standalone memory service for AI agents — three-tier graph-based episodic memory with embeddings, NER, spreading activation, and consolidation.

Extracted from [Bud](https://github.com/vthunder/bud2) and designed to be reusable by any Discord bot, Slack bot, or AI agent.

## Overview

Engram stores memories in a three-tier graph:

| Tier | Type | Description |
|------|------|-------------|
| 1 | **Episodes** | Raw ingested messages, events, or observations |
| 2 | **Entities** | Named entities extracted from episodes (people, orgs, places, etc.) |
| 3 | **Traces** | Consolidated memory summaries, built from episode clusters |

The retrieval algorithm uses **spreading activation**: a dual-trigger seed (semantic vector similarity + lexical FTS5) that propagates through the graph to surface contextually relevant memories, even ones not directly matched by the query.

---

## Quickstart

### Prerequisites

- Go 1.24+
- [Ollama](https://ollama.ai) (for embeddings and optional NER)
- One of the following for consolidation:
  - An Anthropic API key (direct API), **or**
  - [Claude Code](https://claude.ai/code) CLI installed (`claude` on PATH) — lets you use your existing Claude subscription without a separate API key, **or**
  - Ollama as a fully local LLM alternative

### Install

```bash
go install github.com/vthunder/engram/cmd/engram@latest
```

Or build from source:

```bash
git clone https://github.com/vthunder/engram
cd engram
go build -o engram ./cmd/engram
```

### Configure

**Option A: Anthropic API key**
```yaml
# engram.yaml
server:
  port: 8080
  api_key: "your-secret-key"

storage:
  path: "./engram.db"

llm:
  provider: "anthropic"
  model: "claude-sonnet-4-6"
  # api_key: "sk-ant-..."     # or set ANTHROPIC_API_KEY

embedding:
  base_url: "http://localhost:11434"
  model: "nomic-embed-text"

ner:
  provider: "spacy"           # or "ollama"
  spacy_url: "http://localhost:8001"

consolidation:
  enabled: true
  interval: "15m"
```

```bash
ANTHROPIC_API_KEY=sk-ant-... ./engram --config engram.yaml
```

**Option B: Claude Code (subscription, no separate API key)**

Requires [Claude Code](https://claude.ai/code) installed and logged in (`claude` on PATH).

```yaml
# engram.yaml
llm:
  provider: "claude-code"
  model: "claude-sonnet-4-6"  # optional; omit to use Claude's default
  # binary_path: "/usr/local/bin/claude"  # optional; defaults to "claude"
```

```bash
./engram --config engram.yaml
```

**Option C: Fully local (Ollama)**
```yaml
llm:
  provider: "ollama"
  model: "qwen2.5:7b"
  base_url: "http://localhost:11434"
```

Engram starts an HTTP server on port 8080 and runs background consolidation every 15 minutes.

---

## Configuration Reference

### `server`

| Key | Default | Description |
|-----|---------|-------------|
| `port` | `8080` | HTTP server port |
| `api_key` | _(none)_ | Bearer token required for all `/v1/*` requests |

### `storage`

| Key | Default | Description |
|-----|---------|-------------|
| `path` | `./engram.db` | SQLite database file path |

### `llm`

| Key | Default | Description |
|-----|---------|-------------|
| `provider` | `anthropic` | LLM provider: `anthropic`, `ollama`, or `claude-code` |
| `model` | `claude-sonnet-4-6` | Model name |
| `api_key` | _(from env)_ | Anthropic API key (if provider=anthropic) |
| `base_url` | _(none)_ | Ollama base URL (if provider=ollama) |
| `binary_path` | `claude` | Path to Claude Code CLI binary (if provider=claude-code) |

**`claude-code` provider:** Uses your existing Claude subscription via the `claude` CLI. No API key required — just install [Claude Code](https://claude.ai/code) and log in. The CLI must be on `PATH` (or specify `binary_path`).

### `embedding`

| Key | Default | Description |
|-----|---------|-------------|
| `base_url` | `http://localhost:11434` | Ollama-compatible embedding server URL |
| `model` | `nomic-embed-text` | Embedding model name |
| `api_key` | _(none)_ | API key if required by embedding server |

Embeddings are optional — if unavailable, Engram falls back to lexical-only retrieval.

### `ner`

| Key | Default | Description |
|-----|---------|-------------|
| `provider` | `ollama` | NER provider: `spacy` or `ollama` |
| `model` | `qwen2.5:7b` | Model name (Ollama only) |
| `spacy_url` | _(none)_ | spaCy server URL (if provider=spacy) |

NER is optional — entities won't be extracted if not configured.

### `consolidation`

| Key | Default | Description |
|-----|---------|-------------|
| `enabled` | `true` | Whether to run background consolidation |
| `interval` | `15m` | How often to consolidate (Go duration string) |

### Environment Variables

All config fields can be overridden with `ENGRAM_*` env vars:

| Variable | Config field |
|----------|-------------|
| `ENGRAM_SERVER_API_KEY` | `server.api_key` |
| `ENGRAM_STORAGE_PATH` | `storage.path` |
| `ENGRAM_LLM_PROVIDER` | `llm.provider` |
| `ENGRAM_LLM_MODEL` | `llm.model` |
| `ENGRAM_LLM_API_KEY` | `llm.api_key` |
| `ANTHROPIC_API_KEY` | `llm.api_key` (fallback) |
| `ENGRAM_LLM_BASE_URL` | `llm.base_url` |
| `ENGRAM_LLM_BINARY_PATH` | `llm.binary_path` (claude-code path) |
| `ENGRAM_EMBEDDING_BASE_URL` | `embedding.base_url` |
| `ENGRAM_EMBEDDING_MODEL` | `embedding.model` |
| `ENGRAM_EMBEDDING_API_KEY` | `embedding.api_key` |
| `ENGRAM_NER_PROVIDER` | `ner.provider` |
| `ENGRAM_NER_MODEL` | `ner.model` |
| `ENGRAM_NER_SPACY_URL` | `ner.spacy_url` |

---

## REST API

All `/v1/*` endpoints require `Authorization: Bearer <api_key>`. The `/health` endpoint is public.

A full OpenAPI 3.0 specification is available at [`openapi.yaml`](openapi.yaml).

### Health

```
GET /health
```

Response:
```json
{"status": "ok", "time": "2026-02-21T07:00:00Z"}
```

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
  "reply_to": "ep-abc123",
  "embedding": []
}
```

`author`, `author_id`, `channel`, `timestamp_event`, `reply_to`, and `embedding` are optional. If `embedding` is omitted and an embedding server is configured, it is computed automatically. Named entities are extracted in the background if NER is configured.

Response `201`:
```json
{"id": "ep-550e8400-...", "short_id": "a3f2b"}
```

#### `POST /v1/thoughts`

Shorthand for ingesting a free-form thought with `source: "thought"`.

Request:
```json
{"content": "Need to follow up with Bob about the project deadline."}
```

Response `201`:
```json
{"id": "ep-550e8400-..."}
```

### Search

#### `POST /v1/search`

Retrieve relevant memories using hybrid search (semantic + spreading activation).

Request:
```json
{"query": "Alice meeting preferences", "limit": 10}
```

`limit` defaults to 10.

Response `200`:
```json
{
  "traces": [{"id": "tr-...", "summary": "Alice prefers morning meetings...", "activation": 0.82, ...}],
  "episodes": [...],
  "entities": [{"id": "ent-...", "name": "Alice", "type": "PERSON", ...}]
}
```

### Consolidation

#### `POST /v1/consolidate`

Trigger the consolidation pipeline manually. Clusters recent episodes by semantic similarity, generates summaries via LLM, and promotes them to traces.

Response `200`:
```json
{"traces_created": 3, "duration_ms": 1240}
```

Returns `503` if consolidation is not configured.

### Traces

#### `GET /v1/traces`

List all consolidated traces.

#### `GET /v1/traces/{id}?level=1`

Get a trace by full ID or 5-char short ID.

Query param `level` controls summary compression:
- `0` — raw trace, no summary override
- `1` — L1 summary (default, concise)
- `2` — L2 summary (more compressed)

#### `GET /v1/traces/{id}/context`

Get a trace with its source episodes and linked entities.

Response:
```json
{
  "trace": {...},
  "source_episodes": [...],
  "linked_entities": [{"id": "ent-...", "name": "Alice", "type": "PERSON"}]
}
```

#### `POST /v1/traces/{id}/reinforce`

Boost a trace's activation (simulates memory reconsolidation).

Request (all optional):
```json
{"embedding": [...], "alpha": 0.3}
```

`alpha` defaults to `0.3`. Higher values = stronger reinforcement.

### Episodes

#### `GET /v1/episodes/{id}`

Get an episode by full ID or 5-char short ID.

### Entities

#### `GET /v1/entities?type=PERSON&limit=100`

List extracted named entities.

Query params:
- `type` — filter by entity type (see entity types below)
- `limit` — max results (default 100)

**Entity types** (OntoNotes + extensions):

| Type | Description |
|------|-------------|
| `PERSON` | People, including fictional |
| `ORG` | Organizations |
| `GPE` | Geopolitical entities (countries, cities, states) |
| `LOC` | Non-GPE locations |
| `FAC` | Facilities (buildings, airports) |
| `PRODUCT` | Products (vehicles, food, etc.) |
| `EVENT` | Named events |
| `WORK_OF_ART` | Titles of books, songs, etc. |
| `TECHNOLOGY` | Software, frameworks, AI models _(custom)_ |
| `EMAIL` | Email addresses _(custom)_ |
| `PET` | Pet names _(custom)_ |
| `DATE`, `TIME`, `MONEY`, `PERCENT`, `QUANTITY`, `CARDINAL`, `ORDINAL` | Numeric/temporal |
| `OTHER` | Unclassified |

### Activation

#### `POST /v1/activation/decay`

Apply exponential decay to all trace activations. Run periodically to implement forgetting.

Request (all optional):
```json
{"lambda": 0.01, "floor": 0.05}
```

Response:
```json
{"updated": 47}
```

### Management

#### `POST /v1/memory/flush`

Trigger consolidation and cleanup. Equivalent to `POST /v1/consolidate`.

#### `DELETE /v1/memory/reset`

**Destructive.** Clears all episodes, entities, traces, and edges. Cannot be undone.

---

## MCP Server

Engram can run as a native [MCP](https://modelcontextprotocol.io) server alongside the REST server, exposing memory tools to Claude agents.

```bash
ENGRAM_MCP=1 ANTHROPIC_API_KEY=sk-ant-... ./engram --config engram.yaml
```

When `ENGRAM_MCP=1`, Engram serves MCP over stdio (for Claude Desktop / claude-code) while the REST server continues to run on the configured port.

### MCP Tools

| Tool | Description |
|------|-------------|
| `search_memory` | Semantic search over traces and episodes |
| `list_traces` | List all consolidated memory traces |
| `get_trace` | Get a trace by ID with configurable compression level |
| `get_trace_context` | Get a trace plus its source episodes and linked entities |
| `query_episode` | Get a raw episode by ID |

### Claude Desktop configuration

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

---

## Architecture

```
┌──────────────┐      ┌─────────────────────────────────────┐
│ Claude agent │─MCP─▶│           Engram Service             │
│ (any bot)    │      │                                      │
└──────────────┘      │  ┌─────────┐  ┌──────────────────┐  │
                      │  │  REST   │  │     Graph DB     │  │
┌──────────────┐      │  │  API    │  │  (SQLite +       │  │
│  Bot (REST)  │─────▶│  │         │  │   sqlite-vec +   │  │
└──────────────┘      │  │  MCP    │  │   FTS5)          │  │
                      │  │  Server │  └──────────────────┘  │
                      │  └────┬────┘                        │
                      │       │    ┌────────────────────┐   │
                      │  ┌────▼──┐ │   Consolidation    │   │
                      │  │ NER   │ │   (Claude/Ollama)  │   │
                      │  └───────┘ └────────────────────┘   │
                      │  ┌──────────────────────────────┐   │
                      │  │ Embeddings (Ollama-compat)   │   │
                      │  └──────────────────────────────┘   │
                      └─────────────────────────────────────┘
```

### Storage

Engram uses a single SQLite file with two extensions:
- **sqlite-vec** for vector similarity search
- **FTS5** for full-text search

No external database required.

### Consolidation

Background consolidation runs periodically (default: every 15 minutes):

1. Fetches recent unconsolidated episodes
2. Clusters them by semantic similarity (cosine distance threshold)
3. Generates a summary for each cluster via LLM
4. Creates or updates a **Trace** for each cluster
5. Links traces to their source episodes and involved entities

Traces have two types:
- `knowledge` — facts, decisions, preferences (long-lived, decays slowly)
- `operational` — meeting reminders, state syncs, deploys (short-lived, decays faster)

### Spreading Activation

Retrieval (`POST /v1/search`):

1. Embeds the query and runs FTS5 full-text search to find seed nodes
2. Seeds spreading activation across the graph (episodes → entities → traces)
3. Returns top-ranked traces, episodes, and entities by activation score

This surfaces relevant memories even when they aren't directly matched by the query text — e.g., searching for "Alice" will also surface traces that mention Alice's team members.

---

## Running as a Sidecar

For agent deployments, run Engram as a sidecar next to the main bot process:

```yaml
# docker-compose.yml example
services:
  bot:
    image: mybot
    environment:
      ENGRAM_URL: http://engram:8080
      ENGRAM_API_KEY: ${ENGRAM_API_KEY}

  engram:
    image: ghcr.io/vthunder/engram:latest
    environment:
      ENGRAM_SERVER_API_KEY: ${ENGRAM_API_KEY}
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
      ENGRAM_EMBEDDING_BASE_URL: http://ollama:11434
    volumes:
      - engram-data:/data
    command: ["--config", "/config/engram.yaml"]
```

---

## License

MIT
