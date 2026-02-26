package graph

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AddSchema inserts or updates a schema in the database.
func (g *DB) AddSchema(s *Schema) error {
	if s.ID == "" {
		return fmt.Errorf("schema ID is required")
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now()
	}

	embeddingBytes, _ := json.Marshal(s.Embedding)

	_, err := g.db.Exec(`
		INSERT INTO schemas (id, name, content, embedding, is_labile, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			content = excluded.content,
			embedding = excluded.embedding,
			is_labile = excluded.is_labile,
			updated_at = excluded.updated_at
	`, s.ID, s.Name, s.Content, embeddingBytes, boolToInt(s.IsLabile), s.CreatedAt, s.UpdatedAt)
	return err
}

// GetSchema retrieves a schema by ID, optionally populating instances.
func (g *DB) GetSchema(id string) (*Schema, error) {
	row := g.db.QueryRow(`
		SELECT id, name, content, embedding, is_labile, created_at, updated_at
		FROM schemas WHERE id = ?
	`, id)

	s, err := scanSchema(row)
	if err != nil || s == nil {
		return s, err
	}

	// Populate instances
	instances, err := g.GetSchemaInstances(id)
	if err == nil {
		s.Instances = instances
	}
	return s, nil
}

// ListSchemas returns all schemas ordered by updated_at descending.
func (g *DB) ListSchemas() ([]*Schema, error) {
	rows, err := g.db.Query(`
		SELECT id, name, content, embedding, is_labile, created_at, updated_at
		FROM schemas
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list schemas: %w", err)
	}
	defer rows.Close()
	return scanSchemaRows(rows)
}

// UpdateSchemaContent updates the content and marks it non-labile.
func (g *DB) UpdateSchemaContent(id, content string, embedding []float64) error {
	embeddingBytes, _ := json.Marshal(embedding)
	_, err := g.db.Exec(`
		UPDATE schemas SET content = ?, embedding = ?, is_labile = 0, updated_at = ?
		WHERE id = ?
	`, content, embeddingBytes, time.Now(), id)
	return err
}

// MakeSchemaLabile marks a schema as needing reconsolidation.
func (g *DB) MakeSchemaLabile(id string) error {
	_, err := g.db.Exec(`UPDATE schemas SET is_labile = 1, updated_at = ? WHERE id = ?`, time.Now(), id)
	return err
}

// DeleteSchema removes a schema and its instance records.
func (g *DB) DeleteSchema(id string) error {
	_, err := g.db.Exec(`DELETE FROM schemas WHERE id = ?`, id)
	return err
}

// AddSchemaInstance records that an engram matches a schema.
func (g *DB) AddSchemaInstance(inst *SchemaInstance) error {
	slotBytes, _ := json.Marshal(inst.SlotValues)
	_, err := g.db.Exec(`
		INSERT INTO schema_instances (schema_id, engram_id, slot_values, is_anomaly, matched_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(schema_id, engram_id) DO UPDATE SET
			slot_values = excluded.slot_values,
			is_anomaly = excluded.is_anomaly,
			matched_at = excluded.matched_at
	`, inst.SchemaID, inst.EngramID, string(slotBytes), boolToInt(inst.IsAnomaly), inst.MatchedAt)
	if err != nil {
		return err
	}

	// Denormalized annotation for fast engram→schema lookup
	_, err = g.db.Exec(`
		INSERT OR IGNORE INTO schema_annotations (engram_id, schema_id)
		VALUES (?, ?)
	`, inst.EngramID, inst.SchemaID)
	return err
}

// GetSchemaInstances returns all instances for a schema.
func (g *DB) GetSchemaInstances(schemaID string) ([]SchemaInstance, error) {
	rows, err := g.db.Query(`
		SELECT schema_id, engram_id, slot_values, is_anomaly, matched_at
		FROM schema_instances WHERE schema_id = ?
		ORDER BY matched_at DESC
	`, schemaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []SchemaInstance
	for rows.Next() {
		var inst SchemaInstance
		var slotBytes sql.NullString
		var isAnomaly int

		if err := rows.Scan(&inst.SchemaID, &inst.EngramID, &slotBytes, &isAnomaly, &inst.MatchedAt); err != nil {
			continue
		}
		inst.IsAnomaly = isAnomaly != 0
		if slotBytes.Valid && slotBytes.String != "" && slotBytes.String != "null" {
			json.Unmarshal([]byte(slotBytes.String), &inst.SlotValues)
		}
		instances = append(instances, inst)
	}
	return instances, rows.Err()
}

// GetSchemaIDsForEngram returns all schema IDs that an engram is annotated with.
func (g *DB) GetSchemaIDsForEngram(engramID string) ([]string, error) {
	rows, err := g.db.Query(`
		SELECT schema_id FROM schema_annotations WHERE engram_id = ?
	`, engramID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// FindSimilarSchemas returns schemas whose embedding cosine similarity to the given
// embedding exceeds the threshold. Falls back to full scan (no vec0 for schemas).
func (g *DB) FindSimilarSchemas(embedding []float64, threshold float64, limit int) ([]*Schema, error) {
	rows, err := g.db.Query(`
		SELECT id, name, content, embedding, is_labile, created_at, updated_at
		FROM schemas WHERE embedding IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type candidate struct {
		schema *Schema
		sim    float64
	}
	var candidates []candidate

	for rows.Next() {
		var s Schema
		var embBytes []byte
		var isLabile int
		if err := rows.Scan(&s.ID, &s.Name, &s.Content, &embBytes, &isLabile, &s.CreatedAt, &s.UpdatedAt); err != nil {
			continue
		}
		s.IsLabile = isLabile != 0
		if len(embBytes) > 0 {
			json.Unmarshal(embBytes, &s.Embedding)
		}
		sim := cosineSim(embedding, s.Embedding)
		if sim >= threshold {
			candidates = append(candidates, candidate{&s, sim})
		}
	}

	// Sort by similarity descending
	for i := 1; i < len(candidates); i++ {
		for j := 0; j < len(candidates)-i; j++ {
			if candidates[j].sim < candidates[j+1].sim {
				candidates[j], candidates[j+1] = candidates[j+1], candidates[j]
			}
		}
	}

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}

	schemas := make([]*Schema, len(candidates))
	for i, c := range candidates {
		schemas[i] = c.schema
	}
	return schemas, rows.Err()
}

// GetEngrams returns all L2+ engrams grouped by cluster — used for schema induction.
// Returns engrams with depth >= minDepth, ordered by depth then event_time.
func (g *DB) GetEngramsAtMinDepth(minDepth int, limit int) ([]*Engram, error) {
	rows, err := g.db.Query(`
		SELECT e.id,
			COALESCE(
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 0 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 64 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 32 LIMIT 1),
				e.summary, ''
			) as summary,
			e.topic, e.engram_type,
			e.activation, e.strength, e.embedding, e.event_time, e.created_at, e.last_accessed, e.labile_until,
			COALESCE(e.depth, 0)
		FROM engrams e
		WHERE COALESCE(e.depth, 0) >= ?
		ORDER BY COALESCE(e.depth, 0) ASC, e.event_time ASC
		LIMIT ?
	`, minDepth, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEngramRows(rows)
}

// CountSchemas returns the total number of schemas.
func (g *DB) CountSchemas() (int, error) {
	var count int
	err := g.db.QueryRow(`SELECT COUNT(*) FROM schemas`).Scan(&count)
	return count, err
}

// CountSchemaInstances returns the total number of schema instances.
func (g *DB) CountSchemaInstances() (int, error) {
	var count int
	err := g.db.QueryRow(`SELECT COUNT(*) FROM schema_instances`).Scan(&count)
	return count, err
}

// MarkSchemaAnomalies flags instances as anomalous by schema_id + engram_id pairs.
func (g *DB) MarkSchemaAnomaly(schemaID, engramID string) error {
	_, err := g.db.Exec(`
		UPDATE schema_instances SET is_anomaly = 1 WHERE schema_id = ? AND engram_id = ?
	`, schemaID, engramID)
	return err
}

// GetSchemaIDsForEngrams returns a map of engram_id → []schema_id for a set of engram IDs.
// Single query, avoids N+1.
func (g *DB) GetSchemaIDsForEngrams(ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return make(map[string][]string), nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`
		SELECT engram_id, schema_id FROM schema_annotations WHERE engram_id IN (%s)
	`, strings.Join(placeholders, ","))
	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]string)
	for rows.Next() {
		var engramID, schemaID string
		if rows.Scan(&engramID, &schemaID) == nil {
			result[engramID] = append(result[engramID], schemaID)
		}
	}
	return result, rows.Err()
}

