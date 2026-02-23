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

	if cfg.LLMDeprecatedUsed() {
		logger.Warn("config: 'llm' key is deprecated; migrate to 'compression_llm', 'consolidation_llm', and 'inference_llm'")
	}

	comp := cfg.ResolvedCompressionLLM()
	cons := cfg.ResolvedConsolidationLLM()
	infer := cfg.ResolvedInferenceLLM()

	logger.Info("engram starting",
		"port", cfg.Server.Port,
		"db", cfg.Storage.Path,
		"compression_llm", comp.Provider+"/"+comp.Model,
		"consolidation_llm", cons.Provider+"/"+cons.Model,
		"inference_llm", infer.Provider+"/"+infer.Model,
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

	// Set up LLM for consolidation and episode compression.
	// Each function uses its own resolved config: compression, consolidation, inference.
	var consolidator *consolidate.Consolidator
	var compressQueue *graph.EpisodeCompressQueue
	if cfg.Consolidation.Enabled {
		compressor := buildCompressor(comp, embedClient)
		llmClient := buildConsolidationLLM(cons, embedClient, logger)
		claudeInfer := buildInferenceClient(infer)

		if llmClient != nil {
			consolidator = consolidate.NewConsolidator(db, llmClient, claudeInfer)
			consolidator.BotName = cfg.Identity.Name
			consolidator.BotAuthorID = cfg.Identity.AuthorID
			consolidator.OwnerIDs = cfg.Identity.OwnerIDs
			logger.Info("consolidation enabled", "interval", cfg.Consolidation.Interval)

			compressQueue = graph.NewEpisodeCompressQueue(db, compressor, logger)
		} else {
			logger.Warn("consolidation disabled: no LLM client available")
		}
	}

	// Wire services
	svc := &api.Services{
		Graph:         db,
		EmbedClient:   embedClient,
		NERClient:     nerClient,
		Consolidator:  consolidator,
		CompressQueue: compressQueue,
		Logger:        logger,
		BotName:       cfg.Identity.Name,
		BotAuthorID:   cfg.Identity.AuthorID,
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
		go runConsolidation(ctx, consolidator, cfg.Consolidation, logger)
	}

	if compressQueue != nil {
		go compressQueue.Start(ctx)
		logger.Info("episode compression queue started")
	}

	if cfg.Decay.Interval > 0 {
		go runDecay(ctx, db, cfg.Decay, logger)
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

	// If running as MCP (detected via ENGRAM_MCP env), serve stdio only (no REST)
	if os.Getenv("ENGRAM_MCP") == "1" {
		logger.Info("starting MCP stdio server (REST disabled)")
		if err := mcpserver.ServeStdio(mcpSrv); err != nil {
			logger.Error("MCP server error", "err", err)
		}
		return
	}

	// Start REST server
	go func() {
		logger.Info("REST server listening", "addr", restAddr)
		if err := restServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("REST server error", "err", err)
		}
	}()

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

func runConsolidation(ctx context.Context, c *consolidate.Consolidator, cfg config.ConsolidationConfig, logger *slog.Logger) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := c.ShouldRun(cfg.MinEpisodes, cfg.IdleTime, cfg.MaxBuffer)
			if err != nil {
				logger.Warn("consolidation eligibility check failed", "err", err)
				continue
			}
			if !ok {
				continue
			}
			logger.Info("background consolidation starting")
			start := time.Now()
			created, err := c.Run()
			if err != nil {
				logger.Error("background consolidation failed", "err", err)
			} else if created > 0 {
				logger.Info("background consolidation complete", "engrams_created", created, "duration_ms", time.Since(start).Milliseconds())
			}
		}
	}
}

func runDecay(ctx context.Context, db *graph.DB, cfg config.DecayConfig, logger *slog.Logger) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updated, err := db.DecayActivationByAge(cfg.Lambda, cfg.Floor)
			if err != nil {
				logger.Warn("background decay failed", "err", err)
			} else if updated > 0 {
				logger.Info("background decay complete", "engrams_updated", updated)
			}
		}
	}
}

