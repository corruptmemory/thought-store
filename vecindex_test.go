package main

import (
	"math"
	"testing"
)

func TestVecIndexSearch(t *testing.T) {
	idx := NewVecIndex()

	// Three 4-dim vectors
	idx.Load(
		[]string{"a", "b", "c"},
		[][]float32{
			{1, 0, 0, 0},
			{0, 1, 0, 0},
			{0.9, 0.1, 0, 0}, // most similar to {1,0,0,0}
		},
	)

	results := idx.Search([]float32{1, 0, 0, 0}, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First result should be "a" (exact match)
	if results[0].ID != "a" {
		t.Fatalf("expected 'a' first, got %q", results[0].ID)
	}
	if math.Abs(results[0].Similarity-1.0) > 0.001 {
		t.Fatalf("expected similarity ~1.0, got %f", results[0].Similarity)
	}

	// Second should be "c" (close match)
	if results[1].ID != "c" {
		t.Fatalf("expected 'c' second, got %q", results[1].ID)
	}
}

func TestVecIndexAdd(t *testing.T) {
	idx := NewVecIndex()
	idx.Add("x", []float32{1, 0, 0})
	idx.Add("y", []float32{0, 1, 0})

	if idx.Len() != 2 {
		t.Fatalf("expected 2, got %d", idx.Len())
	}

	results := idx.Search([]float32{1, 0, 0}, 1)
	if results[0].ID != "x" {
		t.Fatalf("expected 'x', got %q", results[0].ID)
	}
}

func TestVecIndexEmpty(t *testing.T) {
	idx := NewVecIndex()
	results := idx.Search([]float32{1, 0, 0}, 5)
	if len(results) != 0 {
		t.Fatalf("expected 0 results from empty index, got %d", len(results))
	}
}
