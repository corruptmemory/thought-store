package main

import "time"

// ThoughtState represents the lifecycle state of a thought.
// IMPORTANT: These integer values are stored in the database.
// Order must be preserved forever. You can only add new values, never delete or reorder.
type ThoughtState int

const (
	StateActive    ThoughtState = iota // default; visible in search and display
	StateRefined                       // rolled up into a summary thought
	StateForgotten                     // soft-deleted; hidden from search and display
)

func (s ThoughtState) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateRefined:
		return "refined"
	case StateForgotten:
		return "forgotten"
	default:
		return "unknown"
	}
}

// MarshalText implements encoding.TextMarshaler so JSON output is "active"/"refined"/"forgotten".
func (s ThoughtState) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler for JSON round-tripping.
func (s *ThoughtState) UnmarshalText(text []byte) error {
	switch string(text) {
	case "active":
		*s = StateActive
	case "refined":
		*s = StateRefined
	case "forgotten":
		*s = StateForgotten
	default:
		*s = StateActive
	}
	return nil
}

// MetadataType classifies a metadata entry.
// IMPORTANT: These integer values are stored in the database.
// Order must be preserved forever. You can only add new values, never delete or reorder.
type MetadataType int

const (
	MetadataPerson     MetadataType = iota // 0 — person mentioned
	MetadataDate                           // 1 — date referenced (YYYY-MM-DD)
	MetadataTag                            // 2 — topic tag
	MetadataActionItem                     // 3 — implied to-do
)

func (m MetadataType) String() string {
	switch m {
	case MetadataPerson:
		return "person"
	case MetadataDate:
		return "date"
	case MetadataTag:
		return "tag"
	case MetadataActionItem:
		return "action_item"
	default:
		return "unknown"
	}
}

func (m MetadataType) MarshalText() ([]byte, error) {
	return []byte(m.String()), nil
}

func (m *MetadataType) UnmarshalText(text []byte) error {
	switch string(text) {
	case "person":
		*m = MetadataPerson
	case "date":
		*m = MetadataDate
	case "tag":
		*m = MetadataTag
	case "action_item":
		*m = MetadataActionItem
	default:
		*m = MetadataPerson
	}
	return nil
}

// ThoughtMetadataEntry is a single metadata tag on a thought.
type ThoughtMetadataEntry struct {
	Type  MetadataType `json:"type"`
	Value string       `json:"value"`
}

// Thought represents a single captured thought.
type Thought struct {
	ID        string                 `json:"id"`
	Content   string                 `json:"content"`
	Source    string                 `json:"source"`
	State    ThoughtState           `json:"state"`
	Metadata []ThoughtMetadataEntry `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

// SearchResult is a Thought with a similarity score from vector search.
type SearchResult struct {
	Thought
	Similarity float64 `json:"similarity"`
}

// StoreStats holds aggregate statistics about stored thoughts.
type StoreStats struct {
	Total    int            `json:"total"`
	OldestAt *time.Time     `json:"oldest_at,omitempty"`
	NewestAt *time.Time     `json:"newest_at,omitempty"`
	BySource map[string]int `json:"by_source"`
}

// ThoughtRow is a minimal row for re-embedding (no vector data loaded).
type ThoughtRow struct {
	ID      string
	Content string
}
