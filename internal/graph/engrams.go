package graph

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// AddEngram adds a new engram to the graph
func (g *DB) AddEngram(en *Engram) error {
	if en.ID == "" {
		return fmt.Errorf("engram ID is required")
	}

	embeddingBytes, err := json.Marshal(en.Embedding)
	if err != nil {
		embeddingBytes = nil
	}

	if en.CreatedAt.IsZero() {
		en.CreatedAt = time.Now()
	}
	if en.LastAccessed.IsZero() {
		en.LastAccessed = time.Now()
	}

	engramType := en.EngramType
	if engramType == "" {
		engramType = EngramTypeKnowledge
	}

	_, err = g.db.Exec(`
		INSERT INTO engrams (id, topic, engram_type, activation, strength,
			embedding, event_time, created_at, last_accessed, labile_until)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			engram_type = excluded.engram_type,
			activation = excluded.activation,
			strength = excluded.strength,
			embedding = excluded.embedding,
			event_time = excluded.event_time,
			last_accessed = excluded.last_accessed,
			labile_until = excluded.labile_until
	`,
		en.ID, en.Topic, string(engramType), en.Activation, en.Strength,
		embeddingBytes, en.EventTime, en.CreatedAt, en.LastAccessed, nullableTime(en.LabileUntil),
	)

	if err != nil {
		return fmt.Errorf("failed to insert engram: %w", err)
	}

	// Store verbatim summary in engram_summaries as level 0 (verbatim) if provided.
	if en.Summary != "" {
		tokens := len(en.Summary) / 4 // rough token estimate
		if err := g.AddEngramSummary(en.ID, CompressionLevelVerbatim, en.Summary, tokens); err != nil {
			_ = err
		}
	}

	// Sync to vec index if available.
	if g.vecAvailable && len(en.Embedding) > 0 {
		_ = g.ensureVecTable(len(en.Embedding))
		if g.vecDim == len(en.Embedding) {
			g.syncEngramToVec(en.ID, en.Embedding)
		}
	}

	return nil
}

// GetEngram retrieves an engram by ID
func (g *DB) GetEngram(id string) (*Engram, error) {
	row := g.db.QueryRow(`
		SELECT e.id,
			COALESCE(
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 0 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 64 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 32 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 16 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 8 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 4 LIMIT 1),
				e.summary,
				''
			) as summary,
			e.topic, e.engram_type,
			e.activation, e.strength, e.embedding, e.event_time, e.created_at, e.last_accessed, e.labile_until
		FROM engrams e
		WHERE e.id = ?
	`, id)

	return scanEngram(row)
}

// GetActivatedEngrams retrieves engrams with activation above threshold
func (g *DB) GetActivatedEngrams(threshold float64, limit int) ([]*Engram, error) {
	rows, err := g.db.Query(`
		SELECT e.id,
			COALESCE(
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 64 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 32 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 16 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 8 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 4 LIMIT 1),
				e.summary,
				''
			) as summary,
			e.topic, e.engram_type,
			e.activation, e.strength, e.embedding, e.event_time, e.created_at, e.last_accessed, e.labile_until
		FROM engrams e
		WHERE e.activation >= ?
		ORDER BY e.activation DESC
		LIMIT ?
	`, threshold, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query activated engrams: %w", err)
	}
	defer rows.Close()

	return scanEngramRows(rows)
}

