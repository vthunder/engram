package graph

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// EpisodeSummary represents a compressed version of an episode
type EpisodeSummary struct {
	ID               int    `json:"id"`
	EpisodeID        string `json:"episode_id"`
	CompressionLevel int    `json:"compression_level"`
	Summary          string `json:"summary"`
	Tokens           int    `json:"tokens"`
}

// CompressionLevel represents compression targets by max words
// Level number IS the target word count (L4=4 words, L8=8 words, etc.)
// Level 0 = verbatim/original content (no compression)
const (
	CompressionLevelVerbatim = 0  // Verbatim/original content (no compression)
	CompressionLevel4        = 4  // 4 words max
	CompressionLevel8        = 8  // 8 words max
	CompressionLevel16       = 16 // 16 words max
	CompressionLevel32       = 32 // 32 words max
	CompressionLevel64       = 64 // 64 words max
	CompressionLevelMax      = 64 // Maximum compression level
)

// AddEpisodeSummary stores a summary for an episode at a given compression level
func (g *DB) AddEpisodeSummary(episodeID string, level int, summary string, tokens int) error {
	_, err := g.db.Exec(`
		INSERT OR REPLACE INTO episode_summaries (episode_id, compression_level, summary, tokens)
		VALUES (?, ?, ?, ?)
	`, episodeID, level, summary, tokens)
	return err
}

// DeleteAllEpisodeSummaries removes all episode summaries from the database
func (g *DB) DeleteAllEpisodeSummaries() error {
	_, err := g.db.Exec(`DELETE FROM episode_summaries`)
	return err
}

// GetEpisodeSummary retrieves a summary for an episode at a specific compression level
// Falls back to higher compression levels if the requested level doesn't exist
// Returns nil if no summary exists (caller should fall back to episodes.content)
func (g *DB) GetEpisodeSummary(episodeID string, level int) (*EpisodeSummary, error) {
	// Try requested level first, then higher levels
	for lvl := level; lvl <= CompressionLevelMax; lvl++ {
		var summary EpisodeSummary
		err := g.db.QueryRow(`
			SELECT id, episode_id, compression_level, summary, tokens
			FROM episode_summaries
			WHERE episode_id = ? AND compression_level = ?
		`, episodeID, lvl).Scan(
			&summary.ID,
			&summary.EpisodeID,
			&summary.CompressionLevel,
			&summary.Summary,
			&summary.Tokens,
		)
		if err == nil {
			return &summary, nil
		}
	}
	// No summary found - caller should use episodes.content
	return nil, nil
}

// CompressEpisode generates summaries at different compression levels
// Uses Ollama/Qwen2.5:7b for compression
type Compressor interface {
	Generate(prompt string) (string, error)
}

// hasCJK returns true if the text contains any CJK (Chinese/Japanese/Korean) characters
func hasCJK(text string) bool {
	re := regexp.MustCompile(`[\x{4E00}-\x{9FFF}]`)
	return re.MatchString(text)
}

// GenerateEpisodeSummaries creates summaries at all compression levels (L4-L64) for an episode
// Always generates all levels - uses verbatim text if episode already below target word count
func (g *DB) GenerateEpisodeSummaries(episode Episode, compressor Compressor) error {
	// Generate all compression levels asynchronously
	if compressor != nil {
		go g.generateCompressedSummaries(episode, compressor)
	}

	return nil
}