// GetSchemasByIDs returns schemas for a list of IDs. Order is not guaranteed.
func (g *DB) GetSchemasByIDs(ids []string) ([]*Schema, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`
		SELECT id, name, content, embedding, is_labile, created_at, updated_at
		FROM schemas WHERE id IN (%s)
	`, strings.Join(placeholders, ","))
	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSchemaRows(rows)
}

// FormatSchemaSummary builds a compact summary from schema content at a given word limit.
// Parses the GENERALIZATIONS section; falls back to name only if absent.
func FormatSchemaSummary(name, content string, maxWords int) string {
	var bullets []string
	inSection := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "GENERALIZATIONS") {
			inSection = true
			continue
		}
		if inSection {
			// Stop at next section header (uppercase word(s))
			if len(trimmed) > 0 && trimmed == strings.ToUpper(trimmed) && len(strings.Fields(trimmed)) <= 4 {
				break
			}
			// Accept both "- bullet" and "1. bullet" list styles
			if strings.HasPrefix(trimmed, "- ") {
				bullets = append(bullets, strings.TrimPrefix(trimmed, "- "))
			} else if len(trimmed) > 2 && trimmed[0] >= '1' && trimmed[0] <= '9' && trimmed[1] == '.' {
				// Numbered item: strip "N. " prefix
				rest := strings.TrimSpace(trimmed[2:])
				if rest != "" {
					bullets = append(bullets, rest)
				}
			}
		}
	}
	if len(bullets) == 0 {
		return name
	}
	// Build "Name: bullet1; bullet2" until word limit
	prefix := name + ": "
	wordCount := estimateWordCount(prefix)
	var sb strings.Builder
	sb.WriteString(prefix)
	for i, b := range bullets {
		bWords := estimateWordCount(b)
		if wordCount+bWords > maxWords && i > 0 {
			break
		}
		if i > 0 {
			sb.WriteString("; ")
			wordCount++
		}
		sb.WriteString(b)
		wordCount += bWords
	}
	return sb.String()
}