// GetEngramsBatch retrieves multiple engrams by ID in a single query.
func (g *DB) GetEngramsBatch(ids []string) (map[string]*Engram, error) {
	if len(ids) == 0 {
		return make(map[string]*Engram), nil
	}

	placeholders := make([]byte, 0, len(ids)*2-1)
	for i := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
	}

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := g.db.Query(`
		SELECT e.id,
			COALESCE(
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 0 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 64 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 32 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 16 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 8 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 4 LIMIT 1),
				e.summary,
				''
			) as summary,
			e.topic, e.engram_type,
			e.activation, e.strength, e.embedding, e.event_time, e.created_at, e.last_accessed, e.labile_until
		FROM engrams e
		WHERE e.id IN (`+string(placeholders)+`)
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to batch query engrams: %w", err)
	}
	defer rows.Close()

	engrams, err := scanEngramRows(rows)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*Engram, len(engrams))
	for _, en := range engrams {
		result[en.ID] = en
	}
	return result, nil
}

// GetEngramsBatchAtLevel retrieves multiple engrams by ID, loading only the specified compression level summary.
func (g *DB) GetEngramsBatchAtLevel(ids []string, level int) (map[string]*Engram, error) {
	if len(ids) == 0 {
		return make(map[string]*Engram), nil
	}

	placeholders := make([]byte, 0, len(ids)*2-1)
	for i := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
	}

	args := make([]any, 1+len(ids))
	args[0] = level
	for i, id := range ids {
		args[1+i] = id
	}

	rows, err := g.db.Query(`
		SELECT e.id,
			COALESCE(
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = ? LIMIT 1),
				e.summary,
				''
			) as summary,
			e.topic, e.engram_type,
			e.activation, e.strength, e.embedding, e.event_time, e.created_at, e.last_accessed, e.labile_until
		FROM engrams e
		WHERE e.id IN (`+string(placeholders)+`)
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to batch query engrams at level %d: %w", level, err)
	}
	defer rows.Close()

	engrams, err := scanEngramRows(rows)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*Engram, len(engrams))
	for _, en := range engrams {
		result[en.ID] = en
	}
	return result, nil
}

// GetActivatedEngramsWithLevel retrieves engrams with activation above threshold,
// loading only the specified compression level summary.
func (g *DB) GetActivatedEngramsWithLevel(threshold float64, limit, level int) ([]*Engram, error) {
	rows, err := g.db.Query(`
		SELECT e.id,
			COALESCE(
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = ? LIMIT 1),
				e.summary,
				''
			) as summary,
			e.topic, e.engram_type,
			e.activation, e.strength, e.embedding, e.event_time, e.created_at, e.last_accessed, e.labile_until
		FROM engrams e
		WHERE e.activation >= ?
		ORDER BY e.activation DESC
		LIMIT ?
	`, level, threshold, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query activated engrams at level %d: %w", level, err)
	}
	defer rows.Close()

	return scanEngramRows(rows)
}

// UpdateEngramActivation updates the activation level of an engram
func (g *DB) UpdateEngramActivation(id string, activation float64) error {
	_, err := g.db.Exec(`
		UPDATE engrams SET activation = ?, last_accessed = ? WHERE id = ?
	`, activation, time.Now(), id)
	return err
}

// ReinforceEngram increments strength and updates embedding
func (g *DB) ReinforceEngram(id string, newEmbedding []float64, alpha float64) error {
	engram, err := g.GetEngram(id)
	if err != nil {
		return err
	}
	if engram == nil {
		return fmt.Errorf("engram not found: %s", id)
	}

	if len(engram.Embedding) > 0 && len(newEmbedding) > 0 {
		for i := range engram.Embedding {
			if i < len(newEmbedding) {
				engram.Embedding[i] = alpha*newEmbedding[i] + (1-alpha)*engram.Embedding[i]
			}
		}
	} else if len(newEmbedding) > 0 {
		engram.Embedding = newEmbedding
	}

	embeddingBytes, _ := json.Marshal(engram.Embedding)

	_, err = g.db.Exec(`
		UPDATE engrams SET
			strength = strength + 1,
			embedding = ?,
			last_accessed = ?
		WHERE id = ?
	`, embeddingBytes, time.Now(), id)
	if err != nil {
		return err
	}

	if g.vecAvailable && len(engram.Embedding) > 0 && g.vecDim == len(engram.Embedding) {
		g.syncEngramToVec(id, engram.Embedding)
	}

	return nil
}

// DecayActivation decays all engram activations by the given factor
func (g *DB) DecayActivation(factor float64) error {
	_, err := g.db.Exec(`
		UPDATE engrams SET activation = activation * ?
	`, factor)
	return err
}

