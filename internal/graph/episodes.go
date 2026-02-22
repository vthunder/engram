package graph

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zeebo/blake3"
)

// generateShortID creates a 5-character display ID from BLAKE3 hash (kept for migration use)
func generateShortID(id string) string {
	hash := blake3.Sum256([]byte(id))
	return fmt.Sprintf("%x", hash[:])[:5]
}

// AddEpisode adds a new episode to the graph
func (g *DB) AddEpisode(ep *Episode) error {
	if ep.ID == "" {
		return fmt.Errorf("episode ID is required")
	}

	embeddingBytes, err := json.Marshal(ep.Embedding)
	if err != nil {
		embeddingBytes = nil
	}

	if ep.TimestampIngested.IsZero() {
		ep.TimestampIngested = time.Now()
	}
	if ep.CreatedAt.IsZero() {
		ep.CreatedAt = time.Now()
	}

	// Compute token count if not set
	if ep.TokenCount == 0 {
		ep.TokenCount = estimateTokens(ep.Content)
	}

	_, err = g.db.Exec(`
		INSERT INTO episodes (id, content, token_count, source, author, author_id, channel,
			timestamp_event, timestamp_ingested, dialogue_act, entropy_score,
			embedding, reply_to, authorization_checked, has_authorization, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			content = excluded.content,
			token_count = excluded.token_count,
			embedding = excluded.embedding,
			entropy_score = excluded.entropy_score,
			authorization_checked = excluded.authorization_checked,
			has_authorization = excluded.has_authorization
	`,
		ep.ID, ep.Content, ep.TokenCount, ep.Source, ep.Author, ep.AuthorID, ep.Channel,
		ep.TimestampEvent, ep.TimestampIngested, ep.DialogueAct, ep.EntropyScore,
		embeddingBytes, ep.ReplyTo, ep.AuthorizationChecked, ep.HasAuthorization, ep.CreatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to insert episode: %w", err)
	}

	// Create reply edge if applicable
	if ep.ReplyTo != "" {
		_, _ = g.db.Exec(`
			INSERT OR IGNORE INTO episode_edges (from_id, to_id, edge_type, weight)
			VALUES (?, ?, ?, 1.0)
		`, ep.ID, ep.ReplyTo, EdgeRepliesTo)
	}

	return nil
}

// GetAllEpisodes retrieves episodes with optional limit, ordered by timestamp desc
func (g *DB) GetAllEpisodes(limit int) ([]*Episode, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := g.db.Query(`
		SELECT id, content, token_count, source, author, author_id, channel,
			timestamp_event, timestamp_ingested, dialogue_act, entropy_score,
			embedding, reply_to, authorization_checked, has_authorization, created_at
		FROM episodes
		ORDER BY timestamp_event DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query episodes: %w", err)
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		ep, err := scanEpisodeRow(rows)
		if err != nil {
			continue
		}
		episodes = append(episodes, ep)
	}

	return episodes, nil
}

// SearchEpisodesByText performs a case-insensitive substring search on episode content.
func (g *DB) SearchEpisodesByText(query string, limit int) ([]*Episode, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := g.db.Query(`
		SELECT id, content, token_count, source, author, author_id, channel,
			timestamp_event, timestamp_ingested, dialogue_act, entropy_score,
			embedding, reply_to, authorization_checked, has_authorization, created_at
		FROM episodes
		WHERE content LIKE '%' || ? || '%'
		ORDER BY timestamp_event DESC
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search episodes: %w", err)
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		ep, err := scanEpisodeRow(rows)
		if err != nil {
			continue
		}
		episodes = append(episodes, ep)
	}
	return episodes, nil
}

// CountEpisodes returns the total number of episodes
func (g *DB) CountEpisodes() (int, error) {
	var count int
	err := g.db.QueryRow(`SELECT COUNT(*) FROM episodes`).Scan(&count)
	return count, err
}

