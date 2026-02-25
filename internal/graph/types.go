package graph

import (
	"time"
)

// EdgeType defines the type of relationship between nodes
type EdgeType string

const (
	// Episode edges
	EdgeRepliesTo EdgeType = "REPLIES_TO"
	EdgeFollows   EdgeType = "FOLLOWS"

	// Entity edges (structural)
	EdgeSameAs    EdgeType = "SAME_AS"
	EdgeRelatedTo EdgeType = "RELATED_TO"
	EdgeMentions  EdgeType = "MENTIONS"

	// Entity edges (meta-relationships)
	EdgeAffiliatedWith EdgeType = "AFFILIATED_WITH" // Professional: works_at, works_on, part_of, studied_at, cofounder_of
	EdgeKinOf          EdgeType = "KIN_OF"          // Family: married_to, sibling_of, parent_of, child_of
	EdgeKnows          EdgeType = "KNOWS"           // Social: friend_of, met_at
	EdgeLocatedIn      EdgeType = "LOCATED_IN"      // Spatial: lives_in, located_in
	EdgeHas            EdgeType = "HAS"             // Possession/attribute: owner_of, has_email, has_pet, prefers, allergic_to

	// Legacy edge types (kept for backward compatibility with existing data)
	EdgeWorksAt     EdgeType = "WORKS_AT"
	EdgeLivesIn     EdgeType = "LIVES_IN"
	EdgeMarriedTo   EdgeType = "MARRIED_TO"
	EdgeSiblingOf   EdgeType = "SIBLING_OF"
	EdgeParentOf    EdgeType = "PARENT_OF"
	EdgeChildOf     EdgeType = "CHILD_OF"
	EdgeFriendOf    EdgeType = "FRIEND_OF"
	EdgeWorksOn     EdgeType = "WORKS_ON"
	EdgePartOf      EdgeType = "PART_OF"
	EdgeStudiedAt   EdgeType = "STUDIED_AT"
	EdgeMetAt       EdgeType = "MET_AT"
	EdgeCofounderOf EdgeType = "COFOUNDER_OF"
	EdgeOwnerOf     EdgeType = "OWNER_OF"
	EdgeHasEmail    EdgeType = "HAS_EMAIL"
	EdgePrefers     EdgeType = "PREFERS"
	EdgeAllergicTo  EdgeType = "ALLERGIC_TO"
	EdgeHasPet      EdgeType = "HAS_PET"

	// Engram edges
	EdgeSourcedFrom       EdgeType = "SOURCED_FROM"
	EdgeInvolves          EdgeType = "INVOLVES"
	EdgeInvalidatedBy     EdgeType = "INVALIDATED_BY"
	EdgeSharedEntity      EdgeType = "SHARED_ENTITY"
	EdgeSimilarTo         EdgeType = "SIMILAR_TO"         // Semantic similarity above threshold (0.85+)
	EdgeConsolidatedFrom  EdgeType = "CONSOLIDATED_FROM"  // Higher-depth engram → source engrams
)

// EntityType defines categories of entities (OntoNotes-compatible schema)
type EntityType string

const (
	// Core entity types (OntoNotes)
	EntityPerson    EntityType = "PERSON"      // People, including fictional
	EntityOrg       EntityType = "ORG"         // Organizations
	EntityGPE       EntityType = "GPE"         // Geopolitical entities (countries, cities, states)
	EntityLoc       EntityType = "LOC"         // Non-GPE locations (mountains, bodies of water)
	EntityFac       EntityType = "FAC"         // Facilities (buildings, airports, highways)
	EntityProduct   EntityType = "PRODUCT"     // Products (vehicles, weapons, foods)
	EntityEvent     EntityType = "EVENT"       // Named events (hurricanes, battles, wars)
	EntityWorkOfArt EntityType = "WORK_OF_ART" // Titles of books, songs, etc.
	EntityLaw       EntityType = "LAW"         // Named documents made into laws
	EntityLanguage  EntityType = "LANGUAGE"    // Named languages
	EntityNorp      EntityType = "NORP"        // Nationalities, religious or political groups

	// Numeric/temporal types (OntoNotes)
	EntityDate     EntityType = "DATE"     // Absolute or relative dates
	EntityTime     EntityType = "TIME"     // Times smaller than a day
	EntityMoney    EntityType = "MONEY"    // Monetary values
	EntityPercent  EntityType = "PERCENT"  // Percentages
	EntityQuantity EntityType = "QUANTITY" // Measurements
	EntityCardinal EntityType = "CARDINAL" // Numerals not covered by other types
	EntityOrdinal  EntityType = "ORDINAL"  // "first", "second", etc.

	// Custom types (extended beyond OntoNotes)
	EntityEmail      EntityType = "EMAIL"      // Email addresses
	EntityPet        EntityType = "PET"        // Pet names
	EntityTechnology EntityType = "TECHNOLOGY" // Software, frameworks, AI models, developer tools

	// Fallback
	EntityOther EntityType = "OTHER" // Unknown or unclassified
)

