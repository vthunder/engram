# Configuration Reference

Engram is configured via a YAML file (default: `engram.yaml`). Any field can be overridden with an `ENGRAM_*` environment variable.

```bash
./engram --config /path/to/engram.yaml
```

---

## `server`

| Key | Default | Description |
|-----|---------|-------------|
| `port` | `8080` | HTTP server port |
| `api_key` | _(none)_ | Bearer token required for all `/v1/*` requests |

---

## `storage`

| Key | Default | Description |
|-----|---------|-------------|
| `path` | `./engram.db` | SQLite database file path |

---

## `llm`

Controls the LLM used for the consolidation pipeline.

| Key | Default | Description |
|-----|---------|-------------|
| `provider` | `anthropic` | `anthropic`, `ollama`, or `claude-code` |
| `model` | `claude-sonnet-4-6` | Model name |
| `api_key` | _(from env)_ | Anthropic API key (if `provider: anthropic`) |
| `base_url` | _(none)_ | Base URL (if `provider: ollama`) |
| `binary_path` | `claude` | Path to Claude Code CLI binary (if `provider: claude-code`) |

### Provider options

**`anthropic`** — Direct Anthropic API. Requires `ANTHROPIC_API_KEY` or `llm.api_key`.

```yaml
llm:
  provider: "anthropic"
  model: "claude-sonnet-4-6"
```