// generateCompressedSummaries creates all compression levels (L4-L64) for every episode
// Uses verbatim text if episode is already below target word count
func (g *DB) generateCompressedSummaries(episode Episode, compressor Compressor) {
	// Strip author prefix and calculate word count on clean content
	cleanContent := stripAuthorPrefix(episode.Content)
	wordCount := estimateWordCount(cleanContent)

	// Level 4: 4 words max
	summary, err := compressToTarget(episode, compressor, 4, wordCount)
	if err == nil {
		tokens := estimateTokens(summary)
		g.AddEpisodeSummary(episode.ID, CompressionLevel4, summary, tokens)
	}

	// Level 8: 8 words max
	summary, err = compressToTarget(episode, compressor, 8, wordCount)
	if err == nil {
		tokens := estimateTokens(summary)
		g.AddEpisodeSummary(episode.ID, CompressionLevel8, summary, tokens)
	}

	// Level 16: 16 words max
	summary, err = compressToTarget(episode, compressor, 16, wordCount)
	if err == nil {
		tokens := estimateTokens(summary)
		g.AddEpisodeSummary(episode.ID, CompressionLevel16, summary, tokens)
	}

	// Level 32: 32 words max
	summary, err = compressToTarget(episode, compressor, 32, wordCount)
	if err == nil {
		tokens := estimateTokens(summary)
		g.AddEpisodeSummary(episode.ID, CompressionLevel32, summary, tokens)
	}

	// Level 64: 64 words max
	summary, err = compressToTarget(episode, compressor, 64, wordCount)
	if err == nil {
		tokens := estimateTokens(summary)
		g.AddEpisodeSummary(episode.ID, CompressionLevel64, summary, tokens)
	}
}

// compressToTarget compresses episode to target word count or returns verbatim if already below target
func compressToTarget(episode Episode, compressor Compressor, targetWords int, currentWords int) (string, error) {
	// Strip author prefix from content (it's redundant metadata)
	cleanContent := stripAuthorPrefix(episode.Content)

	// Recalculate word count on cleaned content
	cleanWordCount := estimateWordCount(cleanContent)

	// If episode is already below target, use verbatim text
	if cleanWordCount <= targetWords {
		return cleanContent, nil
	}

	// Otherwise compress to target
	prompt := buildCompressionPrompt(episode.Author, cleanContent, targetWords)
	summary, err := compressor.Generate(prompt)
	if err != nil {
		return "", err
	}

	// Check for CJK leakage: if output has CJK but input doesn't, re-summarize with fallback
	if hasCJK(summary) && !hasCJK(cleanContent) {
		// Try fallback with Mistral (English-focused model)
		if mistralCompressor, ok := compressor.(interface{ SetGenerationModel(string) }); ok {
			mistralCompressor.SetGenerationModel("mistral")
			fallbackSummary, fallbackErr := compressor.Generate(prompt)
			if fallbackErr == nil && !hasCJK(fallbackSummary) {
				return fallbackSummary, nil
			}
			// If fallback also failed, fall through to return original summary
			// Reset model back to default
			mistralCompressor.SetGenerationModel("llama3.2")
		}
	}

	// Check for reverse case: input has CJK but output doesn't (over-correction)
	if !hasCJK(summary) && hasCJK(cleanContent) {
		fmt.Printf("WARNING: Episode %s - Input contains CJK characters but output doesn't. Possible language stripping.\n", episode.ID)
	}

	return summary, nil
}

// stripAuthorPrefix removes "AuthorName:" prefix from beginning of content
// Example: "Bud: hello world" -> "hello world"
func stripAuthorPrefix(content string) string {
	// Look for pattern: "word(s): " at the start
	idx := strings.Index(content, ": ")
	if idx == -1 || idx > 50 { // Limit search to first 50 chars to avoid false matches
		return content
	}

	// Check if prefix looks like an author name (no whitespace before colon)
	prefix := content[:idx]
	if strings.ContainsAny(prefix, " \t\n") {
		return content
	}

	// Strip the prefix and return trimmed content
	return strings.TrimSpace(content[idx+2:])
}

// estimateWordCount provides rough word count estimate
func estimateWordCount(text string) int {
	// Simple word count: split on whitespace
	words := strings.Fields(text)
	return len(words)
}

