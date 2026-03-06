package main

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

func TestSQLiteStoreRoundTrip(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	instanceID, err := store.EnsureInstance(ctx, "test-machine", "test instance")
	if err != nil {
		t.Fatalf("EnsureInstance: %v", err)
	}

	modelID, err := store.EnsureModel(ctx, "test-model", "", 4)
	if err != nil {
		t.Fatalf("EnsureModel: %v", err)
	}
	if err := store.SetActiveModel(ctx, modelID); err != nil {
		t.Fatalf("SetActiveModel: %v", err)
	}

	// Insert
	embedding := []float32{0.5, -0.3, 0.1, 0.9}
	thought, err := store.Insert(ctx, "test thought about auth", embedding, "cli", instanceID, modelID)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if thought.Content != "test thought about auth" {
		t.Fatalf("content mismatch: %q", thought.Content)
	}
	if thought.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if thought.Source != "cli" {
		t.Fatalf("source mismatch: %q", thought.Source)
	}

	// List
	thoughts, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(thoughts) == 0 {
		t.Fatal("expected at least one thought")
	}

	// Stats
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total == 0 {
		t.Fatal("expected non-zero total")
	}
	if stats.BySource["cli"] == 0 {
		t.Fatal("expected cli source count")
	}

	// LoadEmbeddings
	ids, vectors, err := store.LoadEmbeddings(ctx, modelID)
	if err != nil {
		t.Fatalf("LoadEmbeddings: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("expected embeddings")
	}
	if len(vectors[0]) != 4 {
		t.Fatalf("expected 4-dim vector, got %d", len(vectors[0]))
	}
	if vectors[0][0] != 0.5 {
		t.Fatalf("vector mismatch: %v", vectors[0])
	}

	// ListAllContent
	rows, err := store.ListAllContent(ctx)
	if err != nil {
		t.Fatalf("ListAllContent: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected content rows")
	}

	// Diagnostics
	diag := store.Diagnostics(ctx)
	if diag.Version == "" {
		t.Fatal("expected sqlite version")
	}
	if diag.ThoughtCount == 0 {
		t.Fatal("expected non-zero thought count")
	}
}

func TestSQLiteEncodeDecodeVector(t *testing.T) {
	original := []float32{1.0, -2.5, 3.14, 0.0, -0.001}
	encoded := encodeVector(original)
	decoded := decodeVector(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: %d vs %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Fatalf("mismatch at %d: %f vs %f", i, decoded[i], original[i])
		}
	}
}

func TestSQLiteEnsureInstanceIdempotent(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	id1, err := store.EnsureInstance(ctx, "laptop", "Jim's laptop")
	if err != nil {
		t.Fatalf("first EnsureInstance: %v", err)
	}

	id2, err := store.EnsureInstance(ctx, "laptop", "Jim's laptop")
	if err != nil {
		t.Fatalf("second EnsureInstance: %v", err)
	}

	if id1 != id2 {
		t.Fatalf("expected same ID, got %d and %d", id1, id2)
	}
}
