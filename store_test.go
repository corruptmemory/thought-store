package main

import (
	"context"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable")
	if err != nil {
		t.Skip("Postgres not available, skipping integration test")
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Create a fake 1024-dim embedding
	embedding := make([]float32, 1024)
	embedding[0] = 0.5
	embedding[1] = -0.3

	thought, err := store.Insert(ctx, "test thought about fixing auth", embedding, "test")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if thought.Content != "test thought about fixing auth" {
		t.Fatalf("content mismatch: %q", thought.Content)
	}
	if thought.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// List
	thoughts, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(thoughts) == 0 {
		t.Fatal("expected at least one thought")
	}

	// Search with same embedding should find it
	results, err := store.Search(ctx, embedding, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}
	if results[0].Content != "test thought about fixing auth" {
		t.Fatalf("search result mismatch: %q", results[0].Content)
	}

	// Cleanup
	_, err = store.pool.Exec(ctx, "DELETE FROM thoughts WHERE source = 'test'")
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