// GetEpisode retrieves an episode by ID
func (g *DB) GetEpisode(id string) (*Episode, error) {
	row := g.db.QueryRow(`
		SELECT id, content, token_count, source, author, author_id, channel,
			timestamp_event, timestamp_ingested, dialogue_act, entropy_score,
			embedding, reply_to, authorization_checked, has_authorization, created_at
		FROM episodes WHERE id = ?
	`, id)

	return scanEpisode(row)
}

// GetEpisodes retrieves multiple episodes by ID
func (g *DB) GetEpisodes(ids []string) ([]*Episode, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query := `SELECT id, content, token_count, source, author, author_id, channel,
		timestamp_event, timestamp_ingested, dialogue_act, entropy_score,
		embedding, reply_to, authorization_checked, has_authorization, created_at FROM episodes WHERE id IN (`
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		if i > 0 {
			query += ","
		}
		query += "?"
		args[i] = id
	}
	query += ")"

	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query episodes: %w", err)
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		ep, err := scanEpisodeRow(rows)
		if err != nil {
			continue
		}
		episodes = append(episodes, ep)
	}

	return episodes, nil
}

// GetRecentEpisodes retrieves the most recent episodes, optionally filtered by channel.
func (g *DB) GetRecentEpisodes(channel string, limit int) ([]*Episode, error) {
	if limit <= 0 {
		limit = 30
	}

	var rows *sql.Rows
	var err error

	if channel != "" {
		rows, err = g.db.Query(`
			SELECT id, content, token_count, source, author, author_id, channel,
				timestamp_event, timestamp_ingested, dialogue_act, entropy_score,
				reply_to, authorization_checked, has_authorization, created_at
			FROM episodes
			WHERE channel = ?
			ORDER BY timestamp_event DESC
			LIMIT ?
		`, channel, limit)
	} else {
		rows, err = g.db.Query(`
			SELECT id, content, token_count, source, author, author_id, channel,
				timestamp_event, timestamp_ingested, dialogue_act, entropy_score,
				reply_to, authorization_checked, has_authorization, created_at
			FROM episodes
			ORDER BY timestamp_event DESC
			LIMIT ?
		`, limit)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to query episodes: %w", err)
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		ep, err := scanEpisodeRowNoEmbedding(rows)
		if err != nil {
			continue
		}
		episodes = append(episodes, ep)
	}

	return episodes, nil
}

// GetEpisodeReplies returns all episodes that reply to the given episode
func (g *DB) GetEpisodeReplies(id string) ([]*Episode, error) {
	rows, err := g.db.Query(`
		SELECT e.id, e.content, e.token_count, e.source, e.author, e.author_id, e.channel,
			e.timestamp_event, e.timestamp_ingested, e.dialogue_act, e.entropy_score,
			e.embedding, e.reply_to, e.authorization_checked, e.has_authorization, e.created_at
		FROM episodes e
		INNER JOIN episode_edges ee ON ee.from_id = e.id
		WHERE ee.to_id = ? AND ee.edge_type = ?
		ORDER BY e.timestamp_event ASC
	`, id, EdgeRepliesTo)

	if err != nil {
		return nil, fmt.Errorf("failed to query replies: %w", err)
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		ep, err := scanEpisodeRow(rows)
		if err != nil {
			continue
		}
		episodes = append(episodes, ep)
	}

	return episodes, nil
}

// AddEpisodeEdge adds an edge between two episodes
func (g *DB) AddEpisodeEdge(fromID, toID string, edgeType EdgeType, weight float64) error {
	_, err := g.db.Exec(`
		INSERT INTO episode_edges (from_id, to_id, edge_type, weight)
		VALUES (?, ?, ?, ?)
	`, fromID, toID, edgeType, weight)
	return err
}