// DecayActivationByAge applies time-based decay to all engrams based on time since last access.
func (g *DB) DecayActivationByAge(lambda float64, floor float64) (int, error) {
	now := time.Now()

	rows, err := g.db.Query(`
		SELECT id, activation, last_accessed, COALESCE(engram_type, 'knowledge')
		FROM engrams WHERE activation > ?
	`, floor)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type update struct {
		id            string
		newActivation float64
	}
	var updates []update

	for rows.Next() {
		var id string
		var activation float64
		var lastAccessed time.Time
		var engramType string
		if err := rows.Scan(&id, &activation, &lastAccessed, &engramType); err != nil {
			continue
		}

		hoursSinceAccess := now.Sub(lastAccessed).Hours()
		if hoursSinceAccess < 0 {
			hoursSinceAccess = 0
		}

		effectiveLambda := lambda
		if engramType == string(EngramTypeOperational) {
			effectiveLambda = lambda * 3
		}

		decayFactor := math.Exp(-effectiveLambda * hoursSinceAccess)
		newActivation := activation * decayFactor
		if newActivation < floor {
			newActivation = floor
		}

		if newActivation != activation {
			updates = append(updates, update{id: id, newActivation: newActivation})
		}
	}

	for _, u := range updates {
		g.db.Exec(`UPDATE engrams SET activation = ? WHERE id = ?`, u.newActivation, u.id)
	}

	return len(updates), nil
}

// BoostEngramAccess updates last_accessed and boosts activation for engrams that were retrieved.
func (g *DB) BoostEngramAccess(engramIDs []string, boost float64) error {
	now := time.Now()
	for _, id := range engramIDs {
		_, err := g.db.Exec(`
			UPDATE engrams SET
				last_accessed = ?,
				activation = MIN(1.0, activation + ?)
			WHERE id = ?
		`, now, boost, id)
		if err != nil {
			continue
		}
	}
	return nil
}

// LinkEngramToSource links an engram to a source episode
func (g *DB) LinkEngramToSource(engramID, episodeID string) error {
	_, err := g.db.Exec(`
		INSERT OR IGNORE INTO engram_episodes (engram_id, episode_id)
		VALUES (?, ?)
	`, engramID, episodeID)
	return err
}

// LinkEngramToEntity links an engram to an entity
func (g *DB) LinkEngramToEntity(engramID, entityID string) error {
	_, err := g.db.Exec(`
		INSERT OR IGNORE INTO engram_entities (engram_id, entity_id)
		VALUES (?, ?)
	`, engramID, entityID)
	return err
}

// AddEngramRelation adds a relationship between two engrams
func (g *DB) AddEngramRelation(fromID, toID string, relType EdgeType, weight float64) error {
	_, err := g.db.Exec(`
		INSERT INTO engram_relations (from_id, to_id, relation_type, weight)
		VALUES (?, ?, ?, ?)
	`, fromID, toID, relType, weight)
	return err
}

// GetEngramNeighbors returns neighbors of an engram for spreading activation.
func (g *DB) GetEngramNeighbors(id string) ([]Neighbor, error) {
	direct, err := g.getDirectEngramNeighbors(id)
	if err != nil {
		return nil, err
	}

	bridged, err := g.GetEngramNeighborsThroughEntities(id, MaxEdgesPerNode)
	if err != nil {
		bridged = nil
	}

	seen := make(map[string]bool)
	var merged []Neighbor
	for _, n := range direct {
		seen[n.ID] = true
		merged = append(merged, n)
	}
	for _, n := range bridged {
		if !seen[n.ID] {
			seen[n.ID] = true
			merged = append(merged, n)
		}
	}

	if len(merged) > MaxEdgesPerNode {
		sort.Slice(merged, func(i, j int) bool {
			return merged[i].Weight > merged[j].Weight
		})
		merged = merged[:MaxEdgesPerNode]
	}

	return merged, nil
}

func (g *DB) getDirectEngramNeighbors(id string) ([]Neighbor, error) {
	rows, err := g.db.Query(`
		SELECT to_id, weight, relation_type FROM engram_relations WHERE from_id = ?
		UNION ALL
		SELECT from_id, weight, relation_type FROM engram_relations WHERE to_id = ?
	`, id, id)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var neighbors []Neighbor
	for rows.Next() {
		var n Neighbor
		var relType string
		if err := rows.Scan(&n.ID, &n.Weight, &relType); err != nil {
			continue
		}
		n.Type = EdgeType(relType)
		neighbors = append(neighbors, n)
	}

	return neighbors, nil
}

