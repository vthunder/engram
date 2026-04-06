---
topic: Conversation Context Buffer
repo: engram
generated_at: 2026-04-06T00:00:00Z
commit: 0e6ce75a
key_modules: [internal/api, internal/graph]
score: 0.67
---

# Conversation Context Buffer

> Repo: `engram` | Generated: 2026-04-06 | Commit: 0e6ce75a

## Summary

The Conversation Context Buffer is the subsystem that lets callers retrieve a channel's raw conversation history (episodes) in reverse-chronological order, scoped to a channel, paginated via episode-ID cursors, and optionally downsampled to pre-computed pyramid compression levels. It exists so AI agents can fetch their own conversation context at a controlled token budget — full text for deep recall, compressed summaries for efficient sliding-window context.

## Key Data Structures

### `Episode` (`internal/graph/types.go:91`)
Raw conversation observation — one message, thought, or event. Key fields for context retrieval:
- `Channel string` — arbitrary string token (Discord channel ID, thread ID, etc.) that partitions episodes into independent streams
- `TimestampEvent time.Time` — the event time, used as the pagination cursor; distinct from `TimestampIngested`
- `Content string` — mutable at read time: `applyEpisodeLevels` replaces this with a compressed summary when `level > 0`
- `Level int` — compression level actually applied (0 = verbatim); set by the handler after summary substitution

### `EpisodeSummary` (`internal/graph/compression.go:13`)
Pre-computed compressed version of one episode at one word-count target.
- `CompressionLevel int` — the word target: 4, 8, 16, 32, or 64
- `Summary string` — the LLM-generated compressed text (or verbatim content if already under target)
- Stored in a separate `episode_summaries` table in the main SQLite DB; keyed by `(episode_id, compression_level)`

### `EpisodeCompressQueue` (`internal/graph/compress_queue.go:20`)
Serialising worker that generates pyramid summaries asynchronously after ingest.
- `queue chan string` — buffered channel of episode IDs (capacity 256)
- `needsScan atomic.Bool` — set when the queue overflows or on startup; triggers a DB-scan backfill at the next 5-minute tick

### `Services` (`internal/api/handlers.go:20`)
Handler dependency container. Relevant fields:
- `Graph *graph.DB` — all episode reads and writes
- `CompressQueue *graph.EpisodeCompressQueue` — enqueued on every ingest; `nil` when compression is not configured

## Lifecycle

1. **Ingest**: `POST /v1/episodes` hits `handleIngestEpisode`. The handler creates an `Episode` (with `Channel` from the request body), calls `Graph.AddEpisode(ep)`, then calls `CompressQueue.Enqueue(ep.ID)` if compression is configured. NER extraction runs in a background goroutine independently.

2. **Async compression**: `EpisodeCompressQueue.Start()` runs as a long-lived goroutine (launched from `cmd/engram/main.go`). It selects on the `queue` channel; for each episode ID it calls `DB.generateCompressedSummaries(episode, compressor)`, which runs five LLM calls in sequence (L4→L8→L16→L32→L64), storing each result via `AddEpisodeSummary`.

3. **Short-circuit for already-short content**: Inside `generateCompressedSummaries`, `compressToTarget` checks word count before calling the LLM. If the episode is already under the target word count it stores the verbatim text — no LLM call.

4. **Queue overflow / backfill**: If the 256-slot buffer is full at `Enqueue` time, the ID is dropped and `needsScan` is set. Every 5 minutes the background goroutine checks the flag; if set, `scan()` fetches up to 100 episodes without any summaries via `GetEpisodesWithoutSummaries` and compresses them. If a full batch was returned, `needsScan` is set again to continue on the next tick.

5. **Context retrieval**: `GET /v1/episodes?channel=X&before={id}&limit=N&level=L` hits `handleListEpisodes`. The handler:
   - Resolves the `before` query param (an episode ID or short prefix) to a `*time.Time` cursor via `Graph.GetEpisode` → `ResolveEpisodeID` fallback
   - Calls `Graph.GetEpisodesFiltered(channel, beforeTimestamp, unconsolidatedOnly, limit)` — a dynamically-built SQL query ordered `DESC` by `timestamp_event`
   - Calls `applyEpisodeLevels(graph, episodes, level)` to substitute compressed content

6. **Level application (batch)**: `applyEpisodeLevels` collects episode IDs, calls `GetEpisodeSummariesBatch(ids, level)` with an exact `compression_level = ?` match, and mutates each episode's `Content` and `Level` fields in-place. Episodes with no pre-generated summary at the requested level are left unchanged (original content, `Level = 0`).

