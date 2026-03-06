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

func TestSQLiteCheckpoint(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	instanceID, err := store.EnsureInstance(ctx, "test-machine", "")
	if err != nil {
		t.Fatalf("EnsureInstance: %v", err)
	}
	modelID, err := store.EnsureModel(ctx, "test-model", "", 4)
	if err != nil {
		t.Fatalf("EnsureModel: %v", err)
	}

	_, err = store.Insert(ctx, "checkpoint test", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instanceID, modelID)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := store.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
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

func TestSQLiteInstanceUID(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	id, err := store.EnsureInstance(ctx, "laptop", "Jim's laptop")
	if err != nil {
		t.Fatalf("EnsureInstance: %v", err)
	}

	uid, err := store.InstanceUID(ctx, id)
	if err != nil {
		t.Fatalf("InstanceUID: %v", err)
	}
	if uid == "" {
		t.Fatal("expected non-empty UID")
	}
	if len(uid) != 36 {
		t.Fatalf("expected UUID, got %q", uid)
	}
}

func TestSQLiteImportInstance(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	id, err := store.ImportInstance(ctx, "foreign-uuid-123", "desktop", "Jim's desktop")
	if err != nil {
		t.Fatalf("ImportInstance: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	id2, err := store.ImportInstance(ctx, "foreign-uuid-123", "desktop", "Jim's desktop")
	if err != nil {
		t.Fatalf("ImportInstance second: %v", err)
	}
	if id != id2 {
		t.Fatalf("expected same ID, got %d and %d", id, id2)
	}
}

func TestSQLiteImportThought(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	instanceID, err := store.ImportInstance(ctx, "foreign-uuid-456", "server", "")
	if err != nil {
		t.Fatalf("ImportInstance: %v", err)
	}
	modelID, err := store.EnsureModel(ctx, "test-model", "", 4)
	if err != nil {
		t.Fatalf("EnsureModel: %v", err)
	}

	embedding := []float32{0.1, 0.2, 0.3, 0.4}
	err = store.ImportThought(ctx, "thought-uid-abc", "imported thought", "cli", instanceID, modelID, embedding, StateActive, "2025-01-15T10:30:00.000Z")
	if err != nil {
		t.Fatalf("ImportThought: %v", err)
	}

	thoughts, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(thoughts) != 1 {
		t.Fatalf("expected 1 thought, got %d", len(thoughts))
	}
	if thoughts[0].ID != "thought-uid-abc" {
		t.Fatalf("expected imported UID, got %q", thoughts[0].ID)
	}
	if thoughts[0].Content != "imported thought" {
		t.Fatalf("expected imported content, got %q", thoughts[0].Content)
	}

	// Import same UID again — should be no-op
	err = store.ImportThought(ctx, "thought-uid-abc", "imported thought", "cli", instanceID, modelID, embedding, StateActive, "2025-01-15T10:30:00.000Z")
	if err != nil {
		t.Fatalf("ImportThought duplicate: %v", err)
	}
	thoughts, err = store.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List after dup: %v", err)
	}
	if len(thoughts) != 1 {
		t.Fatalf("expected still 1 thought after dup, got %d", len(thoughts))
	}
}
