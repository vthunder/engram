# AGENTS.md — engram

This is the primary ops guide for agents working in this repository. See `README.md` for architecture and project overview.

## Build

**Always use `make build`** — plain `go build` breaks FTS5 full-text search triggers.

```bash
make build        # builds with -tags fts5 and correct CGO flags
./engram          # run locally
```

## Running

Engram runs as a sidecar service on port 8080. On the owner's machine it's managed via launchd:

```
launchctl kickstart -k gui/$(id -u)/com.engram   # restart
```

**⚠️ NEVER restart Engram without warning the owner first.** `launchctl kickstart -k` kills any running process on port 8080. An unannounced restart during active use has caused data loss before (2026-02-26 incident). Call `talk_to_user` with what you're about to do and why, then proceed.

Config lives in `engram.yaml`. Key fields: `anthropic.api_key`, `storage.path`.

## Storage (post-v31)

Three database files in the configured `storage.path`:
- `memory.db` — episodes, engrams, entities, schemas (committed)
- `memory-vectors.db` — embedding BLOBs (gitignored, recomputable)
- `memory-cache.db` — summary pyramids (gitignored, recomputable)

## Key API Endpoints

```
POST /v1/episodes              # ingest raw observations
POST /v1/engrams/search        # retrieve memories
POST /v1/engrams/regenerate-pyramids          # bulk regen (use ?missing_only=true)
POST /v1/engrams/{id}/regenerate-pyramid      # single regen (use ?mode=from_source)
POST /v1/schemas/search        # schema retrieval
POST /v1/schemas/dedup         # embedding dedup (fast)
POST /v1/schemas/dedup-llm     # semantic dedup (slow, use sparingly)
```

## Development Notes

- Go 1.24+ required
- Embeddings require Ollama with `nomic-embed-text` pulled
- Consolidation requires Anthropic API key or Ollama LLM
- Run tests: `go test ./...`
