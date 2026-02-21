package graph

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// EpisodeEdgeRow represents a row from the episode_edges table
type EpisodeEdgeRow struct {
	FromID       string
	ToID         string
	Relationship string
	Confidence   float64
}

// QueryEpisodeEdges retrieves episode edges where both endpoints are in the given set
func (g *DB) QueryEpisodeEdges(episodeIDs map[string]bool) ([]EpisodeEdgeRow, error) {
	if len(episodeIDs) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(episodeIDs))
	for id := range episodeIDs {
		ids = append(ids, id)
	}

	query := `
		SELECT from_id, to_id, relationship_desc, confidence
		FROM episode_edges
		WHERE from_id IN (` + buildPlaceholders(len(ids)) + `)
		  AND to_id IN (` + buildPlaceholders(len(ids)) + `)
		  AND inferred_by_llm = 1`

	args := make([]interface{}, len(ids)*2)
	for i, id := range ids {
		args[i] = id
		args[len(ids)+i] = id
	}

	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []EpisodeEdgeRow
	for rows.Next() {
		var edge EpisodeEdgeRow
		if err := rows.Scan(&edge.FromID, &edge.ToID, &edge.Relationship, &edge.Confidence); err != nil {
			continue
		}
		edges = append(edges, edge)
	}

	return edges, nil
}

func buildPlaceholders(n int) string {
	if n == 0 {
		return ""
	}
	placeholders := make([]string, n)
	for i := range placeholders {
		placeholders[i] = "?"
	}
	return strings.Join(placeholders, ",")
}

// AddEpisodeEpisodeEdge creates a LLM-inferred semantic link between episodes.
// Sets inferred_by_llm=1 to distinguish from structural edges (REPLIES_TO) created at ingestion.
func (g *DB) AddEpisodeEpisodeEdge(fromID, toID, edgeType, relationshipDesc string, confidence float64) error {
	_, err := g.db.Exec(`
		INSERT INTO episode_edges (from_id, to_id, edge_type, relationship_desc, confidence, inferred_by_llm, created_at)
		VALUES (?, ?, ?, ?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT DO NOTHING
	`, fromID, toID, edgeType, relationshipDesc, confidence)
	return err
}

// QueryCrossBatchEpisodeEngrams finds engrams reachable from batch episodes via edges to
// consolidated (non-batch) episodes. Returns a map of batchEpisodeID → engramID.
// Because batch episodes are unconsolidated (not in engram_episodes), the JOIN to
// engram_episodes naturally excludes within-batch edges — no extra filter needed.
// Includes both structural (REPLIES_TO) and LLM-inferred edges so reply chains work.
func (g *DB) QueryCrossBatchEpisodeEngrams(batchIDs map[string]bool) (map[string]string, error) {
	if len(batchIDs) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(batchIDs))
	for id := range batchIDs {
		ids = append(ids, id)
	}
	ph := buildPlaceholders(len(ids))

	// Two directions: batch episode as from_id, and as to_id.
	// The JOIN with engram_episodes filters to consolidated counterparts only.
	query := `
		SELECT ee.from_id, eg.engram_id
		FROM episode_edges ee
		JOIN engram_episodes eg ON eg.episode_id = ee.to_id
		WHERE ee.from_id IN (` + ph + `)
		  AND eg.engram_id != '_ephemeral'
		UNION
		SELECT ee.to_id, eg.engram_id
		FROM episode_edges ee
		JOIN engram_episodes eg ON eg.episode_id = ee.from_id
		WHERE ee.to_id IN (` + ph + `)
		  AND eg.engram_id != '_ephemeral'`

	args := make([]interface{}, len(ids)*2)
	for i, id := range ids {
		args[i] = id
		args[len(ids)+i] = id
	}

	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var batchEpID, engramID string
		if err := rows.Scan(&batchEpID, &engramID); err != nil {
			continue
		}
		if _, exists := result[batchEpID]; !exists {
			result[batchEpID] = engramID
		}
	}
	return result, rows.Err()
}

