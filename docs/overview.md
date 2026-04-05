---
generated_at: 2026-04-06T00:00:00Z
commit: a9c034d6
repomix: available
---

# Engram — Overview

> Generated: 2026-04-06 | Commit: a9c034d6

## Purpose

Engram is an episodic memory service for AI agents — a sidecar that ingests raw observations, consolidates them into structured memories (engrams) via LLM summarization, and retrieves them through spreading activation across a memory graph. Built in Go (v1.24) with a SQLite-backed graph, a React + TypeScript dashboard, and optional MCP server support for Claude agents.

## Data Flow

**Ingestion:** A client POSTs to `/v1/episodes`. The API handler writes the raw observation to the `episodes` table, kicks off async NER (via spaCy or Ollama) to extract named entities, and queues an embedding job via `internal/embed`. Entities land in the `entities` table; embeddings are stored in the `memory-vectors.db` sqlite-vec virtual table.

**Consolidation (background, every 15 min):** `internal/consolidate.Consolidator` reads unconsolidated episodes in a sliding window, infers semantic relationships between them using an LLM inference pass, clusters related episodes, then summarizes each cluster into an engram via a second LLM pass. Each engram links back to its source episodes and extracted entities, forming a traversable graph. After enough L1 engrams accumulate, a recursive pass (`consolidate/recursive.go`) clusters them into L2/L3 engrams. A separate `SchemaInductor` (`internal/schema`) runs every 6 hours, extracting recurring behavioral patterns as schemas. Newly created engrams are immediately matched against existing schemas via a forward-hook wired in `cmd/engram/main.go`.

**Decay (background, every hour):** `runDecay` in `cmd/engram/main.go` applies exponential decay to engram activation levels. Access slows decay; reinforcement reverses it.

**Retrieval:** `/v1/engrams/search` seeds a spreading activation process in `internal/graph` from three parallel signals — vector KNN (sqlite-vec), lexical BM25 (FTS5), and entity-matched lookup — then propagates activation through the engram graph via typed edges, applies lateral inhibition, and returns the highest-activation memories. If confidence is too low, the service returns empty rather than confabulating.

**Pyramid compression:** Each engram has five pre-computed summaries (4, 8, 16, 32, 64 words) stored in `memory-cache.db`. Callers specify a compression level to control token budget.

## Module Map

| Path | Responsibility |
|------|----------------|
| `cmd/engram/` | Entry point — wires all components, starts background goroutines (consolidation, decay), serves REST + optional MCP |
| `config/` | Config struct and YAML loading; resolves per-LLM overrides and env var precedence |
| `internal/graph/` | Core data layer — SQLite schema, CRUD for episodes/engrams/entities/schemas, spreading activation retrieval, pyramid compression queue, ID generation |
| `internal/api/` | HTTP handlers (chi router) — all REST endpoints, auth middleware, request/response types |
| `internal/consolidate/` | LLM-driven consolidation — episode clustering, engram summarization, recursive L2/L3 compression, Anthropic/claude-code/Ollama clients |
| `internal/embed/` | Embedding client — Ollama-backed text embedding; also used as the Ollama LLM adapter |
| `internal/mcp/` | MCP server — maps MCP tool calls to graph operations for Claude agent connections over stdio |
| `internal/ner/` | NER client — wraps spaCy HTTP API for named entity extraction |
| `internal/filter/` | Episode filtering — entropy-based and dialogue-act filters for low-signal episode suppression |
| `internal/schema/` | Schema induction (inductor) and forward matching (matcher) — extracts recurring patterns and annotates new engrams |
| `ner/` | Python spaCy microservice (Dockerfile + `server.py`) — standalone NER sidecar |
| `ui/` | React + TypeScript dashboard (Vite, Tailwind, shadcn/ui) — browses engrams, episodes, entities, and the memory graph |

## Key Files

- `cmd/engram/main.go` — startup wiring, background goroutine launch, component composition
- `internal/graph/db.go` — DB open/init, multi-database setup (main, vectors, cache)
- `internal/graph/types.go` — core data types: Engram, Episode, Entity, Schema
- `internal/graph/engrams.go` — engram CRUD and spreading activation retrieval
- `internal/graph/activation.go` — spreading activation algorithm implementation
- `internal/consolidate/consolidate.go` — consolidation pipeline: clustering, LLM summarization, engram write
- `internal/api/router.go` — route registration; entry point for understanding the API surface
- `config/config.go` — all config keys; start here to understand LLM provider resolution

## Conventions

- **Testing**: Go standard `testing` package; integration tests use a real SQLite in-memory DB (e.g. `consolidate_integration_test.go`, `graph_test.go`). No mocks for the database layer.
- **Naming**: Go idioms throughout; package names match directory names. MCP package aliased as `engrammcp`, schema as `engramschema` to avoid collision with stdlib.
- **Build**: Use `make build` — plain `go build` breaks FTS5 full-text search (CGO flags required for sqlite3).
- **Multi-DB**: Three SQLite files — `memory.db` (main data, committed), `memory-vectors.db` (embeddings, gitignored, recomputable), `memory-cache.db` (pyramids, gitignored, recomputable).
- **LLM providers**: Anthropic, `claude-code` (CLI delegate), and Ollama are all supported; resolved per-function via config (`compression_llm`, `consolidation_llm`, `inference_llm`).
- **Entry points**: REST via chi router (`internal/api/router.go`); MCP via stdio (`internal/mcp/server.go`); both served from `cmd/engram/main.go`.

## Start Here

For a given task type, start at:
- **Adding a new API endpoint**: `internal/api/router.go` — register route, then `internal/api/handlers.go` for handler pattern
- **Changing retrieval behavior**: `internal/graph/activation.go` — spreading activation; `internal/graph/engrams.go` for the search entry point
- **Modifying consolidation logic**: `internal/consolidate/consolidate.go` — main pipeline; `internal/consolidate/claude_inference.go` for LLM prompts
- **Understanding the data model**: `internal/graph/types.go` — core structs; `internal/graph/schemas.go` — SQLite schema DDL
- **Adding an MCP tool**: `internal/mcp/server.go` — tool registration and dispatch
- **Running locally**: `README.md` Quickstart, then `config/config.go` for all config keys; use `make build`
- **Debugging schema induction**: `internal/schema/inductor.go` — extraction logic; `internal/schema/matcher.go` — forward-hook matching
