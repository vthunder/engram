// Command engram runs the Engram memory service.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/vthunder/engram/config"
	"github.com/vthunder/engram/internal/api"
	"github.com/vthunder/engram/internal/consolidate"
	"github.com/vthunder/engram/internal/embed"
	"github.com/vthunder/engram/internal/graph"
	engrammcp "github.com/vthunder/engram/internal/mcp"
	"github.com/vthunder/engram/internal/ner"
)

func main() {
	configFile := flag.String("config", "", "path to config file (default: ./engram.yaml)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Load config
	path := *configFile
	if path == "" {
		if _, err := os.Stat("engram.yaml"); err == nil {
			path = "engram.yaml"
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	logger.Info("engram starting",
		"port", cfg.Server.Port,
		"db", cfg.Storage.Path,
		"llm", cfg.LLM.Provider,
		"model", cfg.LLM.Model,
	)

	// Open graph database
	db, err := graph.Open(cfg.Storage.Path)
	if err != nil {
		logger.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	// Set up embedding client
	var embedClient *embed.Client
	if cfg.Embedding.BaseURL != "" {
		embedClient = embed.NewClient(cfg.Embedding.BaseURL, cfg.Embedding.Model)
		logger.Info("embedding client configured", "base_url", cfg.Embedding.BaseURL, "model", cfg.Embedding.Model)
	}

	// Set up NER client (spaCy provider only; Ollama NER handled separately)
	var nerClient *ner.Client
	if cfg.NER.Provider == "spacy" && cfg.NER.SpacyURL != "" {
		nerClient = ner.NewClient(cfg.NER.SpacyURL)
		logger.Info("NER client configured", "provider", "spacy", "url", cfg.NER.SpacyURL)
	}

	// Set up LLM for consolidation
	var consolidator *consolidate.Consolidator
	if cfg.Consolidation.Enabled {
		var llmClient consolidate.LLMClient
		switch cfg.LLM.Provider {
		case "anthropic":
			if embedClient != nil {
				llmClient = newAnthropicLLMClient(embedClient, cfg.LLM.APIKey, cfg.LLM.Model, logger)
			}
		case "ollama":
			if embedClient != nil {
				llmClient = embedClient // embed.Client satisfies LLMClient (Embed + Generate + Summarize)
			}
		}

		if llmClient != nil {
			claudeInfer := consolidate.NewClaudeInference(cfg.LLM.Model, cfg.LLM.APIKey, false)
			consolidator = consolidate.NewConsolidator(db, llmClient, claudeInfer)
			logger.Info("consolidation enabled", "interval", cfg.Consolidation.Interval)
		} else {
			logger.Warn("consolidation disabled: no LLM client available")
		}
	}

	// Wire services
	svc := &api.Services{
		Graph:        db,
		EmbedClient:  embedClient,
		NERClient:    nerClient,
		Consolidator: consolidator,
		Logger:       logger,
	}

	mcpSvc := &engrammcp.Services{
		Graph:       db,
		EmbedClient: embedClient,
		NERClient:   nerClient,
		Logger:      logger,
	}

	// Background consolidation goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if consolidator != nil && cfg.Consolidation.Interval > 0 {
		go runConsolidation(ctx, consolidator, cfg.Consolidation.Interval, logger)
	}

	// REST server
	router := api.NewRouter(svc, cfg.Server.APIKey)
	restAddr := fmt.Sprintf(":%d", cfg.Server.Port)
	restServer := &http.Server{
		Addr:         restAddr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	// MCP server (stdio transport — agents connect via stdin/stdout)
	mcpSrv := engrammcp.NewServer(mcpSvc)

	// Start REST server
	go func() {
		logger.Info("REST server listening", "addr", restAddr)
		if err := restServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("REST server error", "err", err)
		}
	}()

	// If running as MCP (detected via --mcp flag or ENGRAM_MCP env), serve stdio
	if os.Getenv("ENGRAM_MCP") == "1" {
		logger.Info("starting MCP stdio server")
		if err := mcpserver.ServeStdio(mcpSrv); err != nil {
			logger.Error("MCP server error", "err", err)
		}
		return
	}

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := restServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("REST server shutdown error", "err", err)
	}
	logger.Info("engram stopped")
}

func runConsolidation(ctx context.Context, c *consolidate.Consolidator, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			created, err := c.Run()
			if err != nil {
				logger.Error("background consolidation failed", "err", err)
			} else if created > 0 {
				logger.Info("background consolidation complete", "traces_created", created, "duration_ms", time.Since(start).Milliseconds())
			}
		}
	}
}

// anthropicLLMClient wraps embed.Client + Anthropic for consolidation.
// Embedding is delegated to embed.Client; Generate/Summarize to Anthropic.
type anthropicLLMClient struct {
	embed    *embed.Client
	anthropic *consolidate.AnthropicClient
}

func newAnthropicLLMClient(embedClient *embed.Client, apiKey, model string, logger *slog.Logger) consolidate.LLMClient {
	return &anthropicLLMClient{
		embed:    embedClient,
		anthropic: consolidate.NewAnthropicClient(model, apiKey, false),
	}
}

func (a *anthropicLLMClient) Embed(text string) ([]float64, error) {
	return a.embed.Embed(text)
}

func (a *anthropicLLMClient) Summarize(fragments []string) (string, error) {
	return a.embed.Summarize(fragments)
}

func (a *anthropicLLMClient) Generate(prompt string) (string, error) {
	return a.anthropic.Generate(context.Background(), prompt)
}
