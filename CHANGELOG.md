# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

---

## [0.1.0] - 2026-02-21

Initial public release. Engram is a standalone episodic memory service for AI agents, extracted from [Bud](https://github.com/vthunder/bud2).

### Core Architecture

- Three-tier graph-based memory model: Episodes (raw events) → Entities (extracted named entities) → Engrams (consolidated summaries)
- SQLite storage with `sqlite-vec` for vector similarity and FTS5 for full-text search — no external database required
- Spreading activation retrieval: dual-trigger seed (semantic + lexical) that propagates through the graph to surface contextually relevant memories

### REST API

- `POST /v1/episodes` — ingest episodes with optional embedding, author, channel, reply-to metadata
- `POST /v1/thoughts` — shorthand ingest for free-form thoughts with `source: "thought"`
- `GET /v1/engrams` + `GET /v1/engrams/{id}` — list and fetch consolidated engrams
- `GET /v1/engrams/{id}/context` — get an engram with source episodes and linked entities
- `POST /v1/engrams/{id}/reinforce` — boost an engram's activation
- `GET /v1/episodes`, `GET /v1/episodes/{id}` — list and fetch raw episodes
- `GET /v1/entities`, `GET /v1/entities/{id}` — list and fetch named entities
- `POST /v1/consolidate` / `POST /v1/memory/flush` — trigger consolidation manually
- `POST /v1/activation/decay` — apply exponential activation decay (forgetting)
- `DELETE /v1/memory/reset` — destructive wipe of all memory
- `GET /health` — public health check endpoint
- `?query=` semantic search on list endpoints (engrams, episodes, entities)
- `?detail=full` verbosity flag — minimal responses by default (id + primary field only)
- `?level=N` pyramid summary compression on engrams and entities (4 / 8 / 16 / 32 / 64 words)

### MCP Server

- Runs as a native MCP server alongside the REST API (`ENGRAM_MCP=1`)
- Exposes `search_memory`, `list_engrams`, `get_engram`, `get_engram_context`, `query_episode` tools
- Compatible with Claude Desktop and claude-code

### Consolidation

- Background LLM-powered consolidation clusters recent episodes and generates engrams
- Pyramid summaries: each engram and entity gets multi-level compressed summaries (4→8→16→32→64 words)
- Two engram types: `knowledge` (long-lived) and `operational` (short-lived, decays faster)
- Identity-aware consolidation: labels episodes by role (bot / owner / third-party) for correct first-person attribution and voice
- Entity context injection in consolidation: entity metadata (known facts, recent activity) included in LLM prompt for richer summaries

### NER & Embeddings

- spaCy NER sidecar (via Docker) for fast entity extraction
- Ollama NER fallback (model-based)
- Ollama-compatible embedding server integration (default: `nomic-embed-text`)
- Embeddings optional: falls back to lexical-only retrieval when unavailable
- NER is optional: entities won't be extracted if not configured

### LLM Providers

- `anthropic` — direct Anthropic API (requires `ANTHROPIC_API_KEY`)
- `claude-code` — uses existing Claude subscription via the `claude` CLI, no separate API key required
- `ollama` — fully local LLM (e.g., `qwen2.5:7b`)

### Deployment

- Dockerfile and docker-compose configuration with spaCy NER sidecar
- All config fields overridable via `ENGRAM_*` environment variables
- OpenAPI 3.0 specification (`openapi.yaml`)

### Internal

- BLAKE3-derived content-addressable IDs for engrams and entities
- Short 5-char ID prefix supported on all `/{id}` endpoints
- Test suite: REST API handlers, embed client, CRUD operations, consolidation integration tests

[0.1.0]: https://github.com/vthunder/engram/releases/tag/v0.1.0
