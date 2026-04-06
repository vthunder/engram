# Doc Plan: engram — 2026-04-06

Scoring: centrality (0.30) + coverage gap (0.30) + complexity (0.20) + churn (0.10) + bug density (0.10)
Topics span modules — signals are the max across constituent modules.

| Rank | Topic | Score | Key Modules | Signals | Status |
|------|-------|-------|-------------|---------|--------|
| 1 | Spreading Activation Retrieval | 0.98 | `internal/graph` | centrality 24 (max), no doc, 30 commits/90d, 17 fix-commits | generated |
| 2 | Memory Consolidation Pipeline | 0.96 | `internal/consolidate`, `internal/graph` | centrality 24, no doc, 15+30 commits, 8+17 fix-commits | generated |
| 3 | Episode Ingestion & NER Pipeline | 0.91 | `internal/api`, `internal/ner`, `internal/graph` | centrality 24, no doc, 37 commits/90d (API highest churn), 17 fix-commits. Source: `id-design.md` | generated |
| 4 | Engram Decay & Activation Mechanics | 0.88 | `internal/graph`, `cmd/engram` | centrality 24, no doc, 30 commits, 17 fix-commits; (foundational) | generated |
| 5 | Schema Induction & Forward Matching | 0.82 | `internal/schema`, `internal/consolidate`, `internal/graph` | centrality 24, no doc, 7 schema commits + 30 graph, 4+17 fix-commits | generated |
| 6 | Pyramid Compression | 0.78 | `internal/graph`, `internal/consolidate` | centrality 24, no doc, compress_queue high churn, 17 fix-commits | generated |
| 7 | Multi-Database Architecture | 0.76 | `internal/graph`, `config`, `cmd/engram` | centrality 24, no doc, 3-DB split is non-obvious runtime constraint; (foundational) | generated |
| 8 | MCP Tool Dispatch | 0.73 | `internal/mcp`, `internal/graph` | centrality 24 (via graph), no doc, 5 commits, stdio transport pattern | generated |
| 9 | Conversation Context Buffer | 0.67 | `internal/api`, `internal/graph` | centrality 24, no doc, channel-based pagination + compression levels | generated |
| 10 | Embedding & Vector KNN | 0.63 | `internal/embed`, `internal/graph` | centrality 24, no doc, sqlite-vec extension integration | generated |

## Recommended next

All topics generated. Run `dev:repo-doc engram` to refresh the overview and doc-plan when significant new features land.

---
_Generated: 2026-04-06T00:00:00Z | Commit: a9c034d6_
