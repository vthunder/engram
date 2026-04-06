---
topic: Episode Ingestion & NER Pipeline
repo: engram
generated_at: 2026-04-06T00:00:00Z
commit: e6bd109
key_modules: [internal/api, internal/ner, internal/graph]
score: 0.91
---

# Episode Ingestion & NER Pipeline

> Repo: `engram` | Generated: 2026-04-06 | Commit: e6bd109

## Summary

The episode ingestion pipeline is the front door for all raw observations entering the memory system. It receives a text event via HTTP, computes or stores an embedding, persists the episode with a deterministic BLAKE3 ID, then fans out two background tasks: NER-driven entity extraction and pyramid compression. Named entity recognition uses a two-stage design — a fast spaCy sidecar pre-filters for high-value entity types before triggering expensive Ollama extraction.

## Key Data Structures

### `Episode` (`internal/graph/types.go`)

The fundamental unit of raw memory. Persisted verbatim in the `episodes` table before any consolidation occurs.

Key fields:
- `ID` — 32-char BLAKE3 hex, derived from `content + source + createdAtNs`. Deterministic.
- `Content` — raw text of the observation
- `Source` — provenance tag (`discord`, `calendar`, etc.)
- `Author` / `AuthorID` — display name and stable ID for the author
- `Channel` — logical grouping; used for per-channel consolidation triggers
- `TimestampEvent` — when the event happened (T), distinct from `TimestampIngested` (T')
- `ReplyTo` — optional reference to parent episode ID; creates a structural `REPLIES_TO` edge
- `Embedding` — float64 vector (nil until computed); stored separately in `memory-vectors.db` (post-v31)
- `AuthorizationChecked` / `HasAuthorization` — auth gate flags for downstream access control
- `Attachments` — JSON array of `{filename, content_type, url}` for CDN-hosted files

### `ingestEpisodeRequest` (`internal/api/handlers.go`)

Wire format for `POST /v1/episodes`. Accepts an optional pre-computed `Embedding` — when the caller already has the vector (e.g., a Discord bot that computed it client-side), server-side embedding is skipped. The `TimestampEvent` field defaults to ingestion time if omitted.

### `ner.Entity` (`internal/ner/client.go`)

A raw span returned by the spaCy sidecar: `Text`, `Label` (OntoNotes type), `Start`/`End` (char offsets). This is the coarse, fast result used only to decide whether to run the full Ollama extraction pass.

### `ner.ExtractResponse` (`internal/ner/client.go`)

Sidecar response wrapper. `HasEntities` is the gate flag — true when at least one high-value entity type (PERSON, ORG, GPE, LOC, etc.) was found. MONEY, DATE, TIME, and CARDINAL are deliberately excluded as high-value types to avoid noise-triggering.

### `EpisodeCompressQueue` (`internal/graph/compress_queue.go`)

Serialises pyramid compression for episodes. Backed by a fixed-size channel (`episodeCompressQueueSize = 256`). When the channel is full, `needsScan` is set atomically; a periodic 5-minute scan (`episodeCompressScanInterval`) then picks up any dropped IDs from the database. At most one episode compresses at a time to bound LLM concurrency.

## Lifecycle

1. **Request parse** (`handleIngestEpisode`, `internal/api/handlers.go`): Decodes `ingestEpisodeRequest` JSON; rejects missing `content` or `source` with 400.

2. **Embedding** (`handleIngestEpisode`): If `req.Embedding` is empty and an `EmbedClient` is configured, calls `s.EmbedClient` (Ollama) synchronously to compute the vector before writing to DB. If neither is provided, the episode is stored without an embedding and KNN-based retrieval won't seed from it.

3. **ID generation** (`graph.GenerateEpisodeID`, `internal/graph/id.go`): BLAKE3 hash over `content + source + createdAtNs` truncated to 16 bytes → 32 hex chars. The timestamp component (nanoseconds) is baked into the ID, so two identical messages at different times get different IDs.

4. **Persistence** (`graph.AddEpisode`, `internal/graph/episodes.go`):
   - Inserts into `episodes` table
   - Pre-computes `token_count` if not set (rough estimate: `len(content)/4`)
   - If `ReplyTo` is set, creates an `AddEpisodeEdge(id, replyTo, EdgeRepliesTo, 1.0)` structural edge with `inferred_by_llm=0`
   - Returns the episode ID

5. **HTTP response**: Handler writes `{"id": episodeID}` with 200. All remaining work runs asynchronously.

6. **NER (background goroutine)**: If `s.NERClient != nil`:
   - Calls `s.NERClient.Extract(content)` → HTTP POST to spaCy sidecar at `baseURL/extract`
   - Sidecar responds with entity spans and `HasEntities`
   - If `HasEntities == true`: full entity extraction and linking runs (likely Ollama-backed, delegated to the consolidation layer); new entities are stored via `graph.AddEntity`, existing ones resolved via `graph.FindEntityByName`; each entity is linked with `graph.LinkEpisodeToEntity(episodeID, entityID)` and salience is incremented

7. **Compression enqueue** (`graph.EpisodeCompressQueue.Enqueue`): Episode ID is dropped into the queue channel. The background worker (`compress_queue.go:Start`) dequeues it and calls `graph.GenerateEpisodeSummaries(episode, compressor)`, which generates L4/L8/L16/L32/L64 summaries via Ollama. All five levels are always generated; if the episode is already shorter than the target word count, the verbatim text is stored at that level.

## Design Decisions

- **Two-stage NER (spaCy gate + Ollama)**: spaCy (`en_core_web_sm`) is cheap and fast (~sub-100ms sidecar round-trip) but imprecise. It's used only to decide whether to run the expensive Ollama extraction pass. Non-Western names and tech terms are known to be mis-classified by spaCy, but this is acceptable — false positives just trigger an unnecessary Ollama call, while false negatives at the spaCy stage permanently skip extraction for that episode.

- **Caller-supplied embeddings**: The `Embedding` field on `ingestEpisodeRequest` allows callers (e.g., the Discord bot) to supply pre-computed vectors, avoiding a double round-trip through Ollama. This is a deliberate perf escape hatch, not an accidental affordance.

- **Structural vs. inferred edges** (`inferred_by_llm` flag, `internal/graph/episode_trace_edges.go`): Reply edges created at ingestion have `inferred_by_llm=0`. Consolidation uses this to distinguish structural topology (reply chains) from LLM-inferred semantic edges (`inferred_by_llm=1`). Consolidation skips its inference pass only when semantic edges exist — reply edges alone don't block inference.

- **Async NER and compression**: Both run in goroutines after the HTTP response is sent. This keeps `POST /v1/episodes` latency bounded to DB write + optional embedding, but means entity links and summaries may lag seconds behind the episode record. Callers querying immediately after ingest may see an episode with no entities and no compression summaries.

- **CompressQueue overflow via needsScan**: Rather than blocking the caller on a full queue, the queue silently drops IDs and sets an atomic flag. A 5-minute periodic scan then queries `GetEpisodesWithoutSummaries` and re-enqueues. This means burst ingest can leave episodes uncompressed for up to 5 minutes.

- **CJK detection in compression** (`compression.go:compressToTarget`): If the LLM produces CJK characters but the input had none, the output is re-summarized with a fallback model (Mistral, described as "English-focused"). The reverse (CJK input → no CJK output) is also detected.

## Integration Points

| From | To | What crosses the boundary |
|------|----|--------------------------|
| `internal/api` | `internal/graph` | `AddEpisode()`, `LinkEpisodeToEntity()`, `AddEntityAlias()`, `FindEntityByName()` |
| `internal/api` | `internal/embed` | `EmbedClient.Embed(content)` — Ollama embedding call |
| `internal/api` | `internal/ner` | `NERClient.Extract(content)` — spaCy sidecar HTTP call; gate for entity extraction |
| `internal/api` | `internal/graph.EpisodeCompressQueue` | `Enqueue(episodeID)` — async pyramid compression trigger |
| `internal/graph.AddEpisode` | `internal/graph` | `AddEpisodeEdge(id, replyTo, REPLIES_TO, 1.0)` — self-loop for reply chain topology |
| `internal/ner` | spaCy sidecar (`ner/`) | HTTP POST `/extract` — Python process, separate Docker container |

## Non-Obvious Behaviors

- **No embedding = no KNN seeding at retrieval**: Episodes stored without an embedding (NER client absent, embed client absent) will never be seeded via `FindSimilarEngrams`. They can still surface via FTS5 keyword matching or entity-bridging — but only after consolidation creates an engram with its own embedding.

- **Compression levels are target word counts, not ratios**: `CompressionLevel8 = 8` means "8 words max." If the episode is already 6 words, the verbatim text is stored at L8 (and likely at L16, L32, L64 too). Callers specifying `?level=8` on a short episode will get the original text.

- **The same episode can be ingested twice with different IDs**: If `TimestampEvent` is not supplied in the request, the handler uses the current wall clock with nanosecond precision to derive the ID. Two rapid calls with identical content will produce different IDs because `createdAtNs` differs.

- **Reply edges are not bidirectional in episode neighbors**: `GetEpisodeNeighbors` returns edges from the `episode_edges` table, which stores directed `(from_id, to_id)` pairs. However, the query checks both directions: it returns edges where the episode is either `from_id` or `to_id`. So A→B and B are bidirectional from each other's perspective in neighbor lookup, even though the underlying edge is directed.

- **Entity cache is invalidated on every entity write**: `invalidateEntityCache()` is called on any entity mutation (`AddEntity`, `AddEntityAlias`). The cache is rebuilt lazily on the next `FindEntitiesByText` call. During burst entity ingestion, the cache rebuilds repeatedly; in practice, the batch pattern of entity extraction means this rebuilds once per episode's entity set.

- **Authorization flags are not set by the ingestion handler**: `AuthorizationChecked` and `HasAuthorization` default to `false` at write time. A separate downstream process (likely the bot's permission checker) calls `graph.UpdateEpisodeAuthorization()` after verifying the episode source. Consumers of the API should not assume authorization has been checked until `authorization_checked == true`.

## Start Here

- `internal/api/handlers.go` — `handleIngestEpisode` is the ingestion entry point; all decisions about embedding, NER, and queue dispatch are made here
- `internal/graph/episodes.go` — `AddEpisode` handles the actual write path including reply edge creation
- `internal/graph/id.go` — `GenerateEpisodeID` shows exactly what goes into the BLAKE3 hash (content + source + nanoseconds)
- `internal/ner/client.go` — entire NER client is ~50 lines; clarifies the two-stage design (spaCy gate only)
- `internal/graph/compress_queue.go` — `EpisodeCompressQueue` shows the overflow-via-needsScan pattern and the 5-minute scan interval