// AddEpisodeEngramEdge creates link from episode to engram
func (g *DB) AddEpisodeEngramEdge(episodeID, engramID, relationshipDesc string, confidence float64) error {
	_, err := g.db.Exec(`
		INSERT INTO episode_engram_edges (episode_id, engram_id, relationship_desc, confidence)
		VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, episodeID, engramID, relationshipDesc, confidence)
	return err
}

// GetEpisodesReferencingEngram returns episodes linked to an engram
func (g *DB) GetEpisodesReferencingEngram(engramID string) ([]Episode, error) {
	rows, err := g.db.Query(`
		SELECT ep.id, ep.content, ep.timestamp_event, ep.author, ep.channel, ep.source
		FROM episodes ep
		JOIN episode_engram_edges et ON ep.id = et.episode_id
		WHERE et.engram_id = ?
		ORDER BY ep.timestamp_event ASC
	`, engramID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var episodes []Episode
	for rows.Next() {
		var ep Episode
		var timestampStr string
		err := rows.Scan(&ep.ID, &ep.Content, &timestampStr, &ep.Author, &ep.Channel, &ep.Source)
		if err != nil {
			return nil, err
		}
		ep.TimestampEvent, _ = time.Parse(time.RFC3339, timestampStr)
		episodes = append(episodes, ep)
	}
	return episodes, nil
}

// GetEngramsReferencedByEpisode returns engrams an episode links to
func (g *DB) GetEngramsReferencedByEpisode(episodeID string) ([]string, error) {
	rows, err := g.db.Query(`
		SELECT engram_id FROM episode_engram_edges WHERE episode_id = ?
	`, episodeID)
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
	return engramIDs, nil
}

// GetEpisodeEngramEdges returns all episode-engram edges for a given engram
func (g *DB) GetEpisodeEngramEdges(engramID string) ([]struct {
	EpisodeID        string
	EngramID         string
	RelationshipDesc string
	Confidence       float64
	CreatedAt        time.Time
}, error) {
	rows, err := g.db.Query(`
		SELECT episode_id, engram_id, relationship_desc, confidence, created_at
		FROM episode_engram_edges
		WHERE engram_id = ?
		ORDER BY created_at DESC
	`, engramID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []struct {
		EpisodeID        string
		EngramID         string
		RelationshipDesc string
		Confidence       float64
		CreatedAt        time.Time
	}

	for rows.Next() {
		var edge struct {
			EpisodeID        string
			EngramID         string
			RelationshipDesc string
			Confidence       float64
			CreatedAt        time.Time
		}
		if err := rows.Scan(&edge.EpisodeID, &edge.EngramID, &edge.RelationshipDesc, &edge.Confidence, &edge.CreatedAt); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}

	return edges, nil
}

// GetEpisodeEdges returns all episode-episode edges for a given episode
func (g *DB) GetEpisodeEdges(episodeID string) ([]struct {
	FromID           string
	ToID             string
	EdgeType         string
	RelationshipDesc sql.NullString
	Confidence       sql.NullFloat64
	Weight           float64
	CreatedAt        time.Time
}, error) {
	rows, err := g.db.Query(`
		SELECT from_id, to_id, edge_type, relationship_desc, confidence, weight, created_at
		FROM episode_edges
		WHERE from_id = ? OR to_id = ?
		ORDER BY created_at DESC
	`, episodeID, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []struct {
		FromID           string
		ToID             string
		EdgeType         string
		RelationshipDesc sql.NullString
		Confidence       sql.NullFloat64
		Weight           float64
		CreatedAt        time.Time
	}

	for rows.Next() {
		var edge struct {
			FromID           string
			ToID             string
			EdgeType         string
			RelationshipDesc sql.NullString
			Confidence       sql.NullFloat64
			Weight           float64
			CreatedAt        time.Time
		}
		if err := rows.Scan(&edge.FromID, &edge.ToID, &edge.EdgeType, &edge.RelationshipDesc, &edge.Confidence, &edge.Weight, &edge.CreatedAt); err != nil {
			continue
		}
		edges = append(edges, edge)
	}

	return edges, nil
}

// GetEngramSourceEpisodes retrieves all episode objects for an engram's source episodes
func (g *DB) GetEngramSourceEpisodes(engramID string) ([]Episode, error) {
	episodeIDs, err := g.GetEngramSources(engramID)
	if err != nil {
		return nil, err
	}

	var episodes []Episode
	for _, id := range episodeIDs {
		ep, err := g.GetEpisode(id)
		if err != nil {
			log.Printf("[reconsolidation] Warning: failed to get episode %s: %v", id, err)
			continue
		}
		if ep != nil {
			episodes = append(episodes, *ep)
		}
	}
	return episodes, nil
}

// ReconsolidateEngram updates an engram with new context from additional episodes
func (g *DB) ReconsolidateEngram(
	engramID string,
	newEpisodes []Episode,
	llmSummarize func([]Episode) (string, error),
	llmEmbed func(string) ([]float64, error),
) error {
	engram, err := g.GetEngram(engramID)
	if err != nil {
		return fmt.Errorf("failed to get engram: %w", err)
	}
	if engram == nil {
		return fmt.Errorf("engram not found: %s", engramID)
	}

	originalEpisodes, err := g.GetEngramSourceEpisodes(engramID)
	if err != nil {
		return fmt.Errorf("failed to get source episodes: %w", err)
	}

	allEpisodes := append(originalEpisodes, newEpisodes...)

	summary, err := llmSummarize(allEpisodes)
	if err != nil {
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	embedding, err := llmEmbed(summary)
	if err != nil {
		return fmt.Errorf("failed to generate embedding: %w", err)
	}

	err = g.updateEngramContent(engramID, summary, embedding)
	if err != nil {
		return fmt.Errorf("failed to update engram: %w", err)
	}

	for _, ep := range newEpisodes {
		err = g.LinkEngramToSource(engramID, ep.ID)
		if err != nil {
			log.Printf("[reconsolidation] Failed to add engram source: %v", err)
		}
	}

	err = g.RegeneratePyramidSummaries(engramID, summary, nil)
	if err != nil {
		return fmt.Errorf("failed to regenerate pyramid: %w", err)
	}

	log.Printf("[reconsolidation] Updated engram %s with %d new episodes", engramID, len(newEpisodes))
	return nil
}

// updateEngramContent updates engram embedding (internal helper)
func (g *DB) updateEngramContent(engramID string, summary string, embedding []float64) error {
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}

	_, err = g.db.Exec(`
		UPDATE engrams
		SET embedding = ?, last_accessed = CURRENT_TIMESTAMP
		WHERE id = ?
	`, embeddingJSON, engramID)
	return err
}

// RegeneratePyramidSummaries recreates all compression levels for an engram.
// Stores L0 (verbatim) synchronously; if compressor is non-nil, generates the
// full L4–L64 pyramid asynchronously in a background goroutine.
func (g *DB) RegeneratePyramidSummaries(engramID string, baseSummary string, compressor Compressor) error {
	_, err := g.db.Exec(`DELETE FROM engram_summaries WHERE engram_id = ?`, engramID)
	if err != nil {
		return err
	}

	_, err = g.db.Exec(`
		INSERT INTO engram_summaries (engram_id, compression_level, summary, tokens)
		VALUES (?, 0, ?, ?)
	`, engramID, baseSummary, len(baseSummary)/4)
	if err != nil {
		return fmt.Errorf("failed to store level 0 summary: %w", err)
	}

	if compressor != nil {
		go func() {
			sourceEpisodes, err := g.GetEngramSourceEpisodes(engramID)
			if err != nil || len(sourceEpisodes) == 0 {
				return
			}
			eps := make([]*Episode, len(sourceEpisodes))
			for i := range sourceEpisodes {
				eps[i] = &sourceEpisodes[i]
			}
			if err := g.GenerateEngramPyramid(engramID, eps, compressor); err != nil {
				log.Printf("[pyramid] engram %s: %v", engramID[:8], err)
			}
		}()
	}

	return nil
}