// AddSchemaSummary stores a precomputed summary for a schema at a compression level.
func (g *DB) AddSchemaSummary(schemaID string, level int, summary string, tokens int) error {
	_, err := g.db.Exec(`
		INSERT INTO schema_summaries (schema_id, compression_level, summary, tokens)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(schema_id, compression_level) DO UPDATE SET
			summary = excluded.summary,
			tokens = excluded.tokens
	`, schemaID, level, summary, tokens)
	return err
}

// GetSchemaSummary retrieves a precomputed schema summary at the given level,
// falling back to higher compression levels. Returns "" if none found.
func (g *DB) GetSchemaSummary(schemaID string, level int) (string, error) {
	for lvl := level; lvl <= CompressionLevelMax; lvl++ {
		var summary string
		err := g.db.QueryRow(`
			SELECT summary FROM schema_summaries WHERE schema_id = ? AND compression_level = ?
		`, schemaID, lvl).Scan(&summary)
		if err == nil {
			return summary, nil
		}
	}
	return "", nil
}

// GetSchemaSummariesBatch retrieves summaries for multiple schemas at the nearest available
// level >= target. Returns a map of schema_id → summary string.
func (g *DB) GetSchemaSummariesBatch(ids []string, level int) (map[string]string, error) {
	if len(ids) == 0 {
		return make(map[string]string), nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 1+len(ids))
	args[0] = level
	for i, id := range ids {
		placeholders[i] = "?"
		args[i+1] = id
	}
	query := fmt.Sprintf(`
		SELECT schema_id, compression_level, summary
		FROM schema_summaries
		WHERE compression_level >= ? AND schema_id IN (%s)
		ORDER BY schema_id, compression_level ASC
	`, strings.Join(placeholders, ","))
	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var schemaID string
		var lvl int
		var summary string
		if err := rows.Scan(&schemaID, &lvl, &summary); err != nil {
			continue
		}
		// Keep only the first (lowest available level) per schema.
		if _, exists := result[schemaID]; !exists {
			result[schemaID] = summary
		}
	}
	return result, rows.Err()
}