// Episode represents a raw message in the memory graph (Tier 1)
type Episode struct {
	ID                   string    `json:"id"`
	Content              string    `json:"content"`
	Level                int       `json:"level,omitempty"`    // Compression level applied (0 = original)
	TokenCount           int       `json:"token_count"`        // Pre-computed token count
	Source               string    `json:"source"`             // discord, calendar, etc.
	Author               string    `json:"author,omitempty"`
	AuthorID             string    `json:"author_id,omitempty"`
	Channel              string    `json:"channel,omitempty"`
	TimestampEvent       time.Time `json:"timestamp_event"`    // T: when it happened
	TimestampIngested    time.Time `json:"timestamp_ingested"` // T': when we learned it
	DialogueAct          string    `json:"dialogue_act,omitempty"`
	EntropyScore         float64   `json:"entropy_score,omitempty"`
	Embedding            []float64 `json:"embedding,omitempty"`
	ReplyTo              string    `json:"reply_to,omitempty"`
	AuthorizationChecked bool      `json:"authorization_checked"` // Whether authorization check has been performed
	HasAuthorization     bool      `json:"has_authorization"`     // Whether authorization was detected
	CreatedAt            time.Time `json:"created_at"`
}

// Entity represents an extracted named entity (Tier 2)
type Entity struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Type      EntityType `json:"type"`
	Salience  float64    `json:"salience"`
	Embedding []float64  `json:"embedding,omitempty"`
	Aliases   []string   `json:"aliases,omitempty"`
	Summary   string     `json:"summary,omitempty"` // Populated by pyramid level on retrieval
	Level     int        `json:"level,omitempty"`   // Compression level applied (0 = original)
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// EngramType classifies engrams for differentiated decay and retrieval behavior.
type EngramType string

const (
	EngramTypeKnowledge  EngramType = "knowledge"  // Default: facts, decisions, preferences
	EngramTypeOperational EngramType = "operational" // Meeting reminders, state syncs, deploys, restarts
)

// Engram represents a consolidated memory (Tier 3)
type Engram struct {
	ID         string     `json:"id"`
	Summary    string     `json:"summary"`
	Level      int        `json:"level,omitempty"` // Compression level applied (0 = stored summary)
	Depth      int        `json:"depth,omitempty"` // Hierarchy depth: 0 = L1 (from episodes), 1 = L2 (from L1s), etc.
	Topic      string     `json:"topic,omitempty"`
	EngramType EngramType `json:"engram_type,omitempty"`
	Activation float64    `json:"activation"`
	Strength   int        `json:"strength"`
	Embedding  []float64  `json:"embedding,omitempty"`
	EventTime    time.Time  `json:"event_time"`    // MAX(timestamp_event) of source episodes
	CreatedAt    time.Time  `json:"created_at"`
	LastAccessed time.Time  `json:"last_accessed"`
	LabileUntil  time.Time  `json:"labile_until,omitempty"`

	// Related data (populated on retrieval)
	SourceIDs []string `json:"source_ids,omitempty"`
	EntityIDs []string `json:"entity_ids,omitempty"`
	SchemaIDs []string `json:"schema_ids,omitempty"`
}

// Schema represents a cross-cutting pattern template extracted from L2+ engrams.
// A schema is not a summary of what happened — it's a template for what class of
// event this is, with generalizations extracted from multiple instances.
type Schema struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Content   string    `json:"content"`   // full semi-structured prose (PATTERN, GENERALIZATIONS, etc.)
	Embedding []float64 `json:"embedding,omitempty"`
	IsLabile  bool      `json:"is_labile,omitempty"` // true = needs reconsolidation at next induction run
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Populated on retrieval
	Instances []SchemaInstance `json:"instances,omitempty"`
}

// SchemaInstance records that an engram matches a schema, with extracted slot values.
type SchemaInstance struct {
	SchemaID   string            `json:"schema_id"`
	EngramID   string            `json:"engram_id"`
	SlotValues map[string]string `json:"slot_values,omitempty"` // JSON: {"trigger": "...", "fix": "..."}
	IsAnomaly  bool              `json:"is_anomaly"`
	MatchedAt  time.Time         `json:"matched_at"`
}

// Edge represents a relationship between nodes
type Edge struct {
	ID        int64     `json:"id,omitempty"`
	FromID    string    `json:"from_id"`
	ToID      string    `json:"to_id"`
	Type      EdgeType  `json:"type"`
	Weight    float64   `json:"weight"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Neighbor represents a node connected by an edge (for spreading activation)
type Neighbor struct {
	ID     string
	Weight float64
	Type   EdgeType
}

// ActivationResult holds spreading activation results
type ActivationResult struct {
	NodeID     string
	NodeType   string // "episode", "entity", "engram"
	Activation float64
}

// RetrievalResult holds memory retrieval results
type RetrievalResult struct {
	Engrams  []*Engram
	Episodes []*Episode
	Entities []*Entity
}

// IsLabile returns true if the engram is in its reconsolidation window
func (e *Engram) IsLabile() bool {
	if e.LabileUntil.IsZero() {
		return false
	}
	return time.Now().Before(e.LabileUntil)
}

// MakeLabile sets the engram as labile for the given duration
func (e *Engram) MakeLabile(duration time.Duration) {
	e.LabileUntil = time.Now().Add(duration)
}

// Recency returns seconds since the engram was last accessed
func (e *Engram) Recency() float64 {
	return time.Since(e.LastAccessed).Seconds()
}
