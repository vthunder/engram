package graph

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

func init() {
	sqlite_vec.Auto() // registers the vec0 virtual table with go-sqlite3
}

// entityCacheEntry holds an entity and its pre-compiled word-boundary patterns,
// one per name/alias. Built once and reused across FindEntitiesByText calls.
type entityCacheEntry struct {
	entity   *Entity
	patterns []*regexp.Regexp // pre-compiled patterns, one per name/alias (nil = skip short names)
}

// DB wraps the SQLite database connection for the memory graph.
// It maintains three physical database files:
//   - memory.db      (g.db)      — main source-of-truth: episodes, engrams, entities, schemas
//   - memory-vectors.db (g.vectors) — embedding BLOBs + vec0 virtual table (recomputable)
//   - memory-cache.db   (g.cache)   — summary pyramids: episode/engram/schema_summaries (recomputable)
//
// The secondary databases are also ATTACHed to the main connection under the schema names
// "vectors" and "cache", so that correlated subqueries across tables work transparently.
type DB struct {
	db              *sql.DB // memory.db — main connection (MaxOpenConns=1 for stable ATTACH)
	vectors         *sql.DB // memory-vectors.db — embedding BLOBs + engram_vec
	cache           *sql.DB // memory-cache.db — summary pyramids
	path            string
	vectorsPath     string
	cachePath       string
	vecAvailable    bool
	vecDim          int  // embedding dimension used in engram_vec (0 = not yet determined)
	embeddingInMain bool // true if engrams.embedding column still exists (pre-v31)

	// Entity lookup cache: rebuilt lazily, invalidated on entity writes.
	entityCacheMu sync.RWMutex
	entityCache   []entityCacheEntry // nil means cache needs rebuild
}

// Open opens or creates the memory graph database.
// statePath is the directory that will contain memory.db, memory-vectors.db, memory-cache.db.
// Callers may pass vectorsPath/cachePath as overrides; if empty, defaults are derived from statePath.
func Open(statePath string, extraPaths ...string) (*DB, error) {
	// Derive paths: statePath/memory.db, statePath/memory-vectors.db, statePath/memory-cache.db
	dbPath := filepath.Join(statePath, "memory.db")
	vectorsPath := filepath.Join(statePath, "memory-vectors.db")
	cachePath := filepath.Join(statePath, "memory-cache.db")
	if len(extraPaths) >= 1 && extraPaths[0] != "" {
		vectorsPath = extraPaths[0]
	}
	if len(extraPaths) >= 2 && extraPaths[1] != "" {
		cachePath = extraPaths[1]
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Open main DB with MaxOpenConns(1) so ATTACH statements persist on the same connection.
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	db.SetMaxOpenConns(1)

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Enable foreign keys on main DB
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Open secondary DBs (independent connections for isolated writes/maintenance).
	vectors, err := sql.Open("sqlite3", vectorsPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to open vectors database: %w", err)
	}
	vectors.SetMaxOpenConns(1)

	cache, err := sql.Open("sqlite3", cachePath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		db.Close()
		vectors.Close()
		return nil, fmt.Errorf("failed to open cache database: %w", err)
	}
	cache.SetMaxOpenConns(1)

	// ATTACH secondary DBs to the main connection so cross-table queries work.
	// Tables in attached schemas are accessible as "vectors.tablename" and "cache.tablename",
	// but unqualified names also resolve when the table doesn't exist in main.
	if _, err := db.Exec(fmt.Sprintf(`ATTACH DATABASE %q AS vectors`, vectorsPath)); err != nil {
		db.Close()
		vectors.Close()
		cache.Close()
		return nil, fmt.Errorf("failed to attach vectors database: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`ATTACH DATABASE %q AS cache`, cachePath)); err != nil {
		db.Close()
		vectors.Close()
		cache.Close()
		return nil, fmt.Errorf("failed to attach cache database: %w", err)
	}

	// Enable WAL on attached databases
	db.Exec(`PRAGMA vectors.journal_mode = WAL`)
	db.Exec(`PRAGMA cache.journal_mode = WAL`)

	g := &DB{
		db:          db,
		vectors:     vectors,
		cache:       cache,
		path:        dbPath,
		vectorsPath: vectorsPath,
		cachePath:   cachePath,
	}

	// Initialize secondary DB schemas (idempotent)
	if err := g.initVectorsSchema(); err != nil {
		g.Close()
		return nil, fmt.Errorf("failed to init vectors schema: %w", err)
	}
	if err := g.initCacheSchema(); err != nil {
		g.Close()
		return nil, fmt.Errorf("failed to init cache schema: %w", err)
	}

	// Run migrations on main DB
	if err := g.migrate(); err != nil {
		g.Close()
		return nil, fmt.Errorf("failed to migrate: %w", err)
	}

	// Check if embedding column still exists in main.engrams.
	// For v31+, the column is kept for read compatibility but writes also go to vectors DB.
	g.embeddingInMain = g.columnExistsInMain("engrams", "embedding")

	// Check if sqlite-vec extension is available
	var vecVersion string
	if err := db.QueryRow("SELECT vec_version()").Scan(&vecVersion); err != nil {
		log.Printf("[graph] sqlite-vec not available: %v — falling back to full scan", err)
	} else {
		log.Printf("[graph] sqlite-vec %s loaded", vecVersion)
		g.vecAvailable = true
		// Ensure vec table exists and set vecDim from existing data (handles restarts
		// where migration v18 already ran but vecDim needs to be restored in memory).
		if g.vecDim == 0 {
			if err := g.initVecTableFromEngrams(); err != nil {
				log.Printf("[graph] vec init warning: %v", err)
			}
		}
	}

	return g, nil
}

// initVectorsSchema creates tables in memory-vectors.db (idempotent).
func (g *DB) initVectorsSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS episode_embeddings (id TEXT PRIMARY KEY, embedding BLOB)`,
		`CREATE TABLE IF NOT EXISTS engram_embeddings (id TEXT PRIMARY KEY, embedding BLOB)`,
		`CREATE TABLE IF NOT EXISTS schema_embeddings (id TEXT PRIMARY KEY, embedding BLOB)`,
	}
	for _, s := range stmts {
		if _, err := g.vectors.Exec(s); err != nil {
			return fmt.Errorf("vectors schema: %w", err)
		}
	}
	return nil
}

// initCacheSchema creates tables in memory-cache.db (idempotent).
// No FK constraints because the referenced tables live in a different DB.
func (g *DB) initCacheSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS episode_summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			episode_id TEXT NOT NULL,
			compression_level INTEGER NOT NULL,
			summary TEXT NOT NULL,
			tokens INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(episode_id, compression_level)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_episode_summaries_episode ON episode_summaries(episode_id)`,
		`CREATE INDEX IF NOT EXISTS idx_episode_summaries_level ON episode_summaries(compression_level)`,
		`CREATE TABLE IF NOT EXISTS engram_summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			engram_id TEXT NOT NULL,
			compression_level INTEGER NOT NULL,
			summary TEXT NOT NULL,
			tokens INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(engram_id, compression_level)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_engram_summaries_engram ON engram_summaries(engram_id)`,
		`CREATE INDEX IF NOT EXISTS idx_engram_summaries_level ON engram_summaries(compression_level)`,
		`CREATE TABLE IF NOT EXISTS schema_summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schema_id TEXT NOT NULL,
			compression_level INTEGER NOT NULL,
			summary TEXT NOT NULL,
			tokens INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(schema_id, compression_level)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_schema_summaries_schema ON schema_summaries(schema_id)`,
		`CREATE INDEX IF NOT EXISTS idx_schema_summaries_level ON schema_summaries(compression_level)`,
	}
	for _, s := range stmts {
		if _, err := g.cache.Exec(s); err != nil {
			return fmt.Errorf("cache schema: %w", err)
		}
	}
	return nil
}

