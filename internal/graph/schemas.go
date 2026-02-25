package graph

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
