package main

import (
	"sort"
	"sync"

	"github.com/viterin/vek/vek32"
)

// VecIndex holds an in-memory vector index for fast cosine similarity search.
type VecIndex struct {
	mu      sync.RWMutex
	ids     []string    // thought unique_ids
	vectors [][]float32 // parallel to ids
}

// NewVecIndex creates an empty index.
func NewVecIndex() *VecIndex {
	return &VecIndex{}
}

// Load replaces the index contents (called on startup and after re-embed).
func (idx *VecIndex) Load(ids []string, vectors [][]float32) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.ids = ids
	idx.vectors = vectors
}

// Add appends a single entry (called on capture).
func (idx *VecIndex) Add(id string, vector []float32) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.ids = append(idx.ids, id)
	idx.vectors = append(idx.vectors, vector)
}

// VecSearchResult pairs an ID with its similarity score.
type VecSearchResult struct {
	ID         string
	Similarity float64
}

// Search returns the top-N most similar vectors to the query.
func (idx *VecIndex) Search(query []float32, limit int) []VecSearchResult {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.vectors) == 0 {
		return nil
	}

	type scored struct {
		idx        int
		similarity float64
	}

	results := make([]scored, len(idx.vectors))
	for i, v := range idx.vectors {
		results[i] = scored{idx: i, similarity: float64(vek32.CosineSimilarity(query, v))}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	if limit > len(results) {
		limit = len(results)
	}

	out := make([]VecSearchResult, limit)
	for i := 0; i < limit; i++ {
		out[i] = VecSearchResult{
			ID:         idx.ids[results[i].idx],
			Similarity: results[i].similarity,
		}
	}
	return out
}

// Len returns the number of vectors in the index.
func (idx *VecIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.ids)
}
