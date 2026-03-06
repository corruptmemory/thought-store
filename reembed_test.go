package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReEmbed(t *testing.T) {
	ollamaAvailable(t)

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "reembed.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	instanceID, err := store.EnsureInstance(ctx, "test", "reembed test")
	if err != nil {
		t.Fatalf("EnsureInstance: %v", err)
	}

	modelID, err := store.EnsureModel(ctx, "mxbai-embed-large", "", 1024)
	if err != nil {
		t.Fatalf("EnsureModel: %v", err)
	}
	if err := store.SetActiveModel(ctx, modelID); err != nil {
		t.Fatalf("SetActiveModel: %v", err)
	}

	ollama := NewOllamaClient("http://localhost:11434", "mxbai-embed-large")

	// Insert a test thought via the normal path
	embedding, err := ollama.Embed(ctx, "re-embed test: concurrency is not parallelism")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	thought, err := store.Insert(ctx, "re-embed test: concurrency is not parallelism", embedding, "test", instanceID, modelID)
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

	// UpdateEmbeddings with modelID
	err = store.UpdateEmbeddings(ctx, []string{thought.ID}, embeddings, modelID)
	if err != nil {
		t.Fatalf("UpdateEmbeddings: %v", err)
	}

	// Verify via LoadEmbeddings
	ids, vectors, err := store.LoadEmbeddings(ctx, modelID)
	if err != nil {
		t.Fatalf("LoadEmbeddings: %v", err)
	}
	found = false
	for i, id := range ids {
		if id == thought.ID {
			found = true
			if len(vectors[i]) != 1024 {
				t.Fatalf("expected 1024-dim vector, got %d", len(vectors[i]))
			}
			break
		}
	}
	if !found {
		t.Fatal("expected to find thought embedding after re-embed")
	}
}
