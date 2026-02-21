package consolidate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/vthunder/engram/internal/graph"
)

// AnthropicClient calls the Anthropic Messages API directly.
// Model defaults to claude-sonnet-4-6 if not set.
type AnthropicClient struct {
	model   string
	apiKey  string
	verbose bool

	totalInputTokens       int
	totalOutputTokens      int
	totalCacheReadTokens   int
	totalCacheCreateTokens int
	sessionCount           int

	httpClient *http.Client
}

// NewAnthropicClient creates a new Anthropic LLM client.
func NewAnthropicClient(model, apiKey string, verbose bool) *AnthropicClient {
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return &AnthropicClient{
		model:      model,
		apiKey:     apiKey,
		verbose:    verbose,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// Generate sends a prompt to Claude and returns the text response.
func (c *AnthropicClient) Generate(ctx context.Context, prompt string) (string, error) {
	reqBody := map[string]any{
		"model":      c.model,
		"max_tokens": 4096,
		"messages": []map[string]any{
			{"role": "user", "content": prompt},
		},
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	c.totalInputTokens += result.Usage.InputTokens
	c.totalOutputTokens += result.Usage.OutputTokens
	c.sessionCount++

	var sb strings.Builder
	for _, block := range result.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return sb.String(), nil
}

// ClaudeInference provides Claude-powered relationship inference during consolidation.
type ClaudeInference struct {
	client  Generator
	verbose bool
}

// NewClaudeInference creates a new Claude inference client using the Anthropic API.
func NewClaudeInference(model, apiKey string, verbose bool) *ClaudeInference {
	return &ClaudeInference{
		client:  NewAnthropicClient(model, apiKey, verbose),
		verbose: verbose,
	}
}

// NewClaudeInferenceFromGenerator creates a ClaudeInference using any Generator backend.
// Use this with ClaudeCodeClient to avoid needing an Anthropic API key.
func NewClaudeInferenceFromGenerator(gen Generator, verbose bool) *ClaudeInference {
	return &ClaudeInference{
		client:  gen,
		verbose: verbose,
	}
}

// EpisodeForInference provides episode data for Claude inference.
type EpisodeForInference interface {
	GetID() string
	GetAuthor() string
	GetTimestamp() time.Time
	GetSummaryC16() string
}

// EpisodeEdge represents a relationship between two episodes.
type EpisodeEdge struct {
	FromID       string
	ToID         string
	Relationship string
	Confidence   float64
}

// InferEpisodeEdges analyzes a batch of episodes and infers relationships between them.
func (c *ClaudeInference) InferEpisodeEdges(ctx context.Context, episodes []EpisodeForInference) ([]EpisodeEdge, error) {
	if len(episodes) == 0 {
		return nil, nil
	}

	prompt := c.buildEpisodeInferencePrompt(episodes)

	if c.verbose {
		log.Printf("[claude-inference] Sending %d episodes to Claude for edge inference", len(episodes))
	}

	output, err := c.client.Generate(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("claude inference failed: %w", err)
	}

	var response struct {
		Edges []struct {
			FromID       string  `json:"from_id"`
			ToID         string  `json:"to_id"`
			Relationship string  `json:"relationship"`
			Confidence   float64 `json:"confidence"`
		} `json:"edges"`
	}

	extracted := extractJSON(output)
	if err := json.Unmarshal([]byte(extracted), &response); err != nil {
		if c.verbose {
			log.Printf("[claude-inference] Warning: Failed to parse JSON response, skipping batch")
		}
		return nil, nil
	}

	edges := make([]EpisodeEdge, 0, len(response.Edges))
	for _, e := range response.Edges {
		edges = append(edges, EpisodeEdge{
			FromID:       e.FromID,
			ToID:         e.ToID,
			Relationship: e.Relationship,
			Confidence:   e.Confidence,
		})
	}

	if c.verbose {
		log.Printf("[claude-inference] Inferred %d episode edges", len(edges))
	}

	return edges, nil
}

// InferEngramRelationship analyzes an episode and an engram to determine their relationship.
func (c *ClaudeInference) InferEngramRelationship(ctx context.Context, ep *graph.Episode, engram *graph.Engram) (string, float64, error) {
	prompt := c.buildEngramRelationshipPrompt(ep, engram)

	output, err := c.client.Generate(ctx, prompt)
	if err != nil {
		return "", 0, fmt.Errorf("claude inference failed: %w", err)
	}

	var response struct {
		Relationship string  `json:"relationship"`
		Confidence   float64 `json:"confidence"`
	}

	extracted := extractJSON(output)
	if err := json.Unmarshal([]byte(extracted), &response); err != nil {
		return "", 0, fmt.Errorf("failed to parse inference result: %w\nOutput: %s", err, output)
	}

	return response.Relationship, response.Confidence, nil
}

func (c *ClaudeInference) buildEpisodeInferencePrompt(episodes []EpisodeForInference) string {
	var sb strings.Builder

	sb.WriteString(`You are analyzing conversation episodes to identify semantic relationships between them.

For each pair of related episodes, determine:
1. The semantic relationship (e.g., "elaborates on", "answers", "asks about", "follows up on", "contradicts", "agrees with")
2. Confidence score (0.0-1.0)

Only return relationships with confidence >= 0.7.

Episodes (16-word summaries):
`)

	for _, ep := range episodes {
		author := ep.GetAuthor()
		if author == "" {
			author = "unknown"
		}
		sb.WriteString(fmt.Sprintf("\nID: %s\n", ep.GetID()))
		sb.WriteString(fmt.Sprintf("Author: %s\n", author))
		sb.WriteString(fmt.Sprintf("Timestamp: %s\n", ep.GetTimestamp().Format("2006-01-02 15:04:05")))
		sb.WriteString(fmt.Sprintf("Summary: %s\n", ep.GetSummaryC16()))
	}

	sb.WriteString(`
Return your analysis as JSON:

{
  "edges": [
    {
      "from_id": "episode-123",
      "to_id": "episode-456",
      "relationship": "asks about",
      "confidence": 0.9
    }
  ]
}

Link episodes that:
- Are part of the same conversation turn (same author, close in time)
- Discuss the same specific event or topic
- One elaborates on, responds to, or continues the other

Use high confidence (0.8+) for same-author sequential episodes about the same topic.
Use medium confidence (0.6-0.7) for cross-author semantic relationships.
`)

	return sb.String()
}

func (c *ClaudeInference) buildEngramRelationshipPrompt(ep *graph.Episode, engram *graph.Engram) string {
	var sb strings.Builder

	sb.WriteString(`You are analyzing the relationship between a new conversation episode and an existing memory engram.

Determine:
1. The semantic relationship (e.g., "provides example of", "updates", "contradicts", "reinforces", "relates to")
2. Confidence score (0.0-1.0)

Episode:
`)

	author := ep.Author
	if author == "" {
		author = "unknown"
	}
	sb.WriteString(fmt.Sprintf("Author: %s\n", author))
	sb.WriteString(fmt.Sprintf("Timestamp: %s\n", ep.TimestampEvent.Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("Content: %s\n\n", ep.Content))

	sb.WriteString(fmt.Sprintf(`Memory Engram:
Summary: %s

Return your analysis as JSON:

{
  "relationship": "provides example of",
  "confidence": 0.85
}

If there's no meaningful relationship, set confidence to 0.0.
`, engram.Summary))

	return sb.String()
}

// extractJSON extracts JSON from markdown code blocks or returns the input as-is.
func extractJSON(s string) string {
	if start := strings.Index(s, "```json"); start != -1 {
		start += 7
		if end := strings.Index(s[start:], "```"); end != -1 {
			return strings.TrimSpace(s[start : start+end])
		}
	}
	if start := strings.Index(s, "```"); start != -1 {
		start += 3
		if end := strings.Index(s[start:], "```"); end != -1 {
			content := strings.TrimSpace(s[start : start+end])
			if idx := strings.Index(content, "\n"); idx != -1 {
				content = content[idx+1:]
			}
			return strings.TrimSpace(content)
		}
	}
	return strings.TrimSpace(s)
}

// GetTokenStats returns total token usage (only available when using AnthropicClient).
func (c *ClaudeInference) GetTokenStats() (inputTokens, outputTokens, cacheReadTokens, cacheCreateTokens, sessionCount int) {
	if ac, ok := c.client.(*AnthropicClient); ok {
		return ac.totalInputTokens, ac.totalOutputTokens,
			ac.totalCacheReadTokens, ac.totalCacheCreateTokens,
			ac.sessionCount
	}
	return 0, 0, 0, 0, 0
}
