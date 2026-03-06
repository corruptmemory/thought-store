package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newTestStoreWithData(t *testing.T) (*SQLiteStore, string) {
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

	instanceID, err := store.EnsureInstance(ctx, "exporter", "test exporter")
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

	_, err = store.Insert(ctx, "thought one", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instanceID, modelID)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	_, err = store.Insert(ctx, "thought two", []float32{0.5, 0.6, 0.7, 0.8}, "web", instanceID, modelID)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	return store, dbPath
}

func TestExportBundle(t *testing.T) {
	store, dbPath := newTestStoreWithData(t)
	ctx := context.Background()

	outPath := filepath.Join(t.TempDir(), "bundle.tar.zst")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	defer outFile.Close()

	if err := ExportBundle(ctx, store, dbPath, outFile); err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	outFile.Close()

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat bundle: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("expected non-empty bundle")
	}
}
