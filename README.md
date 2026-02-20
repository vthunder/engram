# Engram

Standalone memory service for AI agents — three-tier graph-based episodic memory with embeddings, NER, spreading activation, and consolidation.

Extracted from [Bud](https://github.com/vthunder/bud2) and designed to be reusable by any Discord bot, Slack bot, or AI agent.

## What it is

- **Episodes** → raw ingested messages/events (Tier 1)
- **Entities** → extracted named entities, linked to episodes (Tier 2)
- **Traces** → consolidated memory summaries, with spreading activation (Tier 3)

The core retrieval algorithm is spreading activation: a dual-trigger (semantic embedding + lexical FTS5) seed that propagates through the graph to surface relevant memories.

## Quick start

```bash
# Install
go install github.com/vthunder/engram/cmd/engram@latest

# Configure
cat > engram.yaml <<EOF
server:
  port: 8080
  api_key: "your-secret-key"

storage:
  path: "./engram.db"

llm:
  provider: "anthropic"
  model: "claude-sonnet-4-6"

embedding:
  base_url: "http://localhost:11434"
  model: "nomic-embed-text"

ner:
  provider: "ollama"
  model: "qwen2.5:7b"

consolidation:
  enabled: true
  interval: "15m"
EOF

# Run
ANTHROPIC_API_KEY=sk-ant-... engram --config engram.yaml
```

## API

All endpoints require `X-API-Key` header (except `/health`).

```
GET  /health
POST /v1/episodes          # ingest an episode
POST /v1/thoughts          # ingest a thought
POST /v1/search            # semantic + lexical search
GET  /v1/traces            # list all traces
GET  /v1/traces/:id        # get trace (with ?level=0|1|2 compression)
GET  /v1/traces/:id/context # trace + source episodes + linked entities
POST /v1/traces/:id/reinforce
GET  /v1/episodes/:id
GET  /v1/entities          # list entities (?type=PERSON&limit=100)
POST /v1/consolidate       # run consolidation pipeline
POST /v1/activation/decay
POST /v1/memory/flush
DELETE /v1/memory/reset
```

## MCP server

To use as a native MCP server (for Claude agents):

```bash
ENGRAM_MCP=1 engram --config engram.yaml
```

Tools exposed: `search_memory`, `list_traces`, `get_trace`, `get_trace_context`, `query_episode`.

## Configuration

All config values can be overridden with env vars using the `ENGRAM_` prefix:
- `ENGRAM_SERVER_API_KEY`
- `ENGRAM_LLM_PROVIDER` (anthropic | ollama)
- `ENGRAM_LLM_MODEL`
- `ANTHROPIC_API_KEY` (also accepted directly)
- `ENGRAM_EMBEDDING_BASE_URL`
- `ENGRAM_NER_PROVIDER` (spacy | ollama)

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
