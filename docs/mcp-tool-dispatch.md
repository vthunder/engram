---
topic: MCP Tool Dispatch
repo: engram
generated_at: 2026-04-06T14:20:19Z
commit: c897a2d6
key_modules: [internal/mcp, internal/graph]
score: 0.73
---

# MCP Tool Dispatch

> Repo: `engram` | Generated: 2026-04-06 | Commit: c897a2d6

## Summary

`internal/mcp` exposes Engram's memory graph as a native MCP server, making it directly addressable by Claude agents (Claude Desktop, claude-code, etc.) over a stdio transport. It maps six MCP tool calls to graph operations — semantic retrieval, engram lookup, episode retrieval, and schema listing — without duplicating the underlying graph logic, which lives entirely in `internal/graph`.

## Key Data Structures

### `Services` (`internal/mcp/server.go:19`)
Dependency container injected into every tool handler. Holds `Graph *graph.DB`, `EmbedClient *embed.Client`, `NERClient *ner.Client`, and `Logger *slog.Logger`. Handlers that need a capability (e.g., embedding) check the relevant field for nil before calling it, so the server degrades gracefully when optional services are absent.

### `engramContextResult` (`internal/mcp/server.go:199`)
Result shape for `get_engram_context`. Contains `Engram *graph.Engram`, `Sources []graph.Episode`, and `Entities []*graph.Entity` — a denormalized view across three graph tables assembled per-request.

### `server.ToolHandlerFunc` (from `github.com/mark3labs/mcp-go/server`)
The function type every handler returns: `func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)`. Handlers should return structured MCP errors via `mcpgo.NewToolResultError(...)` rather than Go errors; Go errors are reserved for catastrophic protocol failures.

### `mcpgo.Tool` (from `github.com/mark3labs/mcp-go/mcp`)
Tool schema descriptor — name, description, and typed parameter declarations. Built once at startup via `mcpgo.NewTool` + option functions (`WithString`, `WithNumber`), then registered with `s.AddTool(schema, handler)`.

## Lifecycle

1. **Startup wiring** (`cmd/engram/main.go:138-143`): `main.go` constructs `engrammcp.Services{Graph, EmbedClient, NERClient, Logger}` using the same component instances shared with the REST API.

2. **Mode detection** (`cmd/engram/main.go:181`): If `ENGRAM_MCP=1` is set, `main.go` calls `mcpserver.ServeStdio(mcpSrv)` and returns immediately — the REST server is never started. Without the env var, both servers run concurrently (REST on a port, MCP on stdio).

3. **Server construction** (`internal/mcp/server.go:27-40`): `NewServer(svc)` creates an `mcp-go` server named `"engram"` (version `"0.1.0"`) with tool capabilities enabled, then registers the six tools via `s.AddTool(schemaFn(), handlerFn(svc))`.

4. **Tool registration** (startup, one-time): Each tool has two functions — a schema builder (e.g., `searchMemoryTool()`) that returns an `mcpgo.Tool` and a handler factory (e.g., `searchMemoryHandler(svc)`) that closes over `svc` and returns a `ToolHandlerFunc`. Registration happens once at startup; the server routes incoming MCP calls by tool name.

5. **Incoming call dispatch** (per-call, handled by mcp-go library): The mcp-go library deserializes the stdio JSON-RPC frame, matches the tool name, and invokes the registered `ToolHandlerFunc`. The library handles framing, versioning, and capability negotiation; the handler only sees a `CallToolRequest`.

6. **Handler execution** — `search_memory` path (most complex): Handler reads `query` and `limit` from the request. It launches two concurrent goroutines: one calls `svc.EmbedClient.Embed(query)` to generate a vector embedding, the other calls `nerEntityEngrams(svc, query)` which runs NER and resolves any named entities to their associated engram IDs. Both goroutines return on buffered channels; the handler blocks until both complete, then calls `svc.Graph.Retrieve(queryEmb, query, limit, extraSeeds...)`.

7. **Handler execution** — lookup tools: `get_engram` and `query_episode` attempt an exact ID lookup first; on failure they call `svc.Graph.ResolveEngramID`/`ResolveEpisodeID` to resolve prefix-matched IDs (first 8 chars of the BLAKE3 hash), then retry. This handles the common case where an agent only has a short ID from a prior search result.

8. **Response serialization**: All handlers marshal their result to JSON via `json.MarshalIndent` and return it as a text content block via `mcpgo.NewToolResultText(string(data))`. Errors are returned via `mcpgo.NewToolResultError(msg)` — these are MCP-level errors visible to the caller as tool failures, not Go panics.