// GetEpisodeNeighbors returns neighbors of an episode for spreading activation
func (g *DB) GetEpisodeNeighbors(id string) ([]Neighbor, error) {
	rows, err := g.db.Query(`
		SELECT to_id, weight, edge_type FROM episode_edges WHERE from_id = ?
		UNION ALL
		SELECT from_id, weight, edge_type FROM episode_edges WHERE to_id = ?
	`, id, id)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var neighbors []Neighbor
	for rows.Next() {
		var n Neighbor
		var edgeType string
		if err := rows.Scan(&n.ID, &n.Weight, &edgeType); err != nil {
			continue
		}
		n.Type = EdgeType(edgeType)
		neighbors = append(neighbors, n)
	}

	return neighbors, nil
}

// GetUnconsolidatedEpisodeCount returns the count of episodes not yet linked to any engram.
func (g *DB) GetUnconsolidatedEpisodeCount() (int, error) {
	var count int
	err := g.db.QueryRow(`
		SELECT COUNT(*) FROM episodes e
		WHERE NOT EXISTS (
			SELECT 1 FROM engram_episodes ee WHERE ee.episode_id = e.id
		)
	`).Scan(&count)
	return count, err
}

// GetUnconsolidatedEpisodeIDsForChannel returns unconsolidated episode IDs for a channel.
func (g *DB) GetUnconsolidatedEpisodeIDsForChannel(channelID string) (map[string]bool, error) {
	rows, err := g.db.Query(`
		SELECT e.id FROM episodes e
		LEFT JOIN engram_episodes ee ON ee.episode_id = e.id
		WHERE e.channel = ? AND ee.engram_id IS NULL
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("failed to query unconsolidated episode IDs: %w", err)
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		result[id] = true
	}
	return result, nil
}

// GetUnconsolidatedEpisodes returns episodes that haven't been linked to any engram yet.
func (g *DB) GetUnconsolidatedEpisodes(limit int) ([]*Episode, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := g.db.Query(`
		SELECT e.id, e.content, e.token_count, e.source, e.author, e.author_id, e.channel,
			e.timestamp_event, e.timestamp_ingested, e.dialogue_act, e.entropy_score,
			e.embedding, e.reply_to, e.authorization_checked, e.has_authorization, e.created_at
		FROM episodes e
		LEFT JOIN engram_episodes ee ON ee.episode_id = e.id
		WHERE ee.engram_id IS NULL
		ORDER BY e.timestamp_event ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query unconsolidated episodes: %w", err)
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		ep, err := scanEpisodeRow(rows)
		if err != nil {
			continue
		}
		episodes = append(episodes, ep)
	}

	return episodes, nil
}

