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

func TestExportUnpackRoundTrip(t *testing.T) {
	store, dbPath := newTestStoreWithData(t)
	ctx := context.Background()

	// Export
	bundlePath := filepath.Join(t.TempDir(), "bundle.tar.zst")
	outFile, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := ExportBundle(ctx, store, dbPath, outFile); err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	outFile.Close()

	// Unpack
	inFile, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer inFile.Close()

	unpackDir := t.TempDir()
	if err := unpackBundle(inFile, unpackDir); err != nil {
		t.Fatalf("unpackBundle: %v", err)
	}

	// Verify brain.db exists
	foreignDB := filepath.Join(unpackDir, "brain.db")
	info, err := os.Stat(foreignDB)
	if err != nil {
		t.Fatalf("brain.db not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("brain.db is empty")
	}

	// Verify we can open and query it
	foreignStore, err := NewSQLiteStore(foreignDB)
	if err != nil {
		t.Fatalf("open foreign db: %v", err)
	}
	defer foreignStore.Close()

	thoughts, err := foreignStore.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list foreign thoughts: %v", err)
	}
	if len(thoughts) != 2 {
		t.Fatalf("expected 2 thoughts, got %d", len(thoughts))
	}
}

func TestImportBundle(t *testing.T) {
	ctx := context.Background()

	// Create exporter (machine A)
	storeA, dbPathA := newTestStoreWithData(t)

	// Create importer (machine B)
	dbPathB := filepath.Join(t.TempDir(), "importer.db")
	storeB, err := NewSQLiteStore(dbPathB)
	if err != nil {
		t.Fatalf("NewSQLiteStore B: %v", err)
	}
	defer storeB.Close()
	if err := storeB.Migrate(ctx); err != nil {
		t.Fatalf("Migrate B: %v", err)
	}
	instanceIDB, err := storeB.EnsureInstance(ctx, "importer", "test importer")
	if err != nil {
		t.Fatalf("EnsureInstance B: %v", err)
	}
	modelIDB, err := storeB.EnsureModel(ctx, "test-model", "", 4)
	if err != nil {
		t.Fatalf("EnsureModel B: %v", err)
	}
	if err := storeB.SetActiveModel(ctx, modelIDB); err != nil {
		t.Fatalf("SetActiveModel B: %v", err)
	}

	// B inserts its own thought
	_, err = storeB.Insert(ctx, "local thought on B", []float32{0.9, 0.8, 0.7, 0.6}, "cli", instanceIDB, modelIDB)
	if err != nil {
		t.Fatalf("Insert B: %v", err)
	}

	// Export from A
	bundlePath := filepath.Join(t.TempDir(), "bundle.tar.zst")
	outFile, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := ExportBundle(ctx, storeA, dbPathA, outFile); err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	outFile.Close()

	// Get B's instance UID
	myUID, err := storeB.InstanceUID(ctx, instanceIDB)
	if err != nil {
		t.Fatalf("InstanceUID B: %v", err)
	}

	// Import into B
	inFile, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer inFile.Close()

	result, err := ImportBundle(ctx, storeB, myUID, modelIDB, inFile)
	if err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}

	if result.NewInstances != 1 {
		t.Fatalf("expected 1 new instance, got %d", result.NewInstances)
	}
	if result.NewThoughts != 2 {
		t.Fatalf("expected 2 new thoughts, got %d", result.NewThoughts)
	}

	// Verify B now has 3 thoughts total (1 local + 2 imported)
	thoughts, err := storeB.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(thoughts) != 3 {
		t.Fatalf("expected 3 thoughts, got %d", len(thoughts))
	}

	// Import again — should be idempotent
	inFile2, _ := os.Open(bundlePath)
	defer inFile2.Close()
	result2, err := ImportBundle(ctx, storeB, myUID, modelIDB, inFile2)
	if err != nil {
		t.Fatalf("ImportBundle second: %v", err)
	}
	if result2.NewThoughts != 0 {
		t.Fatalf("expected 0 new thoughts on re-import, got %d", result2.NewThoughts)
	}
	if result2.Skipped != 2 {
		t.Fatalf("expected 2 skipped on re-import, got %d", result2.Skipped)
	}
}