// GetEngramNeighborsThroughEntities finds engrams that share entities with the given engram.
func (g *DB) GetEngramNeighborsThroughEntities(engramID string, maxNeighbors int) ([]Neighbor, error) {
	if maxNeighbors <= 0 {
		maxNeighbors = MaxEdgesPerNode
	}

	rows, err := g.db.Query(`
		SELECT ee2.engram_id, COUNT(DISTINCT ee1.entity_id) as shared, AVG(e.salience) as sal
		FROM engram_entities ee1
		JOIN engram_entities ee2 ON ee1.entity_id = ee2.entity_id
		JOIN entities e ON e.id = ee1.entity_id
		WHERE ee1.engram_id = ? AND ee2.engram_id != ?
		GROUP BY ee2.engram_id
		ORDER BY shared DESC, sal DESC
		LIMIT ?
	`, engramID, engramID, maxNeighbors)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var neighbors []Neighbor
	for rows.Next() {
		var neighborID string
		var sharedCount int
		var salience float64
		if err := rows.Scan(&neighborID, &sharedCount, &salience); err != nil {
			continue
		}

		weight := float64(sharedCount) * 0.3
		if weight > 1.0 {
			weight = 1.0
		}

		neighbors = append(neighbors, Neighbor{
			ID:     neighborID,
			Weight: weight,
			Type:   EdgeSharedEntity,
		})
	}

	return neighbors, nil
}

// GetEngramNeighborsBatch returns neighbors for a set of engram IDs using 2 SQL queries.
func (g *DB) GetEngramNeighborsBatch(ids []string) (map[string][]Neighbor, error) {
	if len(ids) == 0 {
		return make(map[string][]Neighbor), nil
	}

	result := make(map[string][]Neighbor, len(ids))
	for _, id := range ids {
		result[id] = nil
	}

	ph := strings.Repeat("?,", len(ids))
	ph = ph[:len(ph)-1]

	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	directSQL := fmt.Sprintf(`
		SELECT from_id, to_id, weight, relation_type FROM engram_relations WHERE from_id IN (%s)
		UNION ALL
		SELECT to_id, from_id, weight, relation_type FROM engram_relations WHERE to_id IN (%s)
	`, ph, ph)

	directArgs := make([]interface{}, len(args)*2)
	copy(directArgs, args)
	copy(directArgs[len(args):], args)

	rows, err := g.db.Query(directSQL, directArgs...)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]map[string]bool)
	for rows.Next() {
		var sourceID, neighborID string
		var weight float64
		var relType string
		if err := rows.Scan(&sourceID, &neighborID, &weight, &relType); err != nil {
			continue
		}
		if _, ok := result[sourceID]; !ok {
			continue
		}
		if seen[sourceID] == nil {
			seen[sourceID] = make(map[string]bool)
		}
		if seen[sourceID][neighborID] {
			continue
		}
		seen[sourceID][neighborID] = true
		result[sourceID] = append(result[sourceID], Neighbor{
			ID:     neighborID,
			Weight: weight,
			Type:   EdgeType(relType),
		})
	}
	rows.Close()

	bridgedSQL := fmt.Sprintf(`
		SELECT ee1.engram_id, ee2.engram_id, COUNT(DISTINCT ee1.entity_id) as shared, AVG(e.salience) as sal
		FROM engram_entities ee1
		JOIN engram_entities ee2 ON ee1.entity_id = ee2.entity_id
		JOIN entities e ON e.id = ee1.entity_id
		WHERE ee1.engram_id IN (%s) AND ee2.engram_id != ee1.engram_id
		GROUP BY ee1.engram_id, ee2.engram_id
		ORDER BY ee1.engram_id, shared DESC, sal DESC
	`, ph)

	bridgedRows, err := g.db.Query(bridgedSQL, args...)
	if err == nil {
		perSourceCount := make(map[string]int)
		for bridgedRows.Next() {
			var sourceID, neighborID string
			var sharedCount int
			var salience float64
			if err := bridgedRows.Scan(&sourceID, &neighborID, &sharedCount, &salience); err != nil {
				continue
			}
			if _, ok := result[sourceID]; !ok {
				continue
			}
			if seen[sourceID] != nil && seen[sourceID][neighborID] {
				continue
			}
			if perSourceCount[sourceID] >= MaxEdgesPerNode {
				continue
			}
			weight := math.Min(1.0, float64(sharedCount)*0.3)
			result[sourceID] = append(result[sourceID], Neighbor{
				ID:     neighborID,
				Weight: weight,
				Type:   EdgeSharedEntity,
			})
			perSourceCount[sourceID]++
		}
		bridgedRows.Close()
	}

	for id, neighbors := range result {
		if len(neighbors) > MaxEdgesPerNode {
			sort.Slice(neighbors, func(i, j int) bool {
				return neighbors[i].Weight > neighbors[j].Weight
			})
			result[id] = neighbors[:MaxEdgesPerNode]
		}
	}

	return result, nil
}