// GetConsolidatedEpisodesWithEmbeddings returns episodes that have embeddings and are linked to at least one engram.
func (g *DB) GetConsolidatedEpisodesWithEmbeddings(offset, limit int) ([]*Episode, error) {
	if limit <= 0 {
		limit = 500
	}

	rows, err := g.db.Query(`
		SELECT DISTINCT e.id, e.content, e.token_count, e.source, e.author, e.author_id, e.channel,
			e.timestamp_event, e.timestamp_ingested, e.dialogue_act, e.entropy_score,
			e.embedding, e.reply_to, e.authorization_checked, e.has_authorization, e.created_at
		FROM episodes e
		INNER JOIN engram_episodes ee ON ee.episode_id = e.id
		WHERE e.embedding IS NOT NULL
		ORDER BY e.timestamp_event ASC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query consolidated episodes: %w", err)
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		ep, err := scanEpisodeRow(rows)
		if err != nil {
			continue
		}
		episodes = append(episodes, ep)
	}

	return episodes, nil
}

// UpdateEpisodeAuthorization updates the authorization status for an episode
func (g *DB) UpdateEpisodeAuthorization(episodeID string, hasAuth bool) error {
	_, err := g.db.Exec(`
		UPDATE episodes
		SET authorization_checked = 1, has_authorization = ?
		WHERE id = ?
	`, hasAuth, episodeID)
	return err
}

// GetEpisodeEntities returns the entity IDs mentioned in an episode
func (g *DB) GetEpisodeEntities(episodeID string) ([]string, error) {
	rows, err := g.db.Query(`
		SELECT entity_id FROM episode_mentions WHERE episode_id = ?
	`, episodeID)
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

// ChannelConsolidationStat holds per-channel stats used by the consolidation trigger.
type ChannelConsolidationStat struct {
	Channel             string
	UnconsolidatedCount int
	LastEpisodeTime     time.Time
}

// GetChannelConsolidationStats returns per-channel unconsolidated episode counts and
// last-episode timestamps for all channels with at least minEpisodes unconsolidated.
// Used by the smart consolidation trigger in the background loop.
func (g *DB) GetChannelConsolidationStats(minEpisodes int) ([]ChannelConsolidationStat, error) {
	rows, err := g.db.Query(`
		SELECT e.channel, COUNT(*) as cnt, MAX(e.timestamp_event) as last_time
		FROM episodes e
		LEFT JOIN engram_episodes ee ON ee.episode_id = e.id
		WHERE ee.engram_id IS NULL
		GROUP BY e.channel
		HAVING COUNT(*) >= ?
	`, minEpisodes)
	if err != nil {
		return nil, fmt.Errorf("failed to query channel consolidation stats: %w", err)
	}
	defer rows.Close()

	var stats []ChannelConsolidationStat
	for rows.Next() {
		var s ChannelConsolidationStat
		var channel sql.NullString
		if err := rows.Scan(&channel, &s.UnconsolidatedCount, &s.LastEpisodeTime); err != nil {
			continue
		}
		s.Channel = channel.String
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// GetEpisodesFiltered is the general-purpose episode list query for the API.
// It supports optional channel filtering, cursor-based pagination via beforeTimestamp,
// and filtering to unconsolidated-only episodes. Results are ordered newest-first.
// Embeddings are intentionally omitted for performance.
func (g *DB) GetEpisodesFiltered(channel string, beforeTimestamp *time.Time, unconsolidatedOnly bool, limit int) ([]*Episode, error) {
	if limit <= 0 {
		limit = 50
	}

	var sb strings.Builder
	var args []any

	sb.WriteString(`
		SELECT e.id, e.content, e.token_count, e.source, e.author, e.author_id, e.channel,
			e.timestamp_event, e.timestamp_ingested, e.dialogue_act, e.entropy_score,
			e.reply_to, e.authorization_checked, e.has_authorization, e.created_at
		FROM episodes e`)

	if unconsolidatedOnly {
		sb.WriteString(`
		LEFT JOIN engram_episodes ee ON ee.episode_id = e.id`)
	}

	sb.WriteString(`
		WHERE 1=1`)

	if channel != "" {
		sb.WriteString(` AND e.channel = ?`)
		args = append(args, channel)
	}
	if beforeTimestamp != nil {
		sb.WriteString(` AND e.timestamp_event < ?`)
		args = append(args, beforeTimestamp)
	}
	if unconsolidatedOnly {
		sb.WriteString(` AND ee.engram_id IS NULL`)
	}

	sb.WriteString(`
		ORDER BY e.timestamp_event DESC
		LIMIT ?`)
	args = append(args, limit)

	rows, err := g.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query episodes: %w", err)
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		ep, err := scanEpisodeRowNoEmbedding(rows)
		if err != nil {
			continue
		}
		episodes = append(episodes, ep)
	}
	return episodes, rows.Err()
}

// CountEpisodesFiltered returns the episode count matching optional channel and
// unconsolidated-only filters. Pass empty channel to include all channels.
func (g *DB) CountEpisodesFiltered(channel string, unconsolidatedOnly bool) (int, error) {
	var sb strings.Builder
	var args []any

	sb.WriteString(`SELECT COUNT(*) FROM episodes e`)

	if unconsolidatedOnly {
		sb.WriteString(`
		LEFT JOIN engram_episodes ee ON ee.episode_id = e.id`)
	}

	sb.WriteString(` WHERE 1=1`)

	if channel != "" {
		sb.WriteString(` AND e.channel = ?`)
		args = append(args, channel)
	}
	if unconsolidatedOnly {
		sb.WriteString(` AND ee.engram_id IS NULL`)
	}

	var count int
	err := g.db.QueryRow(sb.String(), args...).Scan(&count)
	return count, err
}

// scanEpisode scans a single row into an Episode
func scanEpisode(row *sql.Row) (*Episode, error) {
	var ep Episode
	var embeddingBytes []byte
	var author, authorID, channel, dialogueAct, replyTo sql.NullString
	var entropyScore sql.NullFloat64
	var authChecked, hasAuth sql.NullBool

	err := row.Scan(
		&ep.ID, &ep.Content, &ep.TokenCount, &ep.Source, &author, &authorID, &channel,
		&ep.TimestampEvent, &ep.TimestampIngested, &dialogueAct, &entropyScore,
		&embeddingBytes, &replyTo, &authChecked, &hasAuth, &ep.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	ep.Author = author.String
	ep.AuthorID = authorID.String
	ep.Channel = channel.String
	ep.DialogueAct = dialogueAct.String
	ep.ReplyTo = replyTo.String
	ep.EntropyScore = entropyScore.Float64
	ep.AuthorizationChecked = authChecked.Bool
	ep.HasAuthorization = hasAuth.Bool

	if len(embeddingBytes) > 0 {
		json.Unmarshal(embeddingBytes, &ep.Embedding)
	}

	return &ep, nil
}

// scanEpisodeRow scans from rows (multiple rows)
func scanEpisodeRow(rows *sql.Rows) (*Episode, error) {
	var ep Episode
	var embeddingBytes []byte
	var author, authorID, channel, dialogueAct, replyTo sql.NullString
	var entropyScore sql.NullFloat64
	var authChecked, hasAuth sql.NullBool

	err := rows.Scan(
		&ep.ID, &ep.Content, &ep.TokenCount, &ep.Source, &author, &authorID, &channel,
		&ep.TimestampEvent, &ep.TimestampIngested, &dialogueAct, &entropyScore,
		&embeddingBytes, &replyTo, &authChecked, &hasAuth, &ep.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	ep.Author = author.String
	ep.AuthorID = authorID.String
	ep.Channel = channel.String
	ep.DialogueAct = dialogueAct.String
	ep.ReplyTo = replyTo.String
	ep.EntropyScore = entropyScore.Float64
	ep.AuthorizationChecked = authChecked.Bool
	ep.HasAuthorization = hasAuth.Bool

	if len(embeddingBytes) > 0 {
		json.Unmarshal(embeddingBytes, &ep.Embedding)
	}

	return &ep, nil
}

// scanEpisodeRowNoEmbedding scans episode rows from queries that omit the embedding column.
func scanEpisodeRowNoEmbedding(rows *sql.Rows) (*Episode, error) {
	var ep Episode
	var author, authorID, channel, dialogueAct, replyTo sql.NullString
	var entropyScore sql.NullFloat64
	var authChecked, hasAuth sql.NullBool

	err := rows.Scan(
		&ep.ID, &ep.Content, &ep.TokenCount, &ep.Source, &author, &authorID, &channel,
		&ep.TimestampEvent, &ep.TimestampIngested, &dialogueAct, &entropyScore,
		&replyTo, &authChecked, &hasAuth, &ep.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	ep.Author = author.String
	ep.AuthorID = authorID.String
	ep.Channel = channel.String
	ep.DialogueAct = dialogueAct.String
	ep.ReplyTo = replyTo.String
	ep.EntropyScore = entropyScore.Float64
	ep.AuthorizationChecked = authChecked.Bool
	ep.HasAuthorization = hasAuth.Bool

	return &ep, nil
}