// buildCompressionPrompt constructs a prompt for episode compression to target word count
func buildCompressionPrompt(author string, content string, targetWords int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Compress this message to EXACTLY %d words or fewer.\n\n", targetWords))
	sb.WriteString("STRICT RULES:\n")
	sb.WriteString(fmt.Sprintf("- Output MUST be %d words or less - NO EXCEPTIONS\n", targetWords))
	sb.WriteString("- Count every word carefully before responding\n")
	sb.WriteString("- Do NOT include author name in output (it's already metadata)\n")
	sb.WriteString("- Keep only the essential core meaning\n")
	sb.WriteString("- Remove ALL filler, small talk, and redundancy\n")
	sb.WriteString("- Preserve key facts and decisions only\n")
	sb.WriteString("- CRITICAL: You MUST write ONLY in English - NO Chinese characters allowed\n")
	sb.WriteString("- If you write ANY Chinese characters (像这样的字符), the output will be REJECTED\n")
	sb.WriteString("- Use ONLY English words from A-Z - absolutely NO non-English characters\n")

	sb.WriteString("\nMessage context:\n")
	sb.WriteString(fmt.Sprintf("- Author: %s (DO NOT include in output)\n", author))

	sb.WriteString("\nOriginal message:\n")
	sb.WriteString(content)
	sb.WriteString(fmt.Sprintf("\n\nCompressed version (%d words max):", targetWords))

	return sb.String()
}

// estimateTokens provides a rough token count estimate (4 chars ≈ 1 token)
func estimateTokens(text string) int {
	chars := utf8.RuneCountInString(text)
	return max(1, chars/4)
}

// GetEpisodeSummariesBatch retrieves summaries for multiple episodes at specified levels
// Returns a map of episode_id -> EpisodeSummary
func (g *DB) GetEpisodeSummariesBatch(episodeIDs []string, level int) (map[string]*EpisodeSummary, error) {
	if len(episodeIDs) == 0 {
		return make(map[string]*EpisodeSummary), nil
	}

	// Build query with placeholders
	placeholders := make([]string, len(episodeIDs))
	args := make([]interface{}, len(episodeIDs)+1)
	args[0] = level
	for i, id := range episodeIDs {
		placeholders[i] = "?"
		args[i+1] = id
	}

	query := fmt.Sprintf(`
		SELECT id, episode_id, compression_level, summary, tokens
		FROM episode_summaries
		WHERE compression_level = ? AND episode_id IN (%s)
	`, strings.Join(placeholders, ","))

	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*EpisodeSummary)
	for rows.Next() {
		var summary EpisodeSummary
		if err := rows.Scan(&summary.ID, &summary.EpisodeID, &summary.CompressionLevel, &summary.Summary, &summary.Tokens); err != nil {
			continue
		}
		result[summary.EpisodeID] = &summary
	}

	return result, nil
}

// StoreEmbeddingJSON is a helper to serialize embeddings to JSON for storage
func StoreEmbeddingJSON(embedding []float64) ([]byte, error) {
	return json.Marshal(embedding)
}