## Design Decisions

- **Timestamp cursor, not offset**: `before={id}` resolves to `timestamp_event` of the referenced episode. This makes pagination stable under concurrent inserts — a new episode appearing after the request was started doesn't shift rows. The handler resolves ID→timestamp server-side, so clients pass IDs not raw timestamps.

- **Batch level-application is exact-match only**: `GetEpisodeSummariesBatch` queries `WHERE compression_level = ?` with no fallback. Single-episode `GetEpisodeSummary` does fall back to higher levels when the exact level is missing. This asymmetry was likely a performance choice — the fallback logic in single-episode mode iterates levels with repeated queries, which is prohibitive over a batch.

- **Queue overflow loses IDs, not correctness**: Dropped IDs are recovered by the periodic DB scan (`GetEpisodesWithoutSummaries`). The choice to drop rather than block preserves ingest latency at the cost of a 5-minute worst-case delay before summaries are available.

- **`needsScan = true` at construction**: `NewEpisodeCompressQueue` always sets the flag, so the first scan tick backfills any episodes that existed before the process started (or before compression was deployed).

- **`unconsolidated=true` uses LEFT JOIN**: The unconsolidated filter adds `LEFT JOIN engram_episodes ee ON ee.episode_id = e.id ... AND ee.engram_id IS NULL` rather than a subquery. This avoids a correlated subquery per row.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `internal/api` | `internal/graph.DB` | `GetEpisodesFiltered`, `GetEpisodeSummariesBatch`, `CountEpisodesFiltered` — all episode list reads |
| `internal/api` | `internal/graph.EpisodeCompressQueue` | `Enqueue(ep.ID)` called after every successful ingest |
| `internal/graph.EpisodeCompressQueue` | `internal/graph.DB` | `GetEpisodesWithoutSummaries`, `generateCompressedSummaries`, `AddEpisodeSummary` — backfill and compression writes |
| `internal/graph.Compressor` (interface) | LLM backend | `Generate(prompt) (string, error)` — called per compression level per episode; implemented by `internal/embed.Client` (Ollama) |
| `internal/graph.DB` | `internal/consolidate` | `GetChannelConsolidationStats` uses the same `channel` partitioning to decide when to trigger consolidation; same `episodes` table, separate concern |

## Non-Obvious Behaviors

- **Silent level downgrade in batch mode**: If an episode has no pre-generated summary at the requested level, `applyEpisodeLevels` silently returns its original content with `Level = 0`. Callers can detect this by checking whether `Level` on the returned episode matches their requested level.

- **`before` accepts short ID prefixes**: The handler tries `Graph.GetEpisode(beforeID)` first, then falls back to `ResolveEpisodeID(beforeID)` which likely does a prefix match. Clients can pass abbreviated IDs as cursors.

- **Compression runs outside the request path**: A caller POSTing an episode and immediately GETting the same channel with `level=8` will receive that episode's original content, not a summary. Summaries arrive asynchronously, potentially minutes later if the queue is backed up.

- **The `Level` field on `Episode` is not persisted**: It is set transiently by `applyEpisodeLevels` after reading from the DB. The DB schema stores only original content; compression lives entirely in the `episode_summaries` table.

- **Compression skips the LLM for short content**: `compressToTarget` uses verbatim text when word count is already ≤ target. For short episodes (e.g., "ok", "yes"), all five levels store the original string.

- **Engram context endpoint is separate**: `GET /v1/engrams/{id}/context` returns source episodes for a consolidated engram, not a channel buffer. It shares the `Episode` type but is a different access pattern with no pagination or level support.

## Start Here

- `internal/api/handlers.go:838` (`handleListEpisodes`) — entry point for context retrieval; shows cursor resolution and level application pipeline
- `internal/graph/episodes.go:489` (`GetEpisodesFiltered`) — the SQL query: channel filter, timestamp cursor, unconsolidated LEFT JOIN, DESC ordering
- `internal/graph/compression.go:52` (`GetEpisodeSummary`) and `:247` (`GetEpisodeSummariesBatch`) — single-episode fallback vs. batch exact-match; understand the asymmetry before using either
- `internal/graph/compress_queue.go` (`EpisodeCompressQueue`) — overflow handling and backfill scan; read this before assuming summaries are always available
- `internal/graph/compression.go:100` (`generateCompressedSummaries`) — the five-level generation loop; verbatim short-circuit is here
