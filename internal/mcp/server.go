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
	s.AddTool(listTracesTool(), listTracesHandler(svc))
	s.AddTool(getTraceTool(), getTraceHandler(svc))
	s.AddTool(getTraceContextTool(), getTraceContextHandler(svc))
	s.AddTool(queryEpisodeTool(), queryEpisodeHandler(svc))

	return s
}

// --- Tool: search_memory ---

func searchMemoryTool() mcpgo.Tool {
	return mcpgo.NewTool("search_memory",
		mcpgo.WithDescription("Search memory traces by semantic similarity. Returns traces most relevant to the query."),
		mcpgo.WithString("query", mcpgo.Required(), mcpgo.Description("The search query")),
		mcpgo.WithNumber("limit", mcpgo.Description("Maximum results to return (default 10)")),
	)
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

		var queryEmb []float64
		if svc.EmbedClient != nil {
			var err error
			queryEmb, err = svc.EmbedClient.Embed(query)
			if err != nil {
				svc.Logger.Warn("MCP: query embedding failed", "err", err)
			}
		}

		result, err := svc.Graph.Retrieve(queryEmb, query, limit)
		if err != nil {
			return mcpgo.NewToolResultError(fmt.Sprintf("retrieval failed: %v", err)), nil
		}

		data, _ := json.MarshalIndent(result, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}

// --- Tool: list_traces ---

func listTracesTool() mcpgo.Tool {
	return mcpgo.NewTool("list_traces",
		mcpgo.WithDescription("List all memory traces with their IDs and content preview."),
	)
}

func listTracesHandler(svc *Services) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		traces, err := svc.Graph.GetAllTraces()
		if err != nil {
			return mcpgo.NewToolResultError(fmt.Sprintf("db error: %v", err)), nil
		}
		data, _ := json.MarshalIndent(traces, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}

// --- Tool: get_trace ---

func getTraceTool() mcpgo.Tool {
	return mcpgo.NewTool("get_trace",
		mcpgo.WithDescription("Get a specific memory trace by ID. Uses L1 compressed summaries by default."),
		mcpgo.WithString("trace_id", mcpgo.Required(), mcpgo.Description("The trace ID to retrieve (short 5-char ID or full ID)")),
		mcpgo.WithNumber("level", mcpgo.Description("Compression level: 0=raw, 1=L1 summary (default), 2=L2 summary")),
	)
}

func getTraceHandler(svc *Services) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := req.GetString("trace_id", "")
		if id == "" {
			return mcpgo.NewToolResultError("trace_id is required"), nil
		}

		level := req.GetInt("level", 1)

		trace, err := svc.Graph.GetTrace(id)
		if err != nil {
			trace, err = svc.Graph.GetTraceByShortID(id)
			if err != nil {
				return mcpgo.NewToolResultError(fmt.Sprintf("trace not found: %v", err)), nil
			}
		}

		// Apply compression level if requested
		if level > 0 {
			if summary, err2 := svc.Graph.GetTraceSummary(trace.ID, level); err2 == nil && summary != nil {
				trace.Summary = summary.Summary
			}
		}

		data, _ := json.MarshalIndent(trace, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}

// --- Tool: get_trace_context ---

func getTraceContextTool() mcpgo.Tool {
	return mcpgo.NewTool("get_trace_context",
		mcpgo.WithDescription("Get detailed context for a trace, including source episodes and linked entities."),
		mcpgo.WithString("trace_id", mcpgo.Required(), mcpgo.Description("The trace ID to get context for")),
	)
}

type traceContextResult struct {
	Trace    *graph.Trace    `json:"trace"`
	Sources  []graph.Episode `json:"source_episodes"`
	Entities []*graph.Entity `json:"linked_entities"`
}

func getTraceContextHandler(svc *Services) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		id := req.GetString("trace_id", "")
		if id == "" {
			return mcpgo.NewToolResultError("trace_id is required"), nil
		}

		trace, err := svc.Graph.GetTrace(id)
		if err != nil {
			trace, err = svc.Graph.GetTraceByShortID(id)
			if err != nil {
				return mcpgo.NewToolResultError(fmt.Sprintf("trace not found: %v", err)), nil
			}
		}

		sources, _ := svc.Graph.GetTraceSourceEpisodes(trace.ID)
		entityIDs, _ := svc.Graph.GetTraceEntities(trace.ID)
		var entities []*graph.Entity
		for _, eid := range entityIDs {
			if e, err2 := svc.Graph.GetEntity(eid); err2 == nil {
				entities = append(entities, e)
			}
		}

		result := traceContextResult{
			Trace:    trace,
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
		mcpgo.WithDescription("Get a specific episode by its ID (short 5-char ID or full ID)."),
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
		if err != nil {
			ep, err = svc.Graph.GetEpisodeByShortID(id)
			if err != nil {
				return mcpgo.NewToolResultError(fmt.Sprintf("episode not found: %v", err)), nil
			}
		}

		data, _ := json.MarshalIndent(ep, "", "  ")
		return mcpgo.NewToolResultText(string(data)), nil
	}
}
