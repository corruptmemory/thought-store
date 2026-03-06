package main

import "time"

// Thought represents a single captured thought.
type Thought struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
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
