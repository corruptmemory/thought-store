package main

import (
	"context"
	"testing"
)

func TestReEmbed(t *testing.T) {
	ollamaAvailable(t)

	ctx := context.Background()
	store, err := NewStore(ctx, "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable")
	if err != nil {
		t.Skip("Postgres not available")
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ollama := NewOllamaClient("http://localhost:11434", "mxbai-embed-large")

	// Insert a test thought via the normal path
	embedding, err := ollama.Embed(ctx, "re-embed test: concurrency is not parallelism")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	thought, err := store.Insert(ctx, "re-embed test: concurrency is not parallelism", embedding, "test")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// ListAllContent should include our thought
	rows, err := store.ListAllContent(ctx)
	if err != nil {
		t.Fatalf("ListAllContent: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == thought.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected to find test thought in ListAllContent")
	}

	// EmbedBatch
	embeddings, err := ollama.EmbedBatch(ctx, []string{"re-embed test: concurrency is not parallelism"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(embeddings))
	}
	if len(embeddings[0]) != 1024 {
		t.Fatalf("expected 1024 dims, got %d", len(embeddings[0]))
	}

	// UpdateEmbeddings
	err = store.UpdateEmbeddings(ctx, []string{thought.ID}, embeddings)
	if err != nil {
		t.Fatalf("UpdateEmbeddings: %v", err)
	}

	// Verify the thought is still searchable after re-embed
	results, err := store.Search(ctx, embeddings[0], 5)
	if err != nil {
		t.Fatalf("Search after re-embed: %v", err)
	}
	found = false
	for _, r := range results {
		if r.ID == thought.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected test thought in search results after re-embed")
	}

	// Cleanup
	store.pool.Exec(ctx, "DELETE FROM thoughts WHERE id = $1", thought.ID)
}