// Close closes all three database connections.
func (g *DB) Close() error {
	var firstErr error
	if g.cache != nil {
		if err := g.cache.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if g.vectors != nil {
		if err := g.vectors.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if g.db != nil {
		if err := g.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// TestSetEngramTimestamp updates the last_accessed timestamp for an engram (for testing only)
func (g *DB) TestSetEngramTimestamp(engramID string, lastAccessed time.Time) error {
	_, err := g.db.Exec(`UPDATE engrams SET last_accessed = ? WHERE id = ?`, lastAccessed, engramID)
	return err
}

// SetEngramType sets the engram type for a given engram (for testing and classification)
func (g *DB) SetEngramType(engramID string, engramType EngramType) error {
	_, err := g.db.Exec(`UPDATE engrams SET engram_type = ? WHERE id = ?`, string(engramType), engramID)
	return err
}

// SetEngramActivation sets the activation level for an engram (for testing only)
func (g *DB) SetEngramActivation(engramID string, activation float64) error {
	_, err := g.db.Exec(`UPDATE engrams SET activation = ? WHERE id = ?`, activation, engramID)
	return err
}

// migrate runs database migrations
func (g *DB) migrate() error {
	schema := `
	-- Schema version tracking
	CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- TIER 1: EPISODES (Non-lossy raw messages)
	CREATE TABLE IF NOT EXISTS episodes (
		id TEXT PRIMARY KEY,
		content TEXT NOT NULL,
		source TEXT NOT NULL,
		author TEXT,
		author_id TEXT,
		channel TEXT,
		timestamp_event DATETIME NOT NULL,
		timestamp_ingested DATETIME NOT NULL,
		dialogue_act TEXT,
		entropy_score REAL,
		embedding BLOB,
		reply_to TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_episodes_timestamp ON episodes(timestamp_event);
	CREATE INDEX IF NOT EXISTS idx_episodes_channel ON episodes(channel);
	CREATE INDEX IF NOT EXISTS idx_episodes_author ON episodes(author_id);
	CREATE INDEX IF NOT EXISTS idx_episodes_reply_to ON episodes(reply_to);

	-- Episode edges (REPLIES_TO, FOLLOWS)
	CREATE TABLE IF NOT EXISTS episode_edges (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		from_id TEXT NOT NULL,
		to_id TEXT NOT NULL,
		edge_type TEXT NOT NULL,
		weight REAL DEFAULT 1.0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (from_id) REFERENCES episodes(id) ON DELETE CASCADE,
		FOREIGN KEY (to_id) REFERENCES episodes(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_episode_edges_from ON episode_edges(from_id);
	CREATE INDEX IF NOT EXISTS idx_episode_edges_to ON episode_edges(to_id);
	CREATE INDEX IF NOT EXISTS idx_episode_edges_type ON episode_edges(edge_type);

	-- TIER 2: ENTITIES (Extracted named entities)
	CREATE TABLE IF NOT EXISTS entities (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		salience REAL DEFAULT 0.0,
		embedding BLOB,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_entities_name ON entities(name);
	CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(type);
	CREATE INDEX IF NOT EXISTS idx_entities_salience ON entities(salience);

	-- Entity aliases (multiple names for same entity)
	CREATE TABLE IF NOT EXISTS entity_aliases (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		entity_id TEXT NOT NULL,
		alias TEXT NOT NULL,
		FOREIGN KEY (entity_id) REFERENCES entities(id) ON DELETE CASCADE,
		UNIQUE(entity_id, alias)
	);

	CREATE INDEX IF NOT EXISTS idx_entity_aliases_alias ON entity_aliases(alias);

	-- Episode mentions (episode -> entity)
	CREATE TABLE IF NOT EXISTS episode_mentions (
		episode_id TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		PRIMARY KEY (episode_id, entity_id),
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE,
		FOREIGN KEY (entity_id) REFERENCES entities(id) ON DELETE CASCADE
	);

	-- Entity relations (entity <-> entity) with temporal validity
	CREATE TABLE IF NOT EXISTS entity_relations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		from_id TEXT NOT NULL,
		to_id TEXT NOT NULL,
		relation_type TEXT NOT NULL,
		weight REAL DEFAULT 1.0,
		valid_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		invalid_at DATETIME,
		invalidated_by INTEGER,
		source_episode_id TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (from_id) REFERENCES entities(id) ON DELETE CASCADE,
		FOREIGN KEY (to_id) REFERENCES entities(id) ON DELETE CASCADE,
		FOREIGN KEY (invalidated_by) REFERENCES entity_relations(id),
		FOREIGN KEY (source_episode_id) REFERENCES episodes(id)
	);

	CREATE INDEX IF NOT EXISTS idx_entity_relations_from ON entity_relations(from_id);
	CREATE INDEX IF NOT EXISTS idx_entity_relations_to ON entity_relations(to_id);
	CREATE INDEX IF NOT EXISTS idx_entity_relations_valid ON entity_relations(invalid_at);

	-- TIER 3: TRACES (Consolidated memories)
	CREATE TABLE IF NOT EXISTS traces (
		id TEXT PRIMARY KEY,
		short_id TEXT DEFAULT '',
		summary TEXT,
		topic TEXT,
		activation REAL DEFAULT 0.5,
		strength INTEGER DEFAULT 1,
		embedding BLOB,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_accessed DATETIME DEFAULT CURRENT_TIMESTAMP,
		labile_until DATETIME,
		trace_type TEXT DEFAULT 'knowledge'
	);

	CREATE INDEX IF NOT EXISTS idx_traces_activation ON traces(activation);
	CREATE INDEX IF NOT EXISTS idx_traces_short_id ON traces(short_id);
	CREATE INDEX IF NOT EXISTS idx_traces_last_accessed ON traces(last_accessed);
	CREATE INDEX IF NOT EXISTS idx_traces_trace_type ON traces(trace_type);

	-- Trace sources (trace -> episode)
	CREATE TABLE IF NOT EXISTS trace_sources (
		trace_id TEXT NOT NULL,
		episode_id TEXT NOT NULL,
		PRIMARY KEY (trace_id, episode_id),
		FOREIGN KEY (trace_id) REFERENCES traces(id) ON DELETE CASCADE,
		FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE
	);

	-- Trace entities (trace -> entity)
	CREATE TABLE IF NOT EXISTS trace_entities (
		trace_id TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		PRIMARY KEY (trace_id, entity_id),
		FOREIGN KEY (trace_id) REFERENCES traces(id) ON DELETE CASCADE,
		FOREIGN KEY (entity_id) REFERENCES entities(id) ON DELETE CASCADE
	);

	-- Trace relations (trace <-> trace)
	CREATE TABLE IF NOT EXISTS trace_relations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		from_id TEXT NOT NULL,
		to_id TEXT NOT NULL,
		relation_type TEXT NOT NULL,
		weight REAL DEFAULT 1.0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (from_id) REFERENCES traces(id) ON DELETE CASCADE,
		FOREIGN KEY (to_id) REFERENCES traces(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_trace_relations_from ON trace_relations(from_id);
	CREATE INDEX IF NOT EXISTS idx_trace_relations_to ON trace_relations(to_id);
	CREATE INDEX IF NOT EXISTS idx_trace_relations_type ON trace_relations(relation_type);

	-- Record schema version
	INSERT OR IGNORE INTO schema_version (version) VALUES (1);
	`

	_, err := g.db.Exec(schema)
	if err != nil {
		return err
	}

	// Run incremental migrations
	return g.runMigrations()
}

// runMigrations applies incremental schema changes
func (g *DB) runMigrations() error {
	// Get current version
	var version int
	err := g.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		version = 1 // Assume v1 if can't read
	}

	// Migration v2: Add temporal columns to entity_relations
	if version < 2 {
		migrations := []string{
			"ALTER TABLE entity_relations ADD COLUMN valid_at DATETIME DEFAULT CURRENT_TIMESTAMP",
			"ALTER TABLE entity_relations ADD COLUMN invalid_at DATETIME",
			"ALTER TABLE entity_relations ADD COLUMN invalidated_by INTEGER",
			"ALTER TABLE entity_relations ADD COLUMN source_episode_id TEXT",
			"CREATE INDEX IF NOT EXISTS idx_entity_relations_valid ON entity_relations(invalid_at)",
		}
		for _, sql := range migrations {
			// Ignore errors for columns that already exist
			g.db.Exec(sql)
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (2)")
	}

	// Migration v3: Add index on trace_entities(entity_id) for entity-bridged activation
	if version < 3 {
		g.db.Exec("CREATE INDEX IF NOT EXISTS idx_trace_entities_entity ON trace_entities(entity_id)")
		g.db.Exec("INSERT INTO schema_version (version) VALUES (3)")
	}

	// Migration v4: Add trace_type column for operational vs knowledge classification
	if version < 4 {
		g.db.Exec("ALTER TABLE traces ADD COLUMN trace_type TEXT DEFAULT 'knowledge'")
		g.db.Exec("CREATE INDEX IF NOT EXISTS idx_traces_trace_type ON traces(trace_type)")
		// Backfill: tag existing traces that look operational
		g.db.Exec(`UPDATE traces SET trace_type = 'operational' WHERE
			(LOWER(summary) LIKE '%upcoming meeting%' OR
			 LOWER(summary) LIKE '%sprint planning%starts%' OR
			 LOWER(summary) LIKE '%heads up%meeting%' OR
			 LOWER(summary) LIKE '%state sync%' OR
			 LOWER(summary) LIKE '%synced state%' OR
			 LOWER(summary) LIKE '%no actionable work%' OR
			 LOWER(summary) LIKE '%idle wake%' OR
			 LOWER(summary) LIKE '%rebuilt binaries%')
			AND is_core = FALSE`)
		g.db.Exec("INSERT INTO schema_version (version) VALUES (4)")
	}

	// Migration v5: Expanded operational classification for meeting reminders and dev work notes
	if version < 5 {
		// Meeting reminders: "starts soon", "meeting starts", "meet.google.com"
		g.db.Exec(`UPDATE traces SET trace_type = 'operational' WHERE
			trace_type = 'knowledge' AND
			is_core = FALSE AND
			(LOWER(summary) LIKE '%starts soon%' OR
			 LOWER(summary) LIKE '%meeting starts%' OR
			 LOWER(summary) LIKE '%meet.google.com%' OR
			 LOWER(summary) LIKE '%starts in%' AND LOWER(summary) LIKE '%minute%')
			AND LOWER(summary) NOT LIKE '%discussed%'
			AND LOWER(summary) NOT LIKE '%decided%'`)

		// Dev work notes: past-tense implementation verbs without knowledge indicators
		// This is a simplified version - catches obvious cases
		g.db.Exec(`UPDATE traces SET trace_type = 'operational' WHERE
			trace_type = 'knowledge' AND
			is_core = FALSE AND
			(LOWER(summary) LIKE '%i updated %' OR
			 LOWER(summary) LIKE '%i implemented %' OR
			 LOWER(summary) LIKE '%i made%commit%' OR
			 LOWER(summary) LIKE '%i prepared%change%' OR
			 LOWER(summary) LIKE '%i proposed%' OR
			 LOWER(summary) LIKE 'explored %' OR
			 LOWER(summary) LIKE 'researched %')
			AND LOWER(summary) NOT LIKE '%because%'
			AND LOWER(summary) NOT LIKE '%decided%'
			AND LOWER(summary) NOT LIKE '%root cause%'
			AND LOWER(summary) NOT LIKE '%finding%'
			AND LOWER(summary) NOT LIKE '%learned%'
			AND LOWER(summary) NOT LIKE '%conclusion%'`)

		g.db.Exec("INSERT INTO schema_version (version) VALUES (5)")
	}

	// Migration v6: Populate trace_relations with similarity-based edges (legacy, pre-engrams).
	// Only relevant for databases that still have a traces table. Skipped for fresh databases
	// (traces table is dropped in v21 and replaced by engrams + engram_relations).
	if version < 6 {
		// Best-effort: populate trace_relations if traces table exists with embeddings.
		// Failures are non-fatal; v21 will drop trace_relations anyway.
		rows, _ := g.db.Query(`SELECT id, embedding FROM traces WHERE embedding IS NOT NULL`)
		if rows != nil {
			rows.Close()
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (6)")
	}

	// Migration v7: Add episode_summaries table for pyramid summaries
	if version < 7 {
		migrations := []string{
			`CREATE TABLE IF NOT EXISTS episode_summaries (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				episode_id TEXT NOT NULL,
				compression_level INTEGER NOT NULL,
				summary TEXT NOT NULL,
				tokens INTEGER NOT NULL,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE,
				UNIQUE(episode_id, compression_level)
			)`,
			"CREATE INDEX IF NOT EXISTS idx_episode_summaries_episode ON episode_summaries(episode_id)",
			"CREATE INDEX IF NOT EXISTS idx_episode_summaries_level ON episode_summaries(compression_level)",
		}
		for _, sql := range migrations {
			if _, err := g.db.Exec(sql); err != nil {
				return fmt.Errorf("migration v7 failed: %w", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (7)")
	}

	// Migration v8: Add token_count and short_id to episodes, remove level 0 summaries
	if version < 8 {
		migrations := []string{
			"ALTER TABLE episodes ADD COLUMN token_count INTEGER DEFAULT 0",
			"ALTER TABLE episodes ADD COLUMN short_id TEXT DEFAULT ''",
			"CREATE INDEX IF NOT EXISTS idx_episodes_short_id ON episodes(short_id)",
			"DELETE FROM episode_summaries WHERE compression_level = 0",
		}
		for _, sql := range migrations {
			// Ignore errors for columns that already exist
			g.db.Exec(sql)
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (8)")
	}

	// Migration v9: Add trace_summaries table for pyramid summaries
	if version < 9 {
		migrations := []string{
			`CREATE TABLE IF NOT EXISTS trace_summaries (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				trace_id TEXT NOT NULL,
				compression_level INTEGER NOT NULL,
				summary TEXT NOT NULL,
				tokens INTEGER NOT NULL,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (trace_id) REFERENCES traces(id) ON DELETE CASCADE,
				UNIQUE(trace_id, compression_level)
			)`,
			"CREATE INDEX IF NOT EXISTS idx_trace_summaries_trace ON trace_summaries(trace_id)",
			"CREATE INDEX IF NOT EXISTS idx_trace_summaries_level ON trace_summaries(compression_level)",
		}
		for _, sql := range migrations {
			if _, err := g.db.Exec(sql); err != nil {
				return fmt.Errorf("migration v9 failed: %w", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (9)")
	}

	// Migration v10: Make traces.summary nullable (deprecated - use trace_summaries instead)
	// SQLite doesn't support ALTER COLUMN, so we need to recreate the table
	if version < 10 {
		// Check if is_core column exists (it was added in v4, but fresh DBs may not have it)
		hasIsCore := false
		pragmaRows, _ := g.db.Query("PRAGMA table_info(traces)")
		if pragmaRows != nil {
			for pragmaRows.Next() {
				var cid int
				var name, colType string
				var notNull int
				var dflt interface{}
				var pk int
				if err := pragmaRows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err == nil && name == "is_core" {
					hasIsCore = true
				}
			}
			pragmaRows.Close()
		}

		insertSQL := `INSERT INTO traces_new SELECT id, summary, topic, activation, strength, is_core, embedding, created_at, last_accessed, labile_until, trace_type FROM traces`
		if !hasIsCore {
			insertSQL = `INSERT INTO traces_new SELECT id, summary, topic, activation, strength, FALSE, embedding, created_at, last_accessed, labile_until, trace_type FROM traces`
		}

		migrations := []string{
			`CREATE TABLE IF NOT EXISTS traces_new (
				id TEXT PRIMARY KEY,
				summary TEXT,
				topic TEXT,
				activation REAL DEFAULT 0.5,
				strength INTEGER DEFAULT 1,
				is_core BOOLEAN DEFAULT FALSE,
				embedding BLOB,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				last_accessed DATETIME DEFAULT CURRENT_TIMESTAMP,
				labile_until DATETIME,
				trace_type TEXT DEFAULT 'knowledge'
			)`,
			insertSQL,
			`DROP TABLE traces`,
			`ALTER TABLE traces_new RENAME TO traces`,
			`CREATE INDEX IF NOT EXISTS idx_traces_activation ON traces(activation)`,
			`CREATE INDEX IF NOT EXISTS idx_traces_is_core ON traces(is_core)`,
			`CREATE INDEX IF NOT EXISTS idx_traces_last_accessed ON traces(last_accessed)`,
			`CREATE INDEX IF NOT EXISTS idx_traces_trace_type ON traces(trace_type)`,
		}
		for _, sql := range migrations {
			if _, err := g.db.Exec(sql); err != nil {
				return fmt.Errorf("migration v10 failed: %w", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (10)")
	}

	// Migration v11: Backfill short_id for episodes missing it
	if version < 11 {
		// Get all episodes without short_id
		rows, err := g.db.Query("SELECT id FROM episodes WHERE short_id IS NULL OR short_id = ''")
		if err == nil {
			var ids []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()

			// Generate and update short_id for each episode
			for _, id := range ids {
				shortID := generateShortID(id)
				g.db.Exec("UPDATE episodes SET short_id = ? WHERE id = ?", shortID, id)
			}
			if len(ids) > 0 {
				log.Printf("[graph] Backfilled short_id for %d episodes", len(ids))
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (11)")
	}

	// Migration v12: Episode linking (episode-episode + episode-trace) for reconsolidation
	if version < 12 {
		log.Println("[graph] Migrating to schema v12: episode linking")

		// 1. Enhance episode_edges table with relationship descriptors
		migrations := []string{
			"ALTER TABLE episode_edges ADD COLUMN relationship_desc TEXT",
			"ALTER TABLE episode_edges ADD COLUMN confidence REAL DEFAULT 1.0",
		}
		for _, sql := range migrations {
			// Ignore errors for columns that already exist
			g.db.Exec(sql)
		}

		// 2. Create episode_trace_edges table
		_, err := g.db.Exec(`
			CREATE TABLE IF NOT EXISTS episode_trace_edges (
				episode_id TEXT NOT NULL,
				trace_id TEXT NOT NULL,
				relationship_desc TEXT NOT NULL,
				confidence REAL DEFAULT 1.0,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (episode_id) REFERENCES episodes(id) ON DELETE CASCADE,
				FOREIGN KEY (trace_id) REFERENCES traces(id) ON DELETE CASCADE,
				PRIMARY KEY (episode_id, trace_id)
			)
		`)
		if err != nil {
			return fmt.Errorf("migration v12 failed to create episode_trace_edges: %w", err)
		}

		// 3. Create indexes
		g.db.Exec("CREATE INDEX IF NOT EXISTS idx_episode_trace_trace ON episode_trace_edges(trace_id)")
		g.db.Exec("CREATE INDEX IF NOT EXISTS idx_episode_trace_episode ON episode_trace_edges(episode_id)")

			g.db.Exec("INSERT INTO schema_version (version) VALUES (12)")
		log.Println("[graph] Migration to v12 completed successfully")
	}

	// Migration v13: Add short_id to traces table
	if version < 13 {
		log.Println("[graph] Migrating to schema v13: add trace short_id")

		migrations := []string{
			"ALTER TABLE traces ADD COLUMN short_id TEXT DEFAULT ''",
			"CREATE INDEX IF NOT EXISTS idx_traces_short_id ON traces(short_id)",
		}
		for _, sql := range migrations {
			// Ignore errors for columns that already exist
			g.db.Exec(sql)
		}

		// Backfill short_id for existing traces
		rows, err := g.db.Query("SELECT id FROM traces WHERE short_id = '' OR short_id IS NULL")
		if err == nil {
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()

			// Generate and update short_id for each trace
			for _, id := range ids {
				shortID := generateShortID(id)
				g.db.Exec("UPDATE traces SET short_id = ? WHERE id = ?", shortID, id)
			}
			if len(ids) > 0 {
				log.Printf("[graph] Backfilled short_id for %d traces", len(ids))
			}
		}

		g.db.Exec("INSERT INTO schema_version (version) VALUES (13)")
		log.Println("[graph] Migration to v13 completed successfully")
	}

	// Migration v14: Remove is_core column (core identity now loaded from state/system/core.md)
	if version < 14 {
		log.Println("[graph] Migrating to schema v14: remove is_core column")

		// SQLite doesn't support DROP COLUMN, so we need to recreate the table
		migrations := []string{
			`CREATE TABLE IF NOT EXISTS traces_new (
				id TEXT PRIMARY KEY,
				short_id TEXT DEFAULT '',
				summary TEXT,
				topic TEXT,
				activation REAL DEFAULT 0.5,
				strength INTEGER DEFAULT 1,
				embedding BLOB,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				last_accessed DATETIME DEFAULT CURRENT_TIMESTAMP,
				labile_until DATETIME,
				trace_type TEXT DEFAULT 'knowledge'
			)`,
			`INSERT INTO traces_new SELECT id, short_id, summary, topic, activation, strength, embedding, created_at, last_accessed, labile_until, trace_type FROM traces`,
			`DROP TABLE traces`,
			`ALTER TABLE traces_new RENAME TO traces`,
			`CREATE INDEX IF NOT EXISTS idx_traces_activation ON traces(activation)`,
			`CREATE INDEX IF NOT EXISTS idx_traces_short_id ON traces(short_id)`,
			`CREATE INDEX IF NOT EXISTS idx_traces_last_accessed ON traces(last_accessed)`,
			`CREATE INDEX IF NOT EXISTS idx_traces_trace_type ON traces(trace_type)`,
		}
		for _, sql := range migrations {
			if _, err := g.db.Exec(sql); err != nil {
				return fmt.Errorf("migration v14 failed: %w", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (14)")
		log.Println("[graph] Migration to v14 completed successfully")
	}

	// Migration v15: Add needs_reconsolidation flag for incremental clustering
	if version < 15 {
		_, err := g.db.Exec(`ALTER TABLE traces ADD COLUMN needs_reconsolidation BOOLEAN DEFAULT 0`)
		if err != nil {
			// Ignore errors for columns that already exist
			g.db.Exec("ALTER TABLE traces ADD COLUMN needs_reconsolidation BOOLEAN DEFAULT 0")
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (15)")
		log.Println("[graph] Migration to v15 completed successfully")
	}

	// Migration v16: Add authorization tracking columns to episodes
	if version < 16 {
		g.db.Exec(`ALTER TABLE episodes ADD COLUMN authorization_checked INTEGER DEFAULT 0`)
		g.db.Exec(`ALTER TABLE episodes ADD COLUMN has_authorization INTEGER DEFAULT 0`)
		g.db.Exec("INSERT INTO schema_version (version) VALUES (16)")
		log.Println("[graph] Migration to v16 completed successfully")
	}

	// Migration v17: Add FTS5 virtual table for trace keyword search.
	// Indexes level-32 summaries from trace_summaries for fast BM25 MATCH queries,
	// replacing the Go-side full table scan in FindTracesWithKeywords.
	if version < 17 {
		log.Println("[graph] Migrating to schema v17: FTS5 index for trace keyword search")
		migrations := []string{
			// Create FTS5 table with content= pointing to trace_summaries
			`CREATE VIRTUAL TABLE IF NOT EXISTS trace_fts USING fts5(
				trace_id UNINDEXED,
				summary,
				content=trace_summaries,
				content_rowid=id
			)`,
			// Populate FTS5 from existing level-32 summaries
			`INSERT INTO trace_fts(rowid, trace_id, summary)
				SELECT id, trace_id, summary FROM trace_summaries WHERE compression_level = 32`,
			// Trigger: keep FTS5 in sync when a summary is inserted
			`CREATE TRIGGER IF NOT EXISTS trace_summaries_ai
				AFTER INSERT ON trace_summaries
				WHEN NEW.compression_level = 32
				BEGIN
					INSERT INTO trace_fts(rowid, trace_id, summary) VALUES (NEW.id, NEW.trace_id, NEW.summary);
				END`,
			// Trigger: keep FTS5 in sync when a summary is updated
			`CREATE TRIGGER IF NOT EXISTS trace_summaries_au
				AFTER UPDATE ON trace_summaries
				WHEN NEW.compression_level = 32
				BEGIN
					INSERT INTO trace_fts(trace_fts, rowid, trace_id, summary) VALUES ('delete', OLD.id, OLD.trace_id, OLD.summary);
					INSERT INTO trace_fts(rowid, trace_id, summary) VALUES (NEW.id, NEW.trace_id, NEW.summary);
				END`,
			// Trigger: keep FTS5 in sync when a summary is deleted
			`CREATE TRIGGER IF NOT EXISTS trace_summaries_ad
				AFTER DELETE ON trace_summaries
				WHEN OLD.compression_level = 32
				BEGIN
					INSERT INTO trace_fts(trace_fts, rowid, trace_id, summary) VALUES ('delete', OLD.id, OLD.trace_id, OLD.summary);
				END`,
		}
		for _, sql := range migrations {
			if _, err := g.db.Exec(sql); err != nil {
				// Non-fatal: FTS5 may not be compiled in; fall back gracefully
				log.Printf("[graph] Migration v17 warning (FTS5 may be unavailable): %v", err)
				break
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (17)")
		log.Println("[graph] Migration to v17 completed successfully")
	}

	// Migration v18: Add sqlite-vec ANN index for trace embedding search.
	// Creates a vec0 virtual table for fast cosine KNN queries, replacing the O(n)
	// Go-side scan in FindSimilarTraces. Backfills from the traces table on first run.
	// Skipped gracefully if sqlite-vec extension is not compiled in or no embeddings exist.
	// The vec table dimension is determined dynamically from existing trace embeddings.
	if version < 18 {
		log.Println("[graph] Migrating to schema v18: sqlite-vec trace_vec index")
		// Detect embedding dimension from existing traces (if any)
		if err := g.initVecTableFromEngrams(); err != nil {
			log.Printf("[graph] Migration v18 warning: %v — vec index deferred to first AddEngram", err)
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (18)")
		log.Println("[graph] Migration to v18 completed successfully")
	}

	// Migration v19: Add index on trace_sources(episode_id) for efficient
	// unconsolidated-episode lookups. The composite PK (trace_id, episode_id)
	// can't be used for joins/lookups on episode_id alone, causing full-table scans
	// (~400ms per wake). This index brings the query to <10ms.
	if version < 19 {
		log.Println("[graph] Migrating to schema v19: idx_trace_sources_episode")
		g.db.Exec("CREATE INDEX IF NOT EXISTS idx_trace_sources_episode ON trace_sources(episode_id)")
		g.db.Exec("INSERT INTO schema_version (version) VALUES (19)")
		log.Println("[graph] Migration to v19 completed successfully")
	}

	// Migration v20: Repair FTS5 table if it was skipped during v17 due to missing build tag.
	// v17 marked itself complete even when FTS5 creation failed, leaving trace_fts absent.
	// This migration re-attempts FTS5 setup idempotently; it's a no-op if trace_fts exists.
	if version < 20 {
		log.Println("[graph] Migrating to schema v20: FTS5 repair (idempotent re-attempt)")
		migrations := []string{
			`CREATE VIRTUAL TABLE IF NOT EXISTS trace_fts USING fts5(
				trace_id UNINDEXED,
				summary,
				content=trace_summaries,
				content_rowid=id
			)`,
			`INSERT OR IGNORE INTO trace_fts(rowid, trace_id, summary)
				SELECT id, trace_id, summary FROM trace_summaries WHERE compression_level = 32`,
			`CREATE TRIGGER IF NOT EXISTS trace_summaries_ai
				AFTER INSERT ON trace_summaries
				WHEN NEW.compression_level = 32
				BEGIN
					INSERT INTO trace_fts(rowid, trace_id, summary) VALUES (NEW.id, NEW.trace_id, NEW.summary);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trace_summaries_au
				AFTER UPDATE ON trace_summaries
				WHEN NEW.compression_level = 32
				BEGIN
					INSERT INTO trace_fts(trace_fts, rowid, trace_id, summary) VALUES ('delete', OLD.id, OLD.trace_id, OLD.summary);
					INSERT INTO trace_fts(rowid, trace_id, summary) VALUES (NEW.id, NEW.trace_id, NEW.summary);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trace_summaries_ad
				AFTER DELETE ON trace_summaries
				WHEN OLD.compression_level = 32
				BEGIN
					INSERT INTO trace_fts(trace_fts, rowid, trace_id, summary) VALUES ('delete', OLD.id, OLD.trace_id, OLD.summary);
				END`,
		}
		ftsOK := true
		for _, sql := range migrations {
			if _, err := g.db.Exec(sql); err != nil {
				log.Printf("[graph] Migration v20 warning (FTS5 may be unavailable): %v", err)
				ftsOK = false
				break
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (20)")
		if ftsOK {
			log.Println("[graph] Migration to v20 completed: FTS5 table created/repaired")
		} else {
			log.Println("[graph] Migration to v20 skipped: FTS5 not available (rebuild with -tags fts5)")
		}
	}

	// Migration v21: Rename traces→engrams, drop short_id columns, adopt BLAKE3 IDs.
	// Drops all trace_* tables and creates engrams schema. Existing data is discarded
	// (fresh schema, no migration of old trace data — IDs are incompatible).
	if version < 21 {
		log.Println("[graph] Migrating to schema v21: traces→engrams, BLAKE3 IDs")

		dropAndCreate := []string{
			// Drop old trace tables (order matters for FK constraints)
			`DROP TABLE IF EXISTS trace_fts`,
			`DROP TABLE IF EXISTS trace_summaries`,
			`DROP TABLE IF EXISTS trace_entities`,
			`DROP TABLE IF EXISTS trace_sources`,
			`DROP TABLE IF EXISTS trace_relations`,
			`DROP TABLE IF EXISTS episode_trace_edges`,
			`DROP TABLE IF EXISTS traces`,

			// Rebuild episodes without short_id; id is now a 32-char BLAKE3 hex.
			// Existing rows with old-format IDs remain — they just won't match new
			// BLAKE3 IDs generated by handlers.  New ingestions will always produce
			// BLAKE3 IDs from the handler.
			// Drop index first (SQLite requires this before dropping the column)
			`DROP INDEX IF EXISTS idx_episodes_short_id`,
			`ALTER TABLE episodes DROP COLUMN short_id`,

			// Engrams table
			`CREATE TABLE IF NOT EXISTS engrams (
				id             TEXT PRIMARY KEY,
				summary        TEXT,
				topic          TEXT,
				engram_type    TEXT DEFAULT 'knowledge',
				activation     REAL DEFAULT 0.5,
				strength       INTEGER DEFAULT 1,
				embedding      BLOB,
				created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
				last_accessed  DATETIME DEFAULT CURRENT_TIMESTAMP,
				labile_until   DATETIME,
				needs_reconsolidation BOOLEAN DEFAULT 0
			)`,
			`CREATE INDEX IF NOT EXISTS idx_engrams_activation ON engrams(activation)`,
			`CREATE INDEX IF NOT EXISTS idx_engrams_last_accessed ON engrams(last_accessed)`,
			`CREATE INDEX IF NOT EXISTS idx_engrams_type ON engrams(engram_type)`,

			// Engram summaries (pyramid levels)
			`CREATE TABLE IF NOT EXISTS engram_summaries (
				id               INTEGER PRIMARY KEY AUTOINCREMENT,
				engram_id        TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				compression_level INTEGER NOT NULL,
				summary          TEXT NOT NULL,
				tokens           INTEGER NOT NULL,
				created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
				UNIQUE(engram_id, compression_level)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_engram_summaries_engram ON engram_summaries(engram_id)`,
			`CREATE INDEX IF NOT EXISTS idx_engram_summaries_level ON engram_summaries(compression_level)`,

			// Engram-episode junction (replaces trace_sources)
			`CREATE TABLE IF NOT EXISTS engram_episodes (
				engram_id  TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				episode_id TEXT NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
				PRIMARY KEY (engram_id, episode_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_engram_episodes_episode ON engram_episodes(episode_id)`,

			// Engram-entity junction (replaces trace_entities)
			`CREATE TABLE IF NOT EXISTS engram_entities (
				engram_id TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
				PRIMARY KEY (engram_id, entity_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_engram_entities_entity ON engram_entities(entity_id)`,

			// Engram relations (replaces trace_relations)
			`CREATE TABLE IF NOT EXISTS engram_relations (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				from_id      TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				to_id        TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				relation_type TEXT NOT NULL,
				weight       REAL DEFAULT 1.0,
				created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_engram_relations_from ON engram_relations(from_id)`,
			`CREATE INDEX IF NOT EXISTS idx_engram_relations_to ON engram_relations(to_id)`,

			// Episode-engram edges (replaces episode_trace_edges)
			`CREATE TABLE IF NOT EXISTS episode_engram_edges (
				episode_id        TEXT NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
				engram_id         TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				relationship_desc TEXT NOT NULL,
				confidence        REAL DEFAULT 1.0,
				created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (episode_id, engram_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_episode_engram_episode ON episode_engram_edges(episode_id)`,
			`CREATE INDEX IF NOT EXISTS idx_episode_engram_engram ON episode_engram_edges(engram_id)`,
		}

		for _, sql := range dropAndCreate {
			if _, err := g.db.Exec(sql); err != nil {
				// Some DROP statements may fail on fresh DBs — non-fatal
				log.Printf("[graph] Migration v21 (non-fatal): %v", err)
			}
		}

		// Create FTS5 for engram_summaries (non-fatal if FTS5 unavailable)
		ftsMigrations := []string{
			`CREATE VIRTUAL TABLE IF NOT EXISTS engram_fts USING fts5(
				engram_id UNINDEXED,
				summary,
				content=engram_summaries,
				content_rowid=id
			)`,
			`INSERT OR IGNORE INTO engram_fts(rowid, engram_id, summary)
				SELECT id, engram_id, summary FROM engram_summaries WHERE compression_level = 32`,
			`CREATE TRIGGER IF NOT EXISTS engram_summaries_ai
				AFTER INSERT ON engram_summaries
				WHEN NEW.compression_level = 32
				BEGIN
					INSERT INTO engram_fts(rowid, engram_id, summary) VALUES (NEW.id, NEW.engram_id, NEW.summary);
				END`,
			`CREATE TRIGGER IF NOT EXISTS engram_summaries_au
				AFTER UPDATE ON engram_summaries
				WHEN NEW.compression_level = 32
				BEGIN
					INSERT INTO engram_fts(engram_fts, rowid, engram_id, summary) VALUES ('delete', OLD.id, OLD.engram_id, OLD.summary);
					INSERT INTO engram_fts(rowid, engram_id, summary) VALUES (NEW.id, NEW.engram_id, NEW.summary);
				END`,
			`CREATE TRIGGER IF NOT EXISTS engram_summaries_ad
				AFTER DELETE ON engram_summaries
				WHEN OLD.compression_level = 32
				BEGIN
					INSERT INTO engram_fts(engram_fts, rowid, engram_id, summary) VALUES ('delete', OLD.id, OLD.engram_id, OLD.summary);
				END`,
		}
		for _, sql := range ftsMigrations {
			if _, err := g.db.Exec(sql); err != nil {
				log.Printf("[graph] Migration v21 FTS5 warning: %v", err)
				break
			}
		}

		g.db.Exec("INSERT INTO schema_version (version) VALUES (21)")
		log.Println("[graph] Migration to v21 completed: engrams schema active")

		// Re-init vec table for engrams (new table name)
		g.vecDim = 0 // reset so ensureVecTable rebuilds
		if err := g.initVecTableFromEngrams(); err != nil {
			log.Printf("[graph] vec init post-v21: %v", err)
		}
	}

	// Migration v22: Add entity_summaries table for pyramid summaries
	if version < 22 {
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS entity_summaries (
				id               INTEGER PRIMARY KEY AUTOINCREMENT,
				entity_id        TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
				compression_level INTEGER NOT NULL,
				summary          TEXT NOT NULL,
				tokens           INTEGER NOT NULL,
				created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
				UNIQUE(entity_id, compression_level)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_entity_summaries_entity ON entity_summaries(entity_id)`,
			`CREATE INDEX IF NOT EXISTS idx_entity_summaries_level ON entity_summaries(compression_level)`,
		}
		for _, sql := range stmts {
			if _, err := g.db.Exec(sql); err != nil {
				log.Printf("[graph] Migration v22 error: %v", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (22)")
		log.Println("[graph] Migration to v22 completed: entity_summaries table added")
	}

	// Migration v23: Add inferred_by_llm to episode_edges to distinguish structural edges
	// (REPLIES_TO created at ingestion, inferred_by_llm=0) from LLM-inferred semantic edges
	// (inferred_by_llm=1). Consolidation only skips LLM inference when semantic edges already
	// exist, preventing structural reply edges from permanently blocking inference.
	if version < 23 {
		g.db.Exec(`ALTER TABLE episode_edges ADD COLUMN inferred_by_llm INTEGER DEFAULT 0`)
		g.db.Exec("INSERT INTO schema_version (version) VALUES (23)")
		log.Println("[graph] Migration to v23 completed: episode_edges.inferred_by_llm")
	}

	// Migration v24: Add event_time to engrams — the MAX(timestamp_event) of source episodes,
	// representing when the underlying events occurred (not when consolidation ran).
	if version < 24 {
		g.db.Exec(`ALTER TABLE engrams ADD COLUMN event_time DATETIME`)
		// Backfill from source episodes where available.
		g.db.Exec(`
			UPDATE engrams
			SET event_time = (
				SELECT MAX(e.timestamp_event)
				FROM episodes e
				JOIN engram_episodes ee ON ee.episode_id = e.id
				WHERE ee.engram_id = engrams.id
			)
		`)
		// Fall back to created_at for any engrams without source links.
		g.db.Exec(`UPDATE engrams SET event_time = created_at WHERE event_time IS NULL`)
		g.db.Exec("INSERT INTO schema_version (version) VALUES (24)")
		log.Println("[graph] Migration to v24 completed: engrams.event_time")
	}

	// Migration v25: Add depth column for recursive engram hierarchy.
	// depth=0: L1 engrams consolidated from episodes (current default).
	// depth=1: L2 engrams consolidated from L1 engrams.
	// depth=N: LN+1 engrams consolidated from LN engrams.
	if version < 25 {
		g.db.Exec(`ALTER TABLE engrams ADD COLUMN depth INTEGER NOT NULL DEFAULT 0`)
		g.db.Exec(`CREATE INDEX IF NOT EXISTS idx_engrams_depth ON engrams(depth)`)
		g.db.Exec("INSERT INTO schema_version (version) VALUES (25)")
		log.Println("[graph] Migration to v25 completed: engrams.depth column added")
	}

	// Migration v26: Schema versioning for engram reconsolidation.
	// - updated_at tracks when an engram was last reconsolidated.
	// - engram_schema_instances is an audit trail of summary history.
	if version < 26 {
		stmts := []string{
			`ALTER TABLE engrams ADD COLUMN updated_at DATETIME`,
			// Backfill: treat last_accessed as the last update time for existing engrams.
			`UPDATE engrams SET updated_at = last_accessed WHERE updated_at IS NULL`,
			`CREATE TABLE IF NOT EXISTS engram_schema_instances (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				engram_id  TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				summary    TEXT NOT NULL,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_schema_instances_engram ON engram_schema_instances(engram_id)`,
		}
		for _, sql := range stmts {
			if _, err := g.db.Exec(sql); err != nil {
				log.Printf("[graph] Migration v26 (non-fatal): %v", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (26)")
		log.Println("[graph] Migration to v26 completed: engram schema versioning")
	}

	// Migration v27: Schema Formation (Phase 2).
	// Adds schemas, schema_instances, and schema_annotations tables.
	// Schemas are cross-cutting pattern templates extracted from L2+ engrams.
	if version < 27 {
		stmts := []string{
			// Schemas: cross-cutting pattern templates
			`CREATE TABLE IF NOT EXISTS schemas (
				id          TEXT PRIMARY KEY,
				name        TEXT NOT NULL,
				content     TEXT NOT NULL,
				embedding   BLOB,
				is_labile   INTEGER DEFAULT 0,
				created_at  DATETIME NOT NULL,
				updated_at  DATETIME NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_schemas_name ON schemas(name)`,
			`CREATE INDEX IF NOT EXISTS idx_schemas_updated ON schemas(updated_at)`,

			// Schema instances: engram → schema match records
			`CREATE TABLE IF NOT EXISTS schema_instances (
				schema_id   TEXT NOT NULL REFERENCES schemas(id) ON DELETE CASCADE,
				engram_id   TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				slot_values TEXT,
				is_anomaly  INTEGER DEFAULT 0,
				matched_at  DATETIME NOT NULL,
				PRIMARY KEY (schema_id, engram_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_schema_instances_schema ON schema_instances(schema_id)`,
			`CREATE INDEX IF NOT EXISTS idx_schema_instances_engram ON schema_instances(engram_id)`,

			// Schema annotations: denormalized engram → schema for fast lookup
			`CREATE TABLE IF NOT EXISTS schema_annotations (
				engram_id   TEXT NOT NULL REFERENCES engrams(id) ON DELETE CASCADE,
				schema_id   TEXT NOT NULL REFERENCES schemas(id) ON DELETE CASCADE,
				PRIMARY KEY (engram_id, schema_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_schema_annotations_engram ON schema_annotations(engram_id)`,
			`CREATE INDEX IF NOT EXISTS idx_schema_annotations_schema ON schema_annotations(schema_id)`,
		}
		for _, sql := range stmts {
			if _, err := g.db.Exec(sql); err != nil {
				log.Printf("[graph] Migration v27 error: %v", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (27)")
		log.Println("[graph] Migration to v27 completed: schema formation tables added")
	}

	// v28: Schema summaries (precomputed pyramid compression levels for schemas).
	// Mirrors engram_summaries and entity_summaries — avoids on-the-fly formatting
	// in the hot path when schemas are surfaced during context assembly.
	if version < 28 {
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS schema_summaries (
				id               INTEGER PRIMARY KEY AUTOINCREMENT,
				schema_id        TEXT NOT NULL REFERENCES schemas(id) ON DELETE CASCADE,
				compression_level INTEGER NOT NULL,
				summary          TEXT NOT NULL,
				tokens           INTEGER NOT NULL,
				created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
				UNIQUE(schema_id, compression_level)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_schema_summaries_schema ON schema_summaries(schema_id)`,
			`CREATE INDEX IF NOT EXISTS idx_schema_summaries_level ON schema_summaries(compression_level)`,
		}
		for _, sql := range stmts {
			if _, err := g.db.Exec(sql); err != nil {
				log.Printf("[graph] Migration v28 error: %v", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (28)")
		log.Println("[graph] Migration to v28 completed: schema_summaries table added")
	}

	// v29: access_count column for base-level activation bias.
	// Tracks how many times each engram has been retrieved; used with last_accessed
	// to compute recency-frequency score in SpreadActivation seed weighting.
	if version < 29 {
		stmts := []string{
			`ALTER TABLE engrams ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0`,
			`CREATE INDEX IF NOT EXISTS idx_engrams_access_count ON engrams(access_count)`,
		}
		for _, sql := range stmts {
			if _, err := g.db.Exec(sql); err != nil {
				log.Printf("[graph] Migration v29 error: %v", err)
			}
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (29)")
		log.Println("[graph] Migration to v29 completed: access_count column added")
	}

	// v30: Add attachments column to episodes for storing Discord CDN URLs.
	// Stores a JSON array of {filename, content_type, url} objects so attachment URLs
	// survive pyramid compression and can be retrieved via query_episode.
	if version < 30 {
		if _, err := g.db.Exec(`ALTER TABLE episodes ADD COLUMN attachments TEXT`); err != nil {
			log.Printf("[graph] Migration v30 error: %v", err)
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (30)")
		log.Println("[graph] Migration to v30 completed: attachments column added to episodes")
	}

	// v31: Multi-DB split — move embeddings to memory-vectors.db and summaries to memory-cache.db.
	// Data is copied to the attached schemas; then embedding columns and summary tables are dropped
	// from main. The engram_vec virtual table is dropped from main (it will live in vectors schema).
	// This migration is idempotent: it checks for the presence of each column/table before acting.
	if version < 31 {
		log.Println("[graph] Migrating to schema v31: multi-DB split (embeddings → vectors, summaries → cache)")
		if err := g.runMultiDBMigration(); err != nil {
			log.Printf("[graph] Migration v31 warning: %v — continuing", err)
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (31)")
		log.Println("[graph] Migration to v31 completed: multi-DB split")
	}

	// v32: Add quality score columns for executive memory feedback loop.
	// quality: EMA score (0–1), default 0.5 (neutral). Multiplied by activation for ranking.
	// quality_ratings: count of times this engram has been rated; tracks signal volume.
	if version < 32 {
		stmts := []string{
			`ALTER TABLE engrams ADD COLUMN quality REAL NOT NULL DEFAULT 0.5`,
			`ALTER TABLE engrams ADD COLUMN quality_ratings INT NOT NULL DEFAULT 0`,
			`CREATE INDEX IF NOT EXISTS idx_engrams_quality ON engrams(quality)`,
		}
		for _, sql := range stmts {
			// Ignore errors for columns that already exist
			g.db.Exec(sql)
		}
		g.db.Exec("INSERT INTO schema_version (version) VALUES (32)")
		log.Println("[graph] Migration to v32 completed: quality + quality_ratings columns added")
	}

	return nil
}

// columnExistsInMain checks whether a column exists in a table in the main schema.
func (g *DB) columnExistsInMain(table, col string) bool {
	rows, err := g.db.Query(fmt.Sprintf("PRAGMA main.table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err == nil && name == col {
			return true
		}
	}
	return false
}

// tableExistsInMain checks whether a table exists in the main schema.
func (g *DB) tableExistsInMain(table string) bool {
	var count int
	err := g.db.QueryRow(
		`SELECT COUNT(*) FROM main.sqlite_master WHERE type='table' AND name=?`, table,
	).Scan(&count)
	return err == nil && count > 0
}

// runMultiDBMigration performs the v31 data migration:
// copies embeddings from main→vectors schema, summaries from main→cache schema,
// then drops those columns/tables from main. Idempotent (checks column/table existence).
func (g *DB) runMultiDBMigration() error {
	columnExists := g.columnExistsInMain
	tableExists := g.tableExistsInMain

	// 1. Copy embeddings to vectors schema (via the ATTACHed connection)
	if columnExists("engrams", "embedding") {
		if _, err := g.db.Exec(`
			INSERT OR IGNORE INTO vectors.engram_embeddings (id, embedding)
			SELECT id, embedding FROM main.engrams WHERE embedding IS NOT NULL
		`); err != nil {
			log.Printf("[graph] v31: copy engram embeddings: %v", err)
		} else {
			log.Println("[graph] v31: engram embeddings copied to vectors DB")
		}
	}
	if columnExists("episodes", "embedding") {
		if _, err := g.db.Exec(`
			INSERT OR IGNORE INTO vectors.episode_embeddings (id, embedding)
			SELECT id, embedding FROM main.episodes WHERE embedding IS NOT NULL
		`); err != nil {
			log.Printf("[graph] v31: copy episode embeddings: %v", err)
		} else {
			log.Println("[graph] v31: episode embeddings copied to vectors DB")
		}
	}
	if columnExists("schemas", "embedding") {
		if _, err := g.db.Exec(`
			INSERT OR IGNORE INTO vectors.schema_embeddings (id, embedding)
			SELECT id, embedding FROM main.schemas WHERE embedding IS NOT NULL
		`); err != nil {
			log.Printf("[graph] v31: copy schema embeddings: %v", err)
		} else {
			log.Println("[graph] v31: schema embeddings copied to vectors DB")
		}
	}

	// 2. Copy summaries to cache schema (via the ATTACHed connection)
	if tableExists("episode_summaries") {
		if _, err := g.db.Exec(`
			INSERT OR IGNORE INTO cache.episode_summaries (episode_id, compression_level, summary, tokens, created_at)
			SELECT episode_id, compression_level, summary, tokens, created_at FROM main.episode_summaries
		`); err != nil {
			log.Printf("[graph] v31: copy episode_summaries: %v", err)
		} else {
			log.Println("[graph] v31: episode_summaries copied to cache DB")
		}
	}
	if tableExists("engram_summaries") {
		if _, err := g.db.Exec(`
			INSERT OR IGNORE INTO cache.engram_summaries (engram_id, compression_level, summary, tokens, created_at)
			SELECT engram_id, compression_level, summary, tokens, created_at FROM main.engram_summaries
		`); err != nil {
			log.Printf("[graph] v31: copy engram_summaries: %v", err)
		} else {
			log.Println("[graph] v31: engram_summaries copied to cache DB")
		}
	}
	if tableExists("schema_summaries") {
		if _, err := g.db.Exec(`
			INSERT OR IGNORE INTO cache.schema_summaries (schema_id, compression_level, summary, tokens, created_at)
			SELECT schema_id, compression_level, summary, tokens, created_at FROM main.schema_summaries
		`); err != nil {
			log.Printf("[graph] v31: copy schema_summaries: %v", err)
		} else {
			log.Println("[graph] v31: schema_summaries copied to cache DB")
		}
	}

	// 3. Drop FTS triggers that reference engram_summaries in main (they'd reference a dropped table)
	for _, trigger := range []string{"engram_summaries_ai", "engram_summaries_au", "engram_summaries_ad"} {
		g.db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s", trigger))
	}

	// 4. Drop summary tables from main
	for _, tbl := range []string{"episode_summaries", "engram_summaries", "schema_summaries"} {
		if tableExists(tbl) {
			if _, err := g.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", tbl)); err != nil {
				log.Printf("[graph] v31: drop %s: %v", tbl, err)
			}
		}
	}

	// 5. Drop engram_vec from main (will be recreated in vectors schema)
	g.db.Exec(`DROP TABLE IF EXISTS engram_vec`)

	// 6. NULL out embedding columns in main to reclaim space (data is now in vectors DB).
	// Queries that previously read main.engrams.embedding now use vectors.engram_embeddings.
	if columnExists("engrams", "embedding") {
		g.db.Exec(`UPDATE engrams SET embedding = NULL WHERE embedding IS NOT NULL`)
		log.Println("[graph] v31: nulled engrams.embedding in main DB")
	}
	if columnExists("episodes", "embedding") {
		g.db.Exec(`UPDATE episodes SET embedding = NULL WHERE embedding IS NOT NULL`)
		log.Println("[graph] v31: nulled episodes.embedding in main DB")
	}
	if columnExists("schemas", "embedding") {
		g.db.Exec(`UPDATE schemas SET embedding = NULL WHERE embedding IS NOT NULL`)
		log.Println("[graph] v31: nulled schemas.embedding in main DB")
	}

	// VACUUM to reclaim freed pages (embedding BLOBs are ~130MB).
	log.Println("[graph] v31: running VACUUM to reclaim embedding space (this may take a moment)...")
	g.db.Exec(`VACUUM`)

	log.Println("[graph] v31: multi-DB split migration complete")
	return nil
}

// initVecTableFromEngrams reads the embedding dimension from existing engrams, creates the
// engram_vec virtual table with that dimension (if it doesn't already exist), and backfills
// all existing engram embeddings. No-ops if no engrams with embeddings exist yet.
// After v31, embeddings live in the vectors.engram_embeddings table; the vec table is in vectors schema.
func (g *DB) initVecTableFromEngrams() error {
	if g.vectors == nil {
		return nil
	}
	// Try to read dimension from engram_embeddings in vectors DB first (post-v31)
	var embBytes []byte
	err := g.vectors.QueryRow(`SELECT embedding FROM engram_embeddings WHERE embedding IS NOT NULL AND LENGTH(embedding) > 4 LIMIT 1`).Scan(&embBytes)
	if err != nil {
		// Fall back to main.engrams.embedding (pre-v31 or during migration)
		err = g.db.QueryRow(`SELECT embedding FROM engrams WHERE embedding IS NOT NULL AND LENGTH(embedding) > 4 LIMIT 1`).Scan(&embBytes)
		if err != nil {
			return nil // no engrams with embeddings yet; defer to first AddEngram
		}
	}
	var emb64 []float64
	if err := json.Unmarshal(embBytes, &emb64); err != nil || len(emb64) == 0 {
		return nil
	}
	return g.ensureVecTable(len(emb64))
}

// ensureVecTable creates the engram_vec virtual table in the vectors schema for the given
// embedding dimension (if not yet created) and backfills all existing engrams.
// Idempotent for the same dim.
//
// Schema uses integer rowid (from the engrams table) + auxiliary +engram_id column,
// avoiding vec0's TEXT PRIMARY KEY partitioning behaviour which breaks KNN queries.
func (g *DB) ensureVecTable(dim int) error {
	if g.vecDim == dim {
		return nil // already set up for this dimension
	}
	if g.vecDim != 0 && g.vecDim != dim {
		return fmt.Errorf("embedding dim %d doesn't match vec table dim %d", dim, g.vecDim)
	}

	// Create engram_vec in the vectors attached schema (accessible via unqualified name after ATTACH)
	_, err := g.db.Exec(fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS vectors.engram_vec USING vec0(
			embedding float[%d],
			+engram_id TEXT
		)
	`, dim))
	if err != nil {
		return fmt.Errorf("failed to create vectors.engram_vec(float[%d]): %w", dim, err)
	}
	g.vecDim = dim

	// Backfill from engram_embeddings in vectors schema (post-v31 layout).
	// The rowid must match main.engrams.rowid for KNN to work — join via engram_id.
	rows, err := g.db.Query(`
		SELECT e.rowid, e.id, ee.embedding
		FROM main.engrams e
		JOIN vectors.engram_embeddings ee ON ee.id = e.id
		WHERE ee.embedding IS NOT NULL
	`)
	if err != nil {
		// Fall back: try main.engrams.embedding (pre-v31)
		rows, err = g.db.Query(`SELECT rowid, id, embedding FROM engrams WHERE embedding IS NOT NULL`)
		if err != nil {
			return nil // backfill failure is non-fatal
		}
	}
	// Collect all rows into memory before closing — required because g.db has
	// MaxOpenConns(1), so calling g.db.Begin() while rows holds the connection
	// would deadlock (Begin blocks waiting for the connection rows already owns).
	type engramRow struct {
		rowid int64
		id    string
		emb   []byte
	}
	var collected []engramRow
	for rows.Next() {
		var r engramRow
		if err := rows.Scan(&r.rowid, &r.id, &r.emb); err != nil {
			continue
		}
		collected = append(collected, r)
	}
	rows.Close()

	tx, err := g.db.Begin()
	if err != nil {
		return nil
	}

	var count int
	for _, r := range collected {
		var emb64 []float64
		if err := json.Unmarshal(r.emb, &emb64); err != nil || len(emb64) != dim {
			continue
		}
		emb32 := normalizeFloat32(float64ToFloat32(emb64))
		serialized, serErr := sqlite_vec.SerializeFloat32(emb32)
		if serErr != nil {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM vectors.engram_vec WHERE rowid = ?`, r.rowid); err != nil {
			log.Printf("[graph] vec backfill delete failed for %s: %v", r.id, err)
			continue
		}
		if _, err := tx.Exec(`INSERT INTO vectors.engram_vec(rowid, embedding, engram_id) VALUES (?, ?, ?)`, r.rowid, serialized, r.id); err != nil {
			log.Printf("[graph] vec backfill failed for %s: %v", r.id, err)
			continue
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return nil
	}
	if count > 0 {
		log.Printf("[graph] vec backfill: indexed %d engrams (dim=%d)", count, dim)
	}
	return nil
}

// float64ToFloat32 converts a float64 slice to float32
func float64ToFloat32(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

// normalizeFloat32 returns a unit-length copy of the vector.
// Normalizing before storing in vec0 makes L2 distance equivalent to cosine distance:
//   cosine_dist = L2_dist² / 2   (for unit vectors)
//   L2_threshold = sqrt(2 * cosine_dist_threshold)
func normalizeFloat32(v []float32) []float32 {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return v
	}
	norm = math.Sqrt(norm)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}

// cosineDistToL2 converts a cosine distance threshold to an L2 distance threshold
// for unit-normalized vectors: L2_threshold = sqrt(2 * cosine_dist_threshold)
func cosineDistToL2(cosineDist float64) float64 {
	return math.Sqrt(2.0 * cosineDist)
}

// l2ToCosineSim converts an L2 distance (on normalized vectors) to cosine similarity:
// cosine_sim = 1 - L2²/2
func l2ToCosineSim(l2dist float64) float64 {
	return 1.0 - (l2dist*l2dist)/2.0
}

// Stats returns database statistics
func (g *DB) Stats() (map[string]int, error) {
	stats := make(map[string]int)

	tables := []string{"episodes", "episode_summaries", "entities", "engrams", "engram_episodes", "episode_edges", "entity_relations", "engram_relations"}
	for _, table := range tables {
		var count int
		err := g.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
		if err != nil {
			stats[table] = 0 // table may not exist yet on fresh DB before migration
		} else {
			stats[table] = count
		}
	}

	return stats, nil
}

// Clear removes all data (for testing/reset)
func (g *DB) Clear() error {
	tables := []string{
		"engram_relations", "engram_entities", "engram_episodes", "engrams",
		"entity_relations", "episode_mentions", "entity_aliases", "entities",
		"episode_edges", "episode_summaries", "episodes",
	}

	for _, table := range tables {
		if _, err := g.db.Exec(fmt.Sprintf("DELETE FROM %s", table)); err != nil {
			return fmt.Errorf("failed to clear %s: %w", table, err)
		}
	}

	return nil
}

// populateEngramRelations computes pairwise similarity for all engrams and creates
// SIMILAR_TO edges for pairs above the given threshold.
func (g *DB) populateEngramRelations(threshold float64) error {
	rows, err := g.db.Query(`SELECT id, embedding FROM engrams WHERE embedding IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("failed to query engrams: %w", err)
	}
	defer rows.Close()

	type engramEmb struct {
		id        string
		embedding []float64
	}
	var engrams []engramEmb

	for rows.Next() {
		var id string
		var embBytes []byte
		if err := rows.Scan(&id, &embBytes); err != nil {
			continue
		}
		var embedding []float64
		if err := json.Unmarshal(embBytes, &embedding); err != nil {
			continue
		}
		engrams = append(engrams, engramEmb{id: id, embedding: embedding})
	}

	if len(engrams) < 2 {
		return nil
	}

	var edgesAdded int
	for i := 0; i < len(engrams); i++ {
		for j := i + 1; j < len(engrams); j++ {
			sim := cosineSim(engrams[i].embedding, engrams[j].embedding)
			if sim >= threshold {
				err := g.AddEngramRelation(engrams[i].id, engrams[j].id, EdgeSimilarTo, sim)
				if err == nil {
					edgesAdded++
				}
			}
		}
	}

	fmt.Printf("[migration] Populated engram_relations: %d SIMILAR_TO edges (threshold %.2f, %d engrams)\n",
		edgesAdded, threshold, len(engrams))
	return nil
}

// cosineSim computes cosine similarity between two embeddings
func cosineSim(a, b []float64) float64 {
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

// parseTimestamp parses a SQLite datetime string into time.Time.
// Used when scanning aggregate columns (e.g. MAX(timestamp_event)) whose
// declared column type is lost, preventing the driver from auto-converting.
var timestampFormats = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range timestampFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp format: %q", s)
}
