# Engram

**Episodic memory service for AI agents — automatic consolidation, neuroscience-inspired retrieval.**

## The Problem

AI agents are stateless. When a bot conversation ends, every observation, preference, and decision it accumulated is gone. Naive solutions make this worse: storing raw messages and doing keyword search gives you a log, not a memory. Flat embeddings + cosine similarity retrieves what *matches* your query, not what's *relevant* to it.

Real memory isn't lookup. It's association: a question about Alice surfaces what you know about Alice's team, her preferences, a decision made last month that affects the answer. Getting there requires structure the agent didn't have to build manually.

## The Inspiration

Engram is grounded in the [Synapse](https://arxiv.org/abs/2601.02744) spreading activation model and the neuroscience of episodic memory. The key ideas:

- **Episodes consolidate into engrams.** Raw observations are transient. Repeated or semantically related episodes consolidate — via LLM summarization — into durable structured memories called engrams.
- **New memories are labile.** For 24 hours after formation, a memory can be updated by new related episodes. After that window closes, it freezes. This mimics the molecular biology of memory consolidation.
- **Memory fades automatically.** Engrams decay exponentially over time — handled by a background process, no client scheduling needed. Operational details (meeting reminders, deploy notes) decay faster than knowledge (facts, decisions, preferences). Access slows decay; reinforcement reverses it.
- **Retrieval is activation, not lookup.** A query seeds a spreading activation process — not a vector search — that propagates through the memory graph, surfacing relevant engrams even when they don't directly match the query.

## The Approach

Engram runs as a sidecar service. Any agent — Discord bot, Slack bot, Claude agent via MCP — posts raw observations to Engram, then queries it at retrieval time. The service handles everything else.

**Three memory tiers:**

| Tier | Type | Description |
|------|------|-------------|
| 1 | **Episodes** | Raw ingested messages, events, observations — lossless |
| 2 | **Entities** | Named entities (people, orgs, technologies) extracted by NER |
| 3 | **Engrams** | LLM-consolidated memory summaries, the primary retrieval target |

**Retrieval uses spreading activation.** Three signals seed the activation in parallel — semantic vector similarity, lexical BM25 full-text search, and NER-matched entity lookup — then activation spreads across the engram graph through typed edges. Lateral inhibition sharpens results. A "feeling of knowing" gate returns empty rather than confabulating when memory confidence is too low.

**Consolidation is automatic.** A background pipeline runs every 15 minutes: Claude (or Ollama) infers semantic relationships between recent episodes using a sliding window, clusters them, and summarizes each cluster into an engram. Engrams link back to their source episodes and extracted entities, building a traversable memory graph without any manual curation.

**Multi-level compression.** Every engram has five pre-computed pyramid summaries (4, 8, 16, 32, 64 words). Callers request the compression level that fits their token budget.

## Quickstart

### Prerequisites

- Go 1.24+
- [Ollama](https://ollama.ai) with `nomic-embed-text` pulled (for embeddings)
- One of: Anthropic API key · [Claude Code](https://claude.ai/code) CLI installed · Ollama (for consolidation)

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

### Configure and run

```yaml
# engram.yaml
server:
  port: 8080
  # api_key: "your-secret-key"   # omit to disable auth (fine for local use)

storage:
  path: "./engram.db"

# Pyramid compression — fast local model is sufficient for word-count compression
compression_llm:
  provider: "ollama"
  model: "qwen2.5:7b"
  base_url: "http://localhost:11434"

# Engram summarization — Haiku produces coherent prose reliably
consolidation_llm:
  provider: "anthropic"
  model: "claude-haiku-4-5-20251001"

# Relationship/edge detection — structured JSON output
inference_llm:
  provider: "anthropic"
  model: "claude-haiku-4-5-20251001"

embedding:
  base_url: "http://localhost:11434"
  model: "nomic-embed-text"

ner:
  provider: "spacy"
  spacy_url: "http://localhost:8001"

consolidation:
  enabled: true
  interval: "15m"

decay:
  interval: "1h"    # run decay every hour (set to 0 to disable)
  lambda: 0.005     # exponential decay rate
  floor: 0.01       # minimum activation level
```

```bash
ANTHROPIC_API_KEY=sk-ant-... ./engram --config engram.yaml
```

**No Anthropic API key?** Set `consolidation_llm.provider: "claude-code"` and `inference_llm.provider: "claude-code"` to use an existing [Claude Code](https://claude.ai/code) subscription, or use `"ollama"` for a fully local setup. See [Configuration](docs/configuration.md) for all options.

**No spaCy sidecar?** Set `ner.provider: "ollama"` for model-based NER, or omit the `ner` block to skip entity extraction entirely (retrieval still works via semantic + lexical seeding).

### Use it

```bash
# Ingest an observation
curl -X POST http://localhost:8080/v1/episodes \
  -H "Content-Type: application/json" \
  -d '{"content": "Alice mentioned she prefers morning standups.", "source": "slack", "author": "alice"}'

# Query memory (spreading activation retrieval)
curl -X POST http://localhost:8080/v1/engrams/search \
  -H "Content-Type: application/json" \
  -d '{"query": "Alice meeting preferences", "limit": 10}'

# Trigger consolidation manually
curl -X POST http://localhost:8080/v1/consolidate
```

## MCP

Engram can serve as an MCP server alongside the REST API, giving Claude agents direct memory access.

```bash
ENGRAM_MCP=1 ./engram --config engram.yaml
```

Add to `claude_desktop_config.json` or `.mcp.json`:

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

MCP tools: `search_memory`, `list_engrams`, `get_engram`, `get_engram_context`, `query_episode`.

## Architecture

```
┌──────────────┐      ┌──────────────────────────────────────────┐
│ Claude agent │─MCP─▶│               Engram Service              │
│              │      │                                           │
└──────────────┘      │  ┌───────────┐   ┌─────────────────────┐ │
                      │  │  REST +   │   │    SQLite graph DB   │ │
┌──────────────┐      │  │  MCP API  │◀─▶│  sqlite-vec (KNN)   │ │
│  Bot / agent │─────▶│  │           │   │  FTS5 (BM25)        │ │
└──────────────┘      │  └─────┬─────┘   └─────────────────────┘ │
                      │        │                                   │
                      │  ┌─────▼──────────────────────────────┐  │
                      │  │         Background pipeline         │  │
                      │  │  NER (spaCy/Ollama) · Embeddings   │  │
                      │  │  Consolidation (Claude/Ollama)      │  │
                      │  └────────────────────────────────────┘  │
                      └──────────────────────────────────────────┘
```

Engram stores everything in a single SQLite file — no external database. The `sqlite-vec` extension handles vector KNN; FTS5 handles lexical search. Both are bundled extensions, not separate services.

## Running as a sidecar

```yaml
# docker-compose.yml
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

## Conversation context window

Beyond spreading activation retrieval, Engram lets bots maintain a tiered conversation buffer — recent messages raw, older ones compressed, anything beyond the buffer retrievable as engrams. The `channel` field on episodes groups messages by conversation; `?before={id}` provides cursor-based pagination; `?level=N` applies pyramid compression in-band:

```bash
GET /v1/episodes?channel=guild:general&limit=10                                        # raw recent
GET /v1/episodes?channel=guild:general&limit=20&before={ep10_id}&level=8              # compressed
GET /v1/episodes?channel=guild:general&limit=70&before={ep30_id}&unconsolidated=true&level=8  # buffer
```

Setting `consolidation.max_buffer` equal to the bot's fetch limit ensures older episodes are always in one place or the other — never in limbo. See [API reference](docs/api.md#conversation-context-window) for the full pattern.

## Use cases

- **Conversational agents** — persistent memory across sessions: preferences, decisions, relationship context
- **Discord / Slack bots** — remember what users said and decided, surface it when relevant
- **Long-running research agents** — accumulate findings over days; recall related prior work at query time
- **Personal assistants** — "what did I say I needed to follow up on?" answered from actual memory

## Docs

- [Configuration reference](docs/configuration.md) — all config keys, environment variable overrides
- [REST API reference](docs/api.md) — all endpoints, request/response shapes
- [MCP tools reference](docs/mcp.md) — tools available to Claude agents, usage patterns
- [OpenAPI spec](openapi.yaml)

## License

Mozilla Public License 2.0. See [LICENSE](LICENSE) or https://mozilla.org/MPL/2.0/.
