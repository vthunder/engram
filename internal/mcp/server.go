// Package mcp exposes Engram's memory tools as a native MCP server.
// Agents (Claude Desktop, claude-code, etc.) can point at this server directly.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/vthunder/engram/internal/embed"
	"github.com/vthunder/engram/internal/graph"
	"github.com/vthunder/engram/internal/ner"
)

// Services groups the dependencies needed by MCP handlers.
type Services struct {
	Graph       *graph.DB
	EmbedClient *embed.Client
	NERClient   *ner.Client
	Logger      *slog.Logger
}

// NewServer builds an MCP server with the Engram tool set.
func NewServer(svc *Services) *server.MCPServer {
	s := server.NewMCPServer("engram", "0.1.0",
		server.WithToolCapabilities(true),
	)

	s.AddTool(searchMemoryTool(), searchMemoryHandler(svc))
	s.AddTool(listEngramsTool(), listEngramsHandler(svc))
	s.AddTool(getEngramTool(), getEngramHandler(svc))
	s.AddTool(getEngramContextTool(), getEngramContextHandler(svc))
	s.AddTool(queryEpisodeTool(), queryEpisodeHandler(svc))

	return s
}

// --- Tool: search_memory ---

func searchMemoryTool() mcpgo.Tool {
	return mcpgo.NewTool("search_memory",
		mcpgo.WithDescription("Search memory engrams by semantic similarity. Returns engrams most relevant to the query."),
		mcpgo.WithString("query", mcpgo.Required(), mcpgo.Description("The search query")),
		mcpgo.WithNumber("limit", mcpgo.Description("Maximum results to return (default 10)")),
	)
}

// nerEntityEngrams extracts named entities from queryStr via NER and returns engram IDs
// linked to those entities, for extra-seeding spreading activation at retrieval time.
func nerEntityEngrams(svc *Services, queryStr string) []string {
	if svc.NERClient == nil || svc.Graph == nil {
		return nil
	}
	resp, err := svc.NERClient.Extract(queryStr)
	if err != nil || !resp.HasEntities {
		return nil
	}
	seen := make(map[string]bool)
	var engramIDs []string
	for _, e := range resp.Entities {
		entity, err := svc.Graph.FindEntityByName(e.Text)
		if err != nil || entity == nil {
			continue
		}
		ids, err := svc.Graph.GetEngramsForEntitiesBatch([]string{entity.ID}, 3)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				engramIDs = append(engramIDs, id)
			}
		}
	}
	return engramIDs
}

func searchMemoryHandler(svc *Services) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		query := req.GetString("query", "")
		if query == "" {
			return mcpgo.NewToolResultError("query is required"), nil
		}

		limit := req.GetInt("limit", 10)
		if limit <= 0 {
			limit = 10
		}

		// Run embedding and NER concurrently — both are network calls.
		embCh := make(chan []float64, 1)
		seedCh := make(chan []string, 1)

		if svc.EmbedClient != nil {
			go func() {
				emb, err := svc.EmbedClient.Embed(query)
				if err != nil {
					svc.Logger.Warn("MCP: query embedding failed", "err", err)
					embCh <- nil
					return
				}
				embCh <- emb
			}()
		} else {
			embCh <- nil
		}

		go func() { seedCh <- nerEntityEngrams(svc, query) }()

		queryEmb := <-embCh
		extraSeeds := <-seedCh

		result, err := svc.Graph.Retrieve(queryEmb, query, limit, extraSeeds...)
		if err != nil {
			return mcpgo.NewToolResultError(fmt.Sprintf("retrieval failed: %v", err)), nil
		}

		data, _ := json.MarshalIndent(result, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}

// --- Tool: list_engrams ---

func listEngramsTool() mcpgo.Tool {
	return mcpgo.NewTool("list_engrams",
		mcpgo.WithDescription("List all memory engrams with their IDs and content preview."),
	)
}

func listEngramsHandler(svc *Services) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		engrams, err := svc.Graph.GetAllEngrams()
		if err != nil {
			return mcpgo.NewToolResultError(fmt.Sprintf("db error: %v", err)), nil
		}
		data, _ := json.MarshalIndent(engrams, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}

// --- Tool: get_engram ---

