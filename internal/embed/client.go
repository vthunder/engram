package embed

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"
)

// embeddingCache is a simple fixed-size FIFO cache for embeddings.
// It reduces repeated Ollama calls (e.g. search_memory with similar queries).
type embeddingCache struct {
	mu      sync.Mutex
	items   map[string][]float64
	order   []string
	maxSize int
}

func newEmbeddingCache(maxSize int) *embeddingCache {
	return &embeddingCache{
		items:   make(map[string][]float64, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
	}
}

func (c *embeddingCache) get(key string) ([]float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

func (c *embeddingCache) set(key string, emb []float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists {
		if len(c.order) >= c.maxSize {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.items, oldest)
		}
		c.order = append(c.order, key)
	}
	c.items[key] = emb
}

// Client handles embedding generation via Ollama
type Client struct {
	baseURL         string
	model           string
	generationModel string
	client          *http.Client
	cache           *embeddingCache
}

// NewClient creates a new Ollama embedding client
func NewClient(baseURL, model string) *Client {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text" // good default, 768 dims
	}
	return &Client{
		baseURL:         baseURL,
		model:           model,
		generationModel: "llama3.2", // fast, available by default
		client: &http.Client{
			Timeout: 300 * time.Second, // 5 minutes for long-running compressions
		},
		cache: newEmbeddingCache(256),
	}
}

// SetGenerationModel changes the model used for text generation
func (c *Client) SetGenerationModel(model string) {
	c.generationModel = model
}

// embeddingRequest is the Ollama API request format
type embeddingRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// embeddingResponse is the Ollama API response format
type embeddingResponse struct {
	Embedding []float64 `json:"embedding"`
}

// cacheKey returns a stable cache key for the given text and model.
func (c *Client) cacheKey(text string) string {
	h := sha256.Sum256([]byte(c.model + "\x00" + text))
	return fmt.Sprintf("%x", h[:16]) // 128-bit prefix is plenty
}

// Embed generates an embedding for the given text
func (c *Client) Embed(text string) ([]float64, error) {
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}

	key := c.cacheKey(text)
	if cached, ok := c.cache.get(key); ok {
		return cached, nil
	}

	reqBody := embeddingRequest{
		Model:  c.model,
		Prompt: text,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.client.Post(
		c.baseURL+"/api/embeddings",
		"application/json",
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama error (status %d): %s", resp.StatusCode, string(body))
	}

	var result embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding returned")
	}

	c.cache.set(key, result.Embedding)
	return result.Embedding, nil
}

// generateRequest is the Ollama API request format for generation
type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

// generateResponse is the Ollama API response format for generation
type generateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// Generate creates text completion using Ollama
func (c *Client) Generate(prompt string) (string, error) {
	if prompt == "" {
		return "", fmt.Errorf("empty prompt")
	}

	reqBody := generateRequest{
		Model:  c.generationModel,
		Prompt: prompt,
		Stream: false,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	start := time.Now()
	resp, err := c.client.Post(
		c.baseURL+"/api/generate",
		"application/json",
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return "", fmt.Errorf("ollama request (took %s): %w", time.Since(start), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama error (status %d, took %s): %s", resp.StatusCode, time.Since(start), string(body))
	}

	var result generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response (took %s): %w", time.Since(start), err)
	}

	return result.Response, nil
}

// Summarize creates a faithful summary of multiple text fragments.
// It extracts only what is explicitly stated — no inference or embellishment.
func (c *Client) Summarize(fragments []string) (string, error) {
	if len(fragments) == 0 {
		return "", fmt.Errorf("no fragments to summarize")
	}

	prompt := `Summarize the following messages in 1-2 sentences.

Rules:
- Include ONLY information explicitly stated in the input — do NOT infer, interpret, or embellish
- Write in third person (e.g. "Alice prefers..." not "I observed...")
- Be concise and neutral
- Output ONLY the summary, no preamble or commentary

Examples:
Input: "My favorite coffee shop is Blue Bottle on Market Street"
Summary: Their favorite coffee shop is Blue Bottle on Market Street.

Input: "Sarah is my cofounder, she handles product"
Summary: Sarah is their cofounder who handles the product side.

Input: "Alice prefers morning meetings"
Summary: Alice prefers morning meetings.

Input: "We decided to use Postgres instead of MySQL for better JSON support"
Summary: They decided to use Postgres instead of MySQL for better JSON support.

Input:
`
	for _, f := range fragments {
		prompt += f + "\n"
	}
	prompt += "\nSummary:"

	return c.Generate(prompt)
}

// CosineSimilarity computes similarity between two embeddings (-1 to 1)
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// AverageEmbeddings computes the centroid of multiple embeddings
func AverageEmbeddings(embeddings [][]float64) []float64 {
	if len(embeddings) == 0 {
		return nil
	}

	dims := len(embeddings[0])
	result := make([]float64, dims)

	for _, emb := range embeddings {
		if len(emb) != dims {
			continue // skip mismatched dimensions
		}
		for i, v := range emb {
			result[i] += v
		}
	}

	n := float64(len(embeddings))
	for i := range result {
		result[i] /= n
	}

	return result
}

// UpdateCentroid updates a centroid with a new embedding using exponential moving average
func UpdateCentroid(current, new []float64, alpha float64) []float64 {
	if len(current) == 0 {
		return new
	}
	if len(new) == 0 {
		return current
	}
	if len(current) != len(new) {
		return new // dimension mismatch, use new
	}

	result := make([]float64, len(current))
	for i := range current {
		result[i] = alpha*new[i] + (1-alpha)*current[i]
	}
	return result
}