// LoadEmbeddingJSON is a helper to deserialize embeddings from JSON
func LoadEmbeddingJSON(data []byte) ([]float64, error) {
	var embedding []float64
	err := json.Unmarshal(data, &embedding)
	return embedding, err
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// EngramSummary represents a compressed version of an engram
type EngramSummary struct {
	ID               int    `json:"id"`
	EngramID         string `json:"engram_id"`
	CompressionLevel int    `json:"compression_level"`
	Summary          string `json:"summary"`
	Tokens           int    `json:"tokens"`
}

// DeleteAllEngramSummaries removes all engram summaries from the database
func (g *DB) DeleteAllEngramSummaries() error {
	_, err := g.db.Exec(`DELETE FROM engram_summaries`)
	return err
}

// GetEngramSummary retrieves a summary for an engram at a specific compression level
// Falls back to higher compression levels if the requested level doesn't exist
func (g *DB) GetEngramSummary(engramID string, level int) (*EngramSummary, error) {
	for lvl := level; lvl <= CompressionLevelMax; lvl++ {
		var summary EngramSummary
		err := g.db.QueryRow(`
			SELECT id, engram_id, compression_level, summary, tokens
			FROM engram_summaries
			WHERE engram_id = ? AND compression_level = ?
		`, engramID, lvl).Scan(
			&summary.ID,
			&summary.EngramID,
			&summary.CompressionLevel,
			&summary.Summary,
			&summary.Tokens,
		)
		if err == nil {
			return &summary, nil
		}
	}
	return nil, nil
}

// GenerateEngramSummaryLevel generates a single compression level for an engram.
func (g *DB) GenerateEngramSummaryLevel(engramID string, level int, sourceEpisodes []*Episode, compressor Compressor) error {
	if compressor == nil || len(sourceEpisodes) == 0 {
		return fmt.Errorf("compressor and source episodes required")
	}

	var contextParts []string
	for _, ep := range sourceEpisodes {
		contextParts = append(contextParts, fmt.Sprintf("[%s] %s", ep.Author, ep.Content))
	}
	sourceContext := strings.Join(contextParts, "\n")
	wordCount := estimateWordCount(sourceContext)

	targetWords := level

	summary, err := compressTraceToTarget(sourceContext, compressor, targetWords, wordCount)
	if err != nil {
		return fmt.Errorf("L%d compression failed: %w", level, err)
	}

	if err := g.AddEngramSummary(engramID, level, summary, estimateTokens(summary)); err != nil {
		return fmt.Errorf("failed to store L%d summary: %w", level, err)
	}

	return nil
}

// GenerateEngramPyramid creates cascading summaries (L64→L32→L16→L8→L4) for an engram
// from its source episodes. Uses cascading approach for consistency.
func (g *DB) GenerateEngramPyramid(engramID string, sourceEpisodes []*Episode, compressor Compressor) error {
	if compressor == nil || len(sourceEpisodes) == 0 {
		return fmt.Errorf("compressor and source episodes required")
	}

	var contextParts []string
	for _, ep := range sourceEpisodes {
		contextParts = append(contextParts, fmt.Sprintf("[%s] %s", ep.Author, ep.Content))
	}
	sourceContext := strings.Join(contextParts, "\n")
	wordCount := estimateWordCount(sourceContext)

	l64Summary, err := compressTraceToTarget(sourceContext, compressor, 64, wordCount)
	if err != nil {
		return fmt.Errorf("L64 compression failed: %w", err)
	}
	if err := g.AddEngramSummary(engramID, CompressionLevel64, l64Summary, estimateTokens(l64Summary)); err != nil {
		return fmt.Errorf("failed to store L64 summary: %w", err)
	}

	l64Words := estimateWordCount(l64Summary)
	l32Summary, err := compressTraceToTarget(l64Summary, compressor, 32, l64Words)
	if err != nil {
		return fmt.Errorf("L32 compression failed: %w", err)
	}
	if err := g.AddEngramSummary(engramID, CompressionLevel32, l32Summary, estimateTokens(l32Summary)); err != nil {
		return fmt.Errorf("failed to store L32 summary: %w", err)
	}

	l32Words := estimateWordCount(l32Summary)
	l16Summary, err := compressTraceToTarget(l32Summary, compressor, 16, l32Words)
	if err != nil {
		return fmt.Errorf("L16 compression failed: %w", err)
	}
	if err := g.AddEngramSummary(engramID, CompressionLevel16, l16Summary, estimateTokens(l16Summary)); err != nil {
		return fmt.Errorf("failed to store L16 summary: %w", err)
	}

	l16Words := estimateWordCount(l16Summary)
	l8Summary, err := compressTraceToTarget(l16Summary, compressor, 8, l16Words)
	if err != nil {
		return fmt.Errorf("L8 compression failed: %w", err)
	}
	if err := g.AddEngramSummary(engramID, CompressionLevel8, l8Summary, estimateTokens(l8Summary)); err != nil {
		return fmt.Errorf("failed to store L8 summary: %w", err)
	}

	l8Words := estimateWordCount(l8Summary)
	l4Summary, err := compressTraceToTarget(l8Summary, compressor, 4, l8Words)
	if err != nil {
		return fmt.Errorf("L4 compression failed: %w", err)
	}
	if err := g.AddEngramSummary(engramID, CompressionLevel4, l4Summary, estimateTokens(l4Summary)); err != nil {
		return fmt.Errorf("failed to store L4 summary: %w", err)
	}

	return nil
}

// compressTraceToTarget compresses trace content to target word count or returns verbatim if already below target
func compressTraceToTarget(content string, compressor Compressor, targetWords int, currentWords int) (string, error) {
	// If content is already below target, use verbatim text
	if currentWords <= targetWords {
		return content, nil
	}

	// Otherwise compress to target
	prompt := buildTraceCompressionPrompt(content, targetWords)
	summary, err := compressor.Generate(prompt)
	if err != nil {
		return "", err
	}

	// Check for CJK leakage: if output has CJK but input doesn't, re-summarize with fallback
	if hasCJK(summary) && !hasCJK(content) {
		// Try fallback with Mistral (English-focused model)
		if mistralCompressor, ok := compressor.(interface{ SetGenerationModel(string) }); ok {
			mistralCompressor.SetGenerationModel("mistral")
			fallbackSummary, fallbackErr := compressor.Generate(prompt)
			if fallbackErr == nil && !hasCJK(fallbackSummary) {
				return fallbackSummary, nil
			}
			// If fallback also failed, fall through to return original summary
			// Reset model back to default
			mistralCompressor.SetGenerationModel("llama3.2")
		}
	}

	// Check for reverse case: input has CJK but output doesn't (over-correction)
	if !hasCJK(summary) && hasCJK(content) {
		fmt.Printf("WARNING: Trace compression - Input contains CJK characters but output doesn't. Possible language stripping.\n")
	}

	return summary, nil
}

// EntitySummary represents a compressed version of an entity
type EntitySummary struct {
	ID               int    `json:"id"`
	EntityID         string `json:"entity_id"`
	CompressionLevel int    `json:"compression_level"`
	Summary          string `json:"summary"`
	Tokens           int    `json:"tokens"`
}

// AddEntitySummary stores a summary for an entity at a given compression level
func (g *DB) AddEntitySummary(entityID string, level int, summary string, tokens int) error {
	_, err := g.db.Exec(`
		INSERT OR REPLACE INTO entity_summaries (entity_id, compression_level, summary, tokens)
		VALUES (?, ?, ?, ?)
	`, entityID, level, summary, tokens)
	return err
}

// GetEntitySummary retrieves a summary for an entity at a specific compression level.
// Falls back to higher compression levels if the requested level doesn't exist.
func (g *DB) GetEntitySummary(entityID string, level int) (*EntitySummary, error) {
	for lvl := level; lvl <= CompressionLevelMax; lvl++ {
		var summary EntitySummary
		err := g.db.QueryRow(`
			SELECT id, entity_id, compression_level, summary, tokens
			FROM entity_summaries
			WHERE entity_id = ? AND compression_level = ?
		`, entityID, lvl).Scan(
			&summary.ID,
			&summary.EntityID,
			&summary.CompressionLevel,
			&summary.Summary,
			&summary.Tokens,
		)
		if err == nil {
			return &summary, nil
		}
	}
	return nil, nil
}

// DeleteEntitySummaries removes all summaries for an entity
func (g *DB) DeleteEntitySummaries(entityID string) error {
	_, err := g.db.Exec(`DELETE FROM entity_summaries WHERE entity_id = ?`, entityID)
	return err
}

// GenerateEntityPyramid creates cascading summaries (L64→L32→L16→L8→L4) for an entity.
// Source content is assembled from the entity's name, type, aliases, and known relations.
func (g *DB) GenerateEntityPyramid(entityID string, compressor Compressor) error {
	if compressor == nil {
		return fmt.Errorf("compressor required")
	}

	entity, err := g.GetEntity(entityID)
	if err != nil || entity == nil {
		return fmt.Errorf("entity not found: %s", entityID)
	}

	// Assemble entity description from metadata and relations
	var parts []string
	parts = append(parts, fmt.Sprintf("%s (%s)", entity.Name, strings.ToLower(string(entity.Type))))

	aliases, _ := g.GetEntityAliases(entityID)
	if len(aliases) > 0 {
		parts = append(parts, "also known as "+strings.Join(aliases, ", "))
	}

	relations, _ := g.GetValidRelationsFor(entityID)
	for _, r := range relations {
		otherID := r.ToID
		if otherID == entityID {
			otherID = r.FromID
		}
		other, err := g.GetEntity(otherID)
		if err != nil || other == nil {
			continue
		}
		relType := strings.ToLower(strings.ReplaceAll(string(r.RelationType), "_", " "))
		parts = append(parts, fmt.Sprintf("%s %s", relType, other.Name))
	}

	sourceContent := strings.Join(parts, "; ")
	wordCount := estimateWordCount(sourceContent)

	// Store L0 (verbatim assembled description)
	if err := g.AddEntitySummary(entityID, CompressionLevelVerbatim, sourceContent, estimateTokens(sourceContent)); err != nil {
		return fmt.Errorf("failed to store L0 entity summary: %w", err)
	}

	// Generate cascading pyramid L64→L32→L16→L8→L4
	l64Summary, err := compressTraceToTarget(sourceContent, compressor, 64, wordCount)
	if err != nil {
		return fmt.Errorf("L64 entity compression failed: %w", err)
	}
	g.AddEntitySummary(entityID, CompressionLevel64, l64Summary, estimateTokens(l64Summary))

	l64Words := estimateWordCount(l64Summary)
	l32Summary, err := compressTraceToTarget(l64Summary, compressor, 32, l64Words)
	if err != nil {
		return fmt.Errorf("L32 entity compression failed: %w", err)
	}
	g.AddEntitySummary(entityID, CompressionLevel32, l32Summary, estimateTokens(l32Summary))

	l32Words := estimateWordCount(l32Summary)
	l16Summary, err := compressTraceToTarget(l32Summary, compressor, 16, l32Words)
	if err != nil {
		return fmt.Errorf("L16 entity compression failed: %w", err)
	}
	g.AddEntitySummary(entityID, CompressionLevel16, l16Summary, estimateTokens(l16Summary))

	l16Words := estimateWordCount(l16Summary)
	l8Summary, err := compressTraceToTarget(l16Summary, compressor, 8, l16Words)
	if err != nil {
		return fmt.Errorf("L8 entity compression failed: %w", err)
	}
	g.AddEntitySummary(entityID, CompressionLevel8, l8Summary, estimateTokens(l8Summary))

	l8Words := estimateWordCount(l8Summary)
	l4Summary, err := compressTraceToTarget(l8Summary, compressor, 4, l8Words)
	if err != nil {
		return fmt.Errorf("L4 entity compression failed: %w", err)
	}
	g.AddEntitySummary(entityID, CompressionLevel4, l4Summary, estimateTokens(l4Summary))

	return nil
}

// buildTraceCompressionPrompt constructs a prompt for trace compression to target word count
func buildTraceCompressionPrompt(content string, targetWords int) string {
	prompt := fmt.Sprintf(`Compress this conversation into a memory trace summary of %d words or less.

Rules:
- Maximum %d words
- Keep the core meaning
- Remove filler, small talk, redundancy
- Preserve key facts and decisions
- Write in past tense (e.g., "User reported..." not "User reports...")
- CRITICAL: You MUST write ONLY in English - NO Chinese characters allowed
- If you write ANY Chinese characters (像这样的字符), the output will be REJECTED
- Use ONLY English words from A-Z - absolutely NO non-English characters

Source conversation:
%s

Compressed summary (ONLY English):`, targetWords, targetWords, content)
	return prompt
}