func getEngramTool() mcpgo.Tool {
	return mcpgo.NewTool("get_engram",
		mcpgo.WithDescription("Get a specific memory engram by ID. Uses L1 compressed summaries by default."),
		mcpgo.WithString("engram_id", mcpgo.Required(), mcpgo.Description("The engram ID to retrieve (prefix or full 32-char ID)")),
		mcpgo.WithNumber("level", mcpgo.Description("Compression level: 0=raw, 1=L1 summary (default), 2=L2 summary")),
	)
}

func getEngramHandler(svc *Services) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := req.GetString("engram_id", "")
		if id == "" {
			return mcpgo.NewToolResultError("engram_id is required"), nil
		}

		level := req.GetInt("level", 1)

		engram, err := svc.Graph.GetEngram(id)
		if err != nil || engram == nil {
			fullID, resolveErr := svc.Graph.ResolveEngramID(id)
			if resolveErr != nil {
				return mcpgo.NewToolResultError(fmt.Sprintf("engram not found: %v", resolveErr)), nil
			}
			engram, err = svc.Graph.GetEngram(fullID)
			if err != nil || engram == nil {
				return mcpgo.NewToolResultError("engram not found"), nil
			}
		}

		// Apply compression level if requested
		if level > 0 {
			if summary, err2 := svc.Graph.GetEngramSummary(engram.ID, level); err2 == nil && summary != nil {
				engram.Summary = summary.Summary
			}
		}

		data, _ := json.MarshalIndent(engram, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}

// --- Tool: get_engram_context ---

func getEngramContextTool() mcpgo.Tool {
	return mcpgo.NewTool("get_engram_context",
		mcpgo.WithDescription("Get detailed context for an engram, including source episodes and linked entities."),
		mcpgo.WithString("engram_id", mcpgo.Required(), mcpgo.Description("The engram ID to get context for")),
	)
}

type engramContextResult struct {
	Engram   *graph.Engram   `json:"engram"`
	Sources  []graph.Episode `json:"source_episodes"`
	Entities []*graph.Entity `json:"linked_entities"`
}

func getEngramContextHandler(svc *Services) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := req.GetString("engram_id", "")
		if id == "" {
			return mcpgo.NewToolResultError("engram_id is required"), nil
		}

		engram, err := svc.Graph.GetEngram(id)
		if err != nil || engram == nil {
			fullID, resolveErr := svc.Graph.ResolveEngramID(id)
			if resolveErr != nil {
				return mcpgo.NewToolResultError(fmt.Sprintf("engram not found: %v", resolveErr)), nil
			}
			engram, err = svc.Graph.GetEngram(fullID)
			if err != nil || engram == nil {
				return mcpgo.NewToolResultError("engram not found"), nil
			}
		}

		sources, _ := svc.Graph.GetEngramSourceEpisodes(engram.ID)
		entityIDs, _ := svc.Graph.GetEngramEntities(engram.ID)
		var entities []*graph.Entity
		for _, eid := range entityIDs {
			if e, err2 := svc.Graph.GetEntity(eid); err2 == nil {
				entities = append(entities, e)
			}
		}

		result := engramContextResult{
			Engram:   engram,
			Sources:  sources,
			Entities: entities,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}

// --- Tool: query_episode ---

func queryEpisodeTool() mcpgo.Tool {
	return mcpgo.NewTool("query_episode",
		mcpgo.WithDescription("Get a specific episode by its ID (prefix or full 32-char ID)."),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("Episode ID")),
	)
}

func queryEpisodeHandler(svc *Services) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := req.GetString("id", "")
		if id == "" {
			return mcpgo.NewToolResultError("id is required"), nil
		}

		ep, err := svc.Graph.GetEpisode(id)
		if err != nil || ep == nil {
			fullID, resolveErr := svc.Graph.ResolveEpisodeID(id)
			if resolveErr != nil {
				return mcpgo.NewToolResultError(fmt.Sprintf("episode not found: %v", resolveErr)), nil
			}
			ep, err = svc.Graph.GetEpisode(fullID)
			if err != nil || ep == nil {
				return mcpgo.NewToolResultError("episode not found"), nil
			}
		}

		data, _ := json.MarshalIndent(ep, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}