## Design Decisions

- **Separate tool schema and handler functions**: Each tool is split into `fooTool()` (returns the schema) and `fooHandler(svc)` (returns the handler). This keeps the registration site (`NewServer`) readable and makes it easy to inspect the tool surface in isolation without reading handler logic.

- **Graceful degradation on nil services**: Handlers check `svc.EmbedClient != nil` and `svc.NERClient != nil` before calling those services. When Ollama or spaCy is unavailable, `search_memory` falls back to lexical-only retrieval (no embedding vector is passed; `graph.Retrieve` still works via BM25/entity seeds). This allows the MCP server to run in minimal configurations.

- **`ENGRAM_MCP=1` for exclusive stdio mode**: Rather than detecting whether stdin is a tty, `main.go` uses an explicit env var to toggle MCP-only mode. This prevents REST startup on machines where the binary is launched as an MCP subprocess (Claude Desktop, claude-code MCP config), while keeping the default behavior (both servers) for local development.

- **MCP errors vs. Go errors**: Handlers return `(result, nil)` where `result.IsError = true` for expected user-facing failures (not found, missing arg). They return `(nil, err)` only for protocol-level failures. This matches the MCP spec's distinction between tool execution errors and server errors.

- **`get_engram` compression level param**: The level parameter defaults to 1 (L1 summary, ~8-16 words), which fits token-constrained contexts. Callers can request level 0 for the full summary. This reuses the pyramid infrastructure already stored in `memory-cache.db`.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `internal/mcp` | `internal/graph` | `Services.Graph` — all tool handlers call `graph.DB` methods for data operations (Retrieve, GetEngram, GetAllEngrams, GetEpisode, etc.) |
| `internal/mcp` | `internal/embed` | `Services.EmbedClient` — `search_memory` calls `EmbedClient.Embed(query)` to generate a query vector for spreading activation seeding |
| `internal/mcp` | `internal/ner` | `Services.NERClient` — `search_memory` calls `NERClient.Extract(query)` to identify named entities for extra activation seeds |
| `cmd/engram` | `internal/mcp` | `engrammcp.NewServer(svc)` + `mcpserver.ServeStdio(mcpSrv)` — startup wiring and transport launch |
| External agent | `internal/mcp` | stdio JSON-RPC (MCP protocol) — agents send tool call requests and receive text content responses |

## Non-Obvious Behaviors

- **NER and embedding run concurrently on every `search_memory` call**: Even if only one succeeds, retrieval proceeds with whatever signals are available. If the embedding goroutine fails, `queryEmb` is nil and retrieval falls back to BM25+entity seeds. The handler does not retry or surface partial failures to the caller.

- **Prefix ID resolution on every `get_engram` / `query_episode` miss**: The handler always tries the exact ID first, then resolves the prefix — meaning two graph lookups happen for every call where only a short ID is provided. This is by design: the short-ID convention (first 8 chars of BLAKE3) is the normal way agents refer to objects after a `search_memory` result.

- **MCP mode disables REST entirely**: When `ENGRAM_MCP=1`, the REST server is never started — not even on a different port. Consolidation, decay, and compression goroutines still run in the background, but no HTTP endpoint is available.

- **`list_engrams` returns all engrams without pagination**: Unlike `search_memory` (which respects a `limit`), `list_engrams` calls `GetAllEngrams()` with no bound. For large memory stores this can produce very large JSON responses; the tool is intended for inspection/debugging, not production retrieval.

- **`list_schemas` strips embeddings from output**: The inline `schemaOut` struct omits the `Embedding []float64` field present on `graph.Schema`. Embeddings are kilobyte-sized float arrays that are useless to an agent and would bloat the response; this is handled locally in the handler rather than in a graph method.

- **`get_engram_context` assembles entities one-by-one**: It calls `GetEntity(eid)` in a loop over entity IDs rather than in a batch query. For engrams with many linked entities this is an N+1 pattern. The tool is infrequent enough that this has not been batched yet.

## Start Here

- `internal/mcp/server.go` — the entire MCP surface; all 6 tools are in this single file
- `cmd/engram/main.go` (lines 138–187) — shows how `Services` is wired and how the MCP/REST fork is decided
- `internal/graph/engrams.go` — `Retrieve` function: entry point for what `search_memory` triggers downstream
- `internal/graph/activation.go` — `SpreadActivationFromEmbedding` and `SpreadActivation`: the spreading activation algorithm that `Retrieve` calls
- `internal/graph/db.go` — `DB.Open`: understand the three-file SQLite layout that all tool handlers operate against
