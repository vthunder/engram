package consolidate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// Generator is a minimal interface for LLM text generation.
// Both AnthropicClient and ClaudeCodeClient implement this.
type Generator interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// ClaudeCodeClient calls the local `claude` CLI binary for one-shot inference.
// This lets users with a Claude subscription use Engram without a separate API key.
type ClaudeCodeClient struct {
	binaryPath string // path to claude binary (default: "claude")
	model      string
	verbose    bool
}

// NewClaudeCodeClient creates a ClaudeCodeClient using the `claude` CLI.
// binaryPath defaults to "claude" (assumes it's on PATH).
// model is optional; if empty, Claude picks its default.
func NewClaudeCodeClient(binaryPath, model string, verbose bool) *ClaudeCodeClient {
	if binaryPath == "" {
		binaryPath = "claude"
	}
	return &ClaudeCodeClient{
		binaryPath: binaryPath,
		model:      model,
		verbose:    verbose,
	}
}

// Generate sends a one-shot prompt to the Claude CLI and returns the response text.
func (c *ClaudeCodeClient) Generate(ctx context.Context, prompt string) (string, error) {
	args := []string{
		"--print",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose", // required with --print + stream-json
	}
	if c.model != "" {
		args = append(args, "--model", c.model)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, c.binaryPath, args...) //nolint:gosec
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("claude-code: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("claude-code: stderr pipe: %w", err)
	}

	if c.verbose {
		log.Printf("[claude-code] invoking %s, prompt length %d chars", c.binaryPath, len(prompt))
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("claude-code: failed to start %q: %w", c.binaryPath, err)
	}

	var wg sync.WaitGroup
	var output strings.Builder
	var stderrBuf strings.Builder

	wg.Add(2)
	go func() {
		defer wg.Done()
		parseClaudeStreamJSON(stdout, &output, c.verbose)
	}()
	go func() {
		defer wg.Done()
		drainStderr(stderr, &stderrBuf, c.verbose)
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		errMsg := stderrBuf.String()
		if errMsg != "" {
			return "", fmt.Errorf("claude-code: exited with error: %w\nstderr: %s", err, errMsg)
		}
		return "", fmt.Errorf("claude-code: exited with error: %w", err)
	}

	result := output.String()
	if c.verbose {
		log.Printf("[claude-code] response length %d chars", len(result))
	}
	return result, nil
}

// parseClaudeStreamJSON reads Claude's stream-json output and collects text from result/delta events.
func parseClaudeStreamJSON(r io.Reader, output *strings.Builder, verbose bool) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event struct {
			Type    string          `json:"type"`
			Result  json.RawMessage `json:"result"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			if verbose {
				log.Printf("[claude-code] parse error: %v", err)
			}
			continue
		}

		switch event.Type {
		case "result":
			var text string
			if err := json.Unmarshal(event.Result, &text); err == nil && text != "" {
				output.WriteString(text)
			}
		case "content_block_delta":
			var delta struct {
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal(event.Content, &delta); err == nil && delta.Delta.Text != "" {
				output.WriteString(delta.Delta.Text)
			}
		}
	}

	if err := scanner.Err(); err != nil && verbose {
		log.Printf("[claude-code] scanner error: %v", err)
	}
}

func drainStderr(r io.Reader, buf *strings.Builder, verbose bool) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if verbose {
			log.Printf("[claude-code stderr] %s", line)
		}
		buf.WriteString(line + "\n")
	}
}