// GetEngramEntities returns the entity IDs linked to an engram
func (g *DB) GetEngramEntities(engramID string) ([]string, error) {
	rows, err := g.db.Query(`
		SELECT entity_id FROM engram_entities WHERE engram_id = ?
	`, engramID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetEpisodeEngrams returns the engram IDs that contain the given episode
func (g *DB) GetEpisodeEngrams(episodeID string) ([]string, error) {
	rows, err := g.db.Query(`
		SELECT engram_id FROM engram_episodes WHERE episode_id = ?
	`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var engramIDs []string
	for rows.Next() {
		var engramID string
		if err := rows.Scan(&engramID); err != nil {
			continue
		}
		engramIDs = append(engramIDs, engramID)
	}
	return engramIDs, nil
}

// GetEngramSources returns the source episode IDs for an engram
func (g *DB) GetEngramSources(engramID string) ([]string, error) {
	rows, err := g.db.Query(`
		SELECT episode_id FROM engram_episodes WHERE engram_id = ?
	`, engramID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetAllEngrams retrieves all engrams
func (g *DB) GetAllEngrams() ([]*Engram, error) {
	rows, err := g.db.Query(`
		SELECT e.id,
			COALESCE(
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 0 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 64 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 32 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 16 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 8 LIMIT 1),
				(SELECT summary FROM engram_summaries WHERE engram_id = e.id AND compression_level = 4 LIMIT 1),
				e.summary,
				''
			) as summary,
			e.topic, e.engram_type,
			e.activation, e.strength, e.embedding, e.event_time, e.created_at, e.last_accessed, e.labile_until
		FROM engrams e
		ORDER BY e.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query engrams: %w", err)
	}
	defer rows.Close()

	return scanEngramRows(rows)
}

// DeleteEngram deletes an engram by ID
func (g *DB) DeleteEngram(id string) error {
	g.db.Exec(`DELETE FROM engram_relations WHERE from_id = ? OR to_id = ?`, id, id)
	g.db.Exec(`DELETE FROM engram_entities WHERE engram_id = ?`, id)
	g.db.Exec(`DELETE FROM engram_episodes WHERE engram_id = ?`, id)
	if g.vecAvailable {
		g.db.Exec(`DELETE FROM engram_vec WHERE rowid = (SELECT rowid FROM engrams WHERE id = ?)`, id)
	}

	result, err := g.db.Exec(`DELETE FROM engrams WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete engram: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("engram not found: %s", id)
	}
	return nil
}

// CountEngrams returns the count of engrams
func (g *DB) CountEngrams() (total int, err error) {
	err = g.db.QueryRow(`SELECT COUNT(*) FROM engrams`).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// scanEngram scans a single row into an Engram
func scanEngram(row *sql.Row) (*Engram, error) {
	var en Engram
	var embeddingBytes []byte
	var summary sql.NullString
	var topic sql.NullString
	var engramType sql.NullString
	var eventTime sql.NullTime
	var labileUntil sql.NullTime

	err := row.Scan(
		&en.ID, &summary, &topic, &engramType, &en.Activation, &en.Strength,
		&embeddingBytes, &eventTime, &en.CreatedAt, &en.LastAccessed, &labileUntil,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	en.Summary = summary.String
	en.Topic = topic.String
	en.EngramType = EngramType(engramType.String)
	if en.EngramType == "" {
		en.EngramType = EngramTypeKnowledge
	}
	if eventTime.Valid {
		en.EventTime = eventTime.Time
	}
	if labileUntil.Valid {
		en.LabileUntil = labileUntil.Time
	}

	if len(embeddingBytes) > 0 {
		json.Unmarshal(embeddingBytes, &en.Embedding)
	}

	return &en, nil
}

// scanEngramRows scans multiple rows into Engrams
func scanEngramRows(rows *sql.Rows) ([]*Engram, error) {
	var engrams []*Engram
	for rows.Next() {
		var en Engram
		var embeddingBytes []byte
		var summary sql.NullString
		var topic sql.NullString
		var engramType sql.NullString
		var eventTime sql.NullTime
		var labileUntil sql.NullTime

		err := rows.Scan(
			&en.ID, &summary, &topic, &engramType, &en.Activation, &en.Strength,
			&embeddingBytes, &eventTime, &en.CreatedAt, &en.LastAccessed, &labileUntil,
		)
		if err != nil {
			continue
		}

		en.Summary = summary.String
		en.Topic = topic.String
		en.EngramType = EngramType(engramType.String)
		if en.EngramType == "" {
			en.EngramType = EngramTypeKnowledge
		}
		if eventTime.Valid {
			en.EventTime = eventTime.Time
		}
		if labileUntil.Valid {
			en.LabileUntil = labileUntil.Time
		}

		if len(embeddingBytes) > 0 {
			json.Unmarshal(embeddingBytes, &en.Embedding)
		}

		engrams = append(engrams, &en)
	}
	return engrams, nil
}

// nullableTime converts a time.Time to sql.NullTime
func nullableTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{Valid: false}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// MarkEngramForReconsolidation marks an engram as needing reconsolidation
func (g *DB) MarkEngramForReconsolidation(engramID string) error {
	_, err := g.db.Exec(`UPDATE engrams SET needs_reconsolidation = 1 WHERE id = ?`, engramID)
	return err
}

// GetEngramsNeedingReconsolidation returns all engrams marked for reconsolidation
func (g *DB) GetEngramsNeedingReconsolidation() ([]string, error) {
	rows, err := g.db.Query(`SELECT id FROM engrams WHERE needs_reconsolidation = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var engramIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		engramIDs = append(engramIDs, id)
	}
	return engramIDs, rows.Err()
}

// ClearReconsolidationFlag removes the needs_reconsolidation flag from an engram
func (g *DB) ClearReconsolidationFlag(engramID string) error {
	_, err := g.db.Exec(`UPDATE engrams SET needs_reconsolidation = 0 WHERE id = ?`, engramID)
	return err
}

// UpdateEngramLabileUntil sets the labile_until timestamp for an engram.
// While labile, new related episodes extend the engram via reconsolidation.
// After the window expires, new related episodes form a separate engram.
func (g *DB) UpdateEngramLabileUntil(engramID string, labileUntil time.Time) error {
	_, err := g.db.Exec(
		`UPDATE engrams SET labile_until = ? WHERE id = ?`,
		nullableTime(labileUntil), engramID,
	)
	return err
}

// UpdateEngram updates an engram's summary, embedding, type, strength, and event_time after reconsolidation.
// eventTime should be MAX(timestamp_event) of all current source episodes.
func (g *DB) UpdateEngram(engramID, summary string, embedding []float64, engramType EngramType, strength int, eventTime time.Time) error {
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("failed to marshal embedding: %w", err)
	}

	_, err = g.db.Exec(`
		UPDATE engrams
		SET summary = ?, embedding = ?, engram_type = ?, strength = ?, event_time = ?, last_accessed = CURRENT_TIMESTAMP
		WHERE id = ?
	`, summary, embeddingJSON, engramType, strength, eventTime, engramID)
	if err != nil {
		return err
	}

	if g.vecAvailable && len(embedding) > 0 && g.vecDim == len(embedding) {
		g.syncEngramToVec(engramID, embedding)
	}

	return nil
}

// syncEngramToVec inserts or replaces an engram in the vec0 index.
func (g *DB) syncEngramToVec(engramID string, embedding []float64) {
	emb32 := normalizeFloat32(float64ToFloat32(embedding))
	serialized, serErr := sqlite_vec.SerializeFloat32(emb32)
	if serErr != nil {
		log.Printf("[graph] vec sync: SerializeFloat32 failed for %s: %v", engramID, serErr)
		return
	}

	var rowid int64
	if err := g.db.QueryRow(`SELECT rowid FROM engrams WHERE id = ?`, engramID).Scan(&rowid); err != nil {
		return
	}

	g.db.Exec(`DELETE FROM engram_vec WHERE rowid = ?`, rowid)
	g.db.Exec(`INSERT INTO engram_vec(rowid, embedding, engram_id) VALUES (?, ?, ?)`, rowid, serialized, engramID)
}

// AddEngramSummary stores a compression-level summary for an engram
func (g *DB) AddEngramSummary(engramID string, level int, summary string, tokens int) error {
	_, err := g.db.Exec(`
		INSERT INTO engram_summaries (engram_id, compression_level, summary, tokens)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(engram_id, compression_level) DO UPDATE SET
			summary = excluded.summary,
			tokens = excluded.tokens
	`, engramID, level, summary, tokens)
	return err
}