**`claude-code`** — Uses your existing [Claude Code](https://claude.ai/code) subscription via the `claude` CLI. No separate API key needed. The `claude` binary must be on `PATH` (or specify `binary_path`).

```yaml
llm:
  provider: "claude-code"
  model: "claude-sonnet-4-6"   # optional; omit to use Claude's default
  # binary_path: "/usr/local/bin/claude"
```

**`ollama`** — Fully local via Ollama. Requires `base_url`.

```yaml
llm:
  provider: "ollama"
  model: "qwen2.5:7b"
  base_url: "http://localhost:11434"
```

---

## `embedding`

| Key | Default | Description |
|-----|---------|-------------|
| `base_url` | `http://localhost:11434` | Ollama-compatible embedding server URL |
| `model` | `nomic-embed-text` | Embedding model name |
| `api_key` | _(none)_ | API key if required by the embedding server |

If the embedding server is unavailable, Engram falls back to lexical-only retrieval (BM25 + entity seeding).

---

## `ner`

| Key | Default | Description |
|-----|---------|-------------|
| `provider` | `ollama` | `spacy` or `ollama` |
| `model` | `qwen2.5:7b` | Model name (Ollama only) |
| `spacy_url` | _(none)_ | spaCy server URL (if `provider: spacy`) |

NER is optional — omit this block to skip entity extraction. Retrieval still works via semantic and lexical seeding; entity-based seeding is simply absent.

**spaCy** is faster and more accurate for English. Run the sidecar:

```bash
docker run -p 8001:8001 ghcr.io/vthunder/engram-ner:latest
```

Or from the repo:

```bash
cd ner && uvicorn server:app --port 8001
```

---

## `consolidation`

| Key | Default | Description |
|-----|---------|-------------|
| `enabled` | `true` | Run background consolidation |
| `interval` | `15m` | How often to check consolidation eligibility (Go duration string) |
| `min_episodes` | `10` | Minimum unconsolidated episodes in any channel before consolidation is eligible |
| `idle_time` | `30m` | Time since last episode in a channel before that channel can trigger consolidation |
| `max_buffer` | `100` | Unconsolidated episode count that triggers consolidation immediately, regardless of idle time |

Consolidation now runs conditionally on each tick. For each channel with unconsolidated episodes, it checks:

```
unconsolidated_count >= min_episodes  AND  (idle_time elapsed  OR  unconsolidated_count >= max_buffer)
```

This prevents consolidation from firing mid-conversation (the `idle_time` gate) while ensuring the buffer doesn't grow unbounded (the `max_buffer` gate). Setting `max_buffer` equal to the bot's maximum unconsolidated episode fetch limit ensures older episodes are always reachable either via the unconsolidated buffer or as consolidated engrams retrievable by spreading activation.

---

## `decay`

Controls automatic background activation decay. Engram applies exponential decay to all engram activations on each tick — no client scheduling required. Operational engrams decay at 3× the base rate.

| Key | Default | Description |
|-----|---------|-------------|
| `interval` | `1h` | How often to run decay (Go duration string). Set to `0` to disable. |
| `lambda` | `0.005` | Exponential decay coefficient λ. Higher values = faster forgetting. |
| `floor` | `0.01` | Minimum activation level — engrams never decay below this value. |

The decay formula applied to each engram:

```
new_activation = current_activation × exp(−λ × hours_since_last_access)
```

`lambda` of `0.005` causes an engram that hasn't been accessed for 7 days to retain roughly 70% activation; after 30 days, ~67%; after 90 days, ~50%. Reinforce engrams with `POST /v1/engrams/{id}/reinforce` to reset their `last_accessed` timestamp and slow decay.

`POST /v1/activation/decay` remains available for manual or one-off decay runs.

---

## `identity`

When set, the consolidation pipeline uses role-aware memory formation. The bot's own episodes are written in first person; owner episodes are attributed by name; third-party episodes are attributed correctly. One-time approvals ("ok you can restart") are recorded with temporal anchoring rather than as standing permissions.

| Key | Default | Description |
|-----|---------|-------------|
| `name` | _(none)_ | Bot's display name, e.g. `"Bud"`. Matched against `episode.author` |
| `author_id` | _(none)_ | Bot's author ID. Matched against `episode.author_id` |
| `owner_ids` | _(none)_ | List of owner `author_id` values for owner-specific framing |

```yaml
identity:
  name: "Bud"
  author_id: "bud"
  owner_ids:
    - "thunder"
```

Effects:
- Episodes where `author_id` matches `identity.author_id` are written in first person ("I should...")
- Episodes where `author_id` is in `identity.owner_ids` are attributed as "the owner" or by name
- `POST /v1/thoughts` automatically sets `author` and `author_id` from identity config

---

## Environment variable overrides

All config fields can be set or overridden with `ENGRAM_*` environment variables:

| Variable | Config field |
|----------|-------------|
| `ENGRAM_SERVER_API_KEY` | `server.api_key` |
| `ENGRAM_STORAGE_PATH` | `storage.path` |
| `ENGRAM_LLM_PROVIDER` | `llm.provider` |
| `ENGRAM_LLM_MODEL` | `llm.model` |
| `ENGRAM_LLM_API_KEY` | `llm.api_key` |
| `ANTHROPIC_API_KEY` | `llm.api_key` (fallback) |
| `ENGRAM_LLM_BASE_URL` | `llm.base_url` |
| `ENGRAM_LLM_BINARY_PATH` | `llm.binary_path` |
| `ENGRAM_EMBEDDING_BASE_URL` | `embedding.base_url` |
| `ENGRAM_EMBEDDING_MODEL` | `embedding.model` |
| `ENGRAM_EMBEDDING_API_KEY` | `embedding.api_key` |
| `ENGRAM_NER_PROVIDER` | `ner.provider` |
| `ENGRAM_NER_MODEL` | `ner.model` |
| `ENGRAM_NER_SPACY_URL` | `ner.spacy_url` |
| `ENGRAM_IDENTITY_NAME` | `identity.name` |
| `ENGRAM_IDENTITY_AUTHOR_ID` | `identity.author_id` |
| `ENGRAM_CONSOLIDATION_MIN_EPISODES` | `consolidation.min_episodes` |
| `ENGRAM_CONSOLIDATION_IDLE_TIME` | `consolidation.idle_time` |
| `ENGRAM_CONSOLIDATION_MAX_BUFFER` | `consolidation.max_buffer` |

Decay does not currently have env var overrides; configure it via the YAML file.