// GenerateSchemaSummaries precomputes summaries at all compression levels for a schema.
// Called at induction time (after AddSchema) and for backfilling existing schemas.
// No LLM needed — summaries are extracted from the GENERALIZATIONS section.
func (g *DB) GenerateSchemaSummaries(schemaID, name, content string) error {
	levels := []int{CompressionLevel4, CompressionLevel8, CompressionLevel16, CompressionLevel32, CompressionLevel64}
	for _, lvl := range levels {
		summary := FormatSchemaSummary(name, content, lvl)
		tokens := estimateTokens(summary)
		if err := g.AddSchemaSummary(schemaID, lvl, summary, tokens); err != nil {
			return fmt.Errorf("schema summary level %d: %w", lvl, err)
		}
	}
	return nil
}

// BackfillSchemaSummaries (re)generates summaries for all schemas.
// Safe to call at startup or via API — no LLM needed, pure text extraction.
// Uses upsert so existing summaries are overwritten with corrected versions.
// Returns the count of schemas processed.
func (g *DB) BackfillSchemaSummaries() (int, error) {
	rows, err := g.db.Query(`SELECT id, name, content FROM schemas`)
	if err != nil {
		return 0, fmt.Errorf("querying schemas without summaries: %w", err)
	}
	defer rows.Close()

	type schemaRow struct{ id, name, content string }
	var pending []schemaRow
	for rows.Next() {
		var r schemaRow
		if err := rows.Scan(&r.id, &r.name, &r.content); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, r := range pending {
		if err := g.GenerateSchemaSummaries(r.id, r.name, r.content); err != nil {
			return 0, fmt.Errorf("generating summaries for schema %s: %w", r.id[:8], err)
		}
	}
	return len(pending), nil
}

// --- scan helpers ---

func scanSchema(row *sql.Row) (*Schema, error) {
	var s Schema
	var embBytes []byte
	var isLabile int

	err := row.Scan(&s.ID, &s.Name, &s.Content, &embBytes, &isLabile, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	s.IsLabile = isLabile != 0
	if len(embBytes) > 0 {
		json.Unmarshal(embBytes, &s.Embedding)
	}
	return &s, nil
}

func scanSchemaRows(rows *sql.Rows) ([]*Schema, error) {
	var schemas []*Schema
	for rows.Next() {
		var s Schema
		var embBytes []byte
		var isLabile int

		if err := rows.Scan(&s.ID, &s.Name, &s.Content, &embBytes, &isLabile, &s.CreatedAt, &s.UpdatedAt); err != nil {
			continue
		}
		s.IsLabile = isLabile != 0
		if len(embBytes) > 0 {
			json.Unmarshal(embBytes, &s.Embedding)
		}
		schemas = append(schemas, &s)
	}
	return schemas, rows.Err()
}

// boolToInt converts bool to SQLite integer (0/1).
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