// generatorCompressor adapts a context-aware Generator (Anthropic/claude-code)
// to the graph.Compressor interface (no context).
type generatorCompressor struct {
	gen consolidate.Generator
}

func (g *generatorCompressor) Generate(prompt string) (string, error) {
	return g.gen.Generate(context.Background(), prompt)
}

// buildCompressor creates a graph.Compressor for pyramid compression.
// Ollama uses embed.Client with the configured generation model.
// Anthropic / claude-code are wrapped via generatorCompressor.
func buildCompressor(cfg config.LLMConfig, embedClient *embed.Client) graph.Compressor {
	switch cfg.Provider {
	case "ollama":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		c := embed.NewClient(baseURL, "") // embedding not used by compressor
		c.SetGenerationModel(cfg.Model)
		return c
	case "anthropic":
		return &generatorCompressor{gen: consolidate.NewAnthropicClient(cfg.Model, cfg.APIKey, false)}
	case "claude-code":
		return &generatorCompressor{gen: consolidate.NewClaudeCodeClient(cfg.BinaryPath, cfg.Model, false)}
	}
	return nil
}

// anthropicLLMClient wraps embed.Client + Anthropic for consolidation.
// Embedding is delegated to embed.Client; Generate to Anthropic.
type anthropicLLMClient struct {
	embed     *embed.Client
	anthropic *consolidate.AnthropicClient
}

func (a *anthropicLLMClient) Embed(text string) ([]float64, error) {
	return a.embed.Embed(text)
}

func (a *anthropicLLMClient) Generate(prompt string) (string, error) {
	return a.anthropic.Generate(context.Background(), prompt)
}

// claudeCodeLLMClient satisfies consolidate.LLMClient using the claude CLI for generation
// and the Ollama embed client for embeddings.
type claudeCodeLLMClient struct {
	embed *embed.Client
	cc    *consolidate.ClaudeCodeClient
}

func (c *claudeCodeLLMClient) Embed(text string) ([]float64, error) {
	return c.embed.Embed(text)
}

func (c *claudeCodeLLMClient) Generate(prompt string) (string, error) {
	return c.cc.Generate(context.Background(), prompt)
}

// buildConsolidationLLM creates a consolidate.LLMClient for engram/trace summarization.
// Embeddings always come from embedClient (Ollama). Generation uses the resolved config.
func buildConsolidationLLM(cfg config.LLMConfig, embedClient *embed.Client, logger *slog.Logger) consolidate.LLMClient {
	if embedClient == nil {
		return nil
	}
	switch cfg.Provider {
	case "anthropic":
		return &anthropicLLMClient{
			embed:     embedClient,
			anthropic: consolidate.NewAnthropicClient(cfg.Model, cfg.APIKey, false),
		}
	case "ollama":
		// embed.Client satisfies LLMClient directly when used for both embed + generate
		c := embed.NewClient(cfg.BaseURL, embedClient.EmbedModel())
		c.SetGenerationModel(cfg.Model)
		return c
	case "claude-code":
		logger.Info("consolidation LLM backend: claude-code", "binary", cfg.BinaryPath)
		return &claudeCodeLLMClient{
			embed: embedClient,
			cc:    consolidate.NewClaudeCodeClient(cfg.BinaryPath, cfg.Model, false),
		}
	}
	return nil
}

// buildInferenceClient creates a ClaudeInference for relationship/edge detection.
func buildInferenceClient(cfg config.LLMConfig) *consolidate.ClaudeInference {
	switch cfg.Provider {
	case "anthropic":
		return consolidate.NewClaudeInference(cfg.Model, cfg.APIKey, false)
	case "ollama":
		return consolidate.NewClaudeInference(cfg.Model, cfg.APIKey, false)
	case "claude-code":
		ccc := consolidate.NewClaudeCodeClient(cfg.BinaryPath, cfg.Model, false)
		return consolidate.NewClaudeInferenceFromGenerator(ccc, false)
	}
	return nil
}
