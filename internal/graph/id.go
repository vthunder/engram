package graph

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/zeebo/blake3"
)

// GenerateEpisodeID returns a 32-char BLAKE3 hex ID for an episode.
// Input: content + source + created_at (nanoseconds).
func GenerateEpisodeID(content, source string, createdAtNs int64) string {
	h := blake3.New()
	h.Write([]byte(content))
	h.Write([]byte(source))
	h.Write([]byte(strconv.FormatInt(createdAtNs, 10)))
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum[:16]) // 128 bits = 32 hex chars
}

// GenerateEngramID returns a 32-char BLAKE3 hex ID for a consolidated engram.
func GenerateEngramID(content string, createdAtNs int64) string {
	h := blake3.New()
	h.Write([]byte(content))
	h.Write([]byte(strconv.FormatInt(createdAtNs, 10)))
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum[:16])
}

// GenerateSchemaID returns a 32-char BLAKE3 hex ID for a schema.
func GenerateSchemaID(name string, createdAtNs int64) string {
	h := blake3.New()
	h.Write([]byte("schema:"))
	h.Write([]byte(name))
	h.Write([]byte(strconv.FormatInt(createdAtNs, 10)))
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum[:16])
}

// ResolveID resolves a full or prefix ID against the given table.
// Prefix must be at least 5 characters. Returns the full 32-char ID or an error.
func ResolveID(db *sql.DB, table, prefix string) (string, error) {
	if len(prefix) < 5 {
		return "", fmt.Errorf("prefix too short: %q (minimum 5 chars)", prefix)
	}
	if len(prefix) == 32 {
		var id string
		err := db.QueryRow("SELECT id FROM "+table+" WHERE id = ?", prefix).Scan(&id)
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("not found: %s", prefix)
		}
		return id, err
	}

	upper := nextHexPrefix(prefix)
	var query string
	var args []any
	if upper == "" {
		query = "SELECT id FROM " + table + " WHERE id >= ? LIMIT 3"
		args = []any{prefix}
	} else {
		query = "SELECT id FROM " + table + " WHERE id >= ? AND id < ? LIMIT 3"
		args = []any{prefix, upper}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		matches = append(matches, id)
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("not found: %s", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", &AmbiguousRefError{Ref: prefix, Matches: matches}
	}
}

// AmbiguousRefError is returned when a prefix matches more than one ID.
type AmbiguousRefError struct {
	Ref     string
	Matches []string
}

func (e *AmbiguousRefError) Error() string {
	return fmt.Sprintf("ambiguous ref: %s matches %d objects", e.Ref, len(e.Matches))
}

// ResolveEngramID resolves a full or prefix engram ID using the DB.
func (g *DB) ResolveEngramID(prefix string) (string, error) {
	return ResolveID(g.db, "engrams", prefix)
}

// ResolveEpisodeID resolves a full or prefix episode ID using the DB.
func (g *DB) ResolveEpisodeID(prefix string) (string, error) {
	return ResolveID(g.db, "episodes", prefix)
}

// nextHexPrefix returns the smallest string greater than all strings
// with the given prefix. Returns "" if the prefix is all 'f' chars (open upper bound).
func nextHexPrefix(prefix string) string {
	b := []byte(strings.ToLower(prefix))
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 'f' {
			b[i]++
			return string(b[:i+1])
		}
	}
	return "" // all 'f' — open upper bound
}
