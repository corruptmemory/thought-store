package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRefineThoughtsViaInterface(t *testing.T) {
	// Compile-time interface check: LocalBrain and RemoteBrain must satisfy ThoughtStore,
	// which now includes RefineThoughts. The var _ lines in brain.go and remote.go
	// enforce this at compile time; this test exists so the check is visible in test output.
	var _ ThoughtStore = (*LocalBrain)(nil)
	var _ ThoughtStore = (*RemoteBrain)(nil)
}

func TestMigrateCreatesRefinedThoughtsTable(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migrate-refine.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var name string
	err = store.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='refined_thoughts'`).Scan(&name)
	if err != nil {
		t.Fatalf("refined_thoughts table not found: %v", err)
	}
}

func TestRefineThoughts(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "refine.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	// Create three source thoughts
	t1, _ := store.Insert(ctx, "thought about security", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	t2, _ := store.Insert(ctx, "thought about distributed systems", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)
	t3, _ := store.Insert(ctx, "thought about encryption", []float32{0.2, 0.3, 0.4, 0.5}, "cli", instID, modelID)

	// Add metadata to sources
	store.AddThoughtMetadata(ctx, t1.ID, MetadataTag, "security", instID)
	store.AddThoughtMetadata(ctx, t2.ID, MetadataTag, "distributed", instID)
	store.AddThoughtMetadata(ctx, t3.ID, MetadataTag, "security", instID)

	// Refine
	meta := []ThoughtMetadataEntry{
		{Type: MetadataTag, Value: "security"},
		{Type: MetadataTag, Value: "distributed"},
		{Type: MetadataTag, Value: "summary"},
	}
	summary, err := store.RefineThoughts(ctx,
		[]string{t1.ID, t2.ID, t3.ID},
		"Summary of distributed security thoughts",
		[]float32{0.3, 0.4, 0.5, 0.6},
		meta, "cli", instID, modelID)
	if err != nil {
		t.Fatalf("RefineThoughts: %v", err)
	}

	// Summary should be active with metadata
	if summary.State != StateActive {
		t.Fatalf("expected summary to be active, got %v", summary.State)
	}
	if len(summary.Metadata) != 3 {
		t.Fatalf("expected 3 metadata entries on summary, got %d", len(summary.Metadata))
	}

	// Sources should be refined
	sources, _ := store.GetByUniqueIDs(ctx, []string{t1.ID, t2.ID, t3.ID})
	for _, s := range sources {
		if s.State != StateRefined {
			t.Fatalf("expected source %s to be refined, got %v", s.ID, s.State)
		}
	}

	// DAG edges should exist
	var edgeCount int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refined_thoughts`).Scan(&edgeCount)
	if edgeCount != 3 {
		t.Fatalf("expected 3 DAG edges, got %d", edgeCount)
	}
}

func TestRefineThoughtsSharedSource(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "refine-shared.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	// A, B → D (refinement 1)
	tA, _ := store.Insert(ctx, "thought A", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	tB, _ := store.Insert(ctx, "thought B", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)
	tC, _ := store.Insert(ctx, "thought C", []float32{0.2, 0.3, 0.4, 0.5}, "cli", instID, modelID)

	store.RefineThoughts(ctx, []string{tA.ID, tB.ID}, "summary D",
		[]float32{0.3, 0.4, 0.5, 0.6}, nil, "cli", instID, modelID)

	// A, C → E (refinement 2 — shares source A)
	_, err = store.RefineThoughts(ctx, []string{tA.ID, tC.ID}, "summary E",
		[]float32{0.4, 0.5, 0.6, 0.7}, nil, "cli", instID, modelID)
	if err != nil {
		t.Fatalf("second RefineThoughts: %v", err)
	}

	// A should still be refined (participated in both)
	thoughts, _ := store.GetByUniqueIDs(ctx, []string{tA.ID})
	if thoughts[0].State != StateRefined {
		t.Fatalf("expected A to be refined, got %v", thoughts[0].State)
	}

	// Should have 4 edges total (2 for D, 2 for E)
	var edgeCount int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refined_thoughts`).Scan(&edgeCount)
	if edgeCount != 4 {
		t.Fatalf("expected 4 DAG edges, got %d", edgeCount)
	}
}

func TestForgetSummaryRevertsSourcesConditionally(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "forget-summary.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	// A, B → D (refinement 1)
	tA, _ := store.Insert(ctx, "thought A", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	tB, _ := store.Insert(ctx, "thought B", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)
	tC, _ := store.Insert(ctx, "thought C", []float32{0.2, 0.3, 0.4, 0.5}, "cli", instID, modelID)

	summaryD, _ := store.RefineThoughts(ctx, []string{tA.ID, tB.ID}, "summary D",
		[]float32{0.3, 0.4, 0.5, 0.6}, nil, "cli", instID, modelID)

	// A, C → E (refinement 2 — shares source A)
	_, err = store.RefineThoughts(ctx, []string{tA.ID, tC.ID}, "summary E",
		[]float32{0.4, 0.5, 0.6, 0.7}, nil, "cli", instID, modelID)
	if err != nil {
		t.Fatal(err)
	}

	// Forget D
	if err := store.ForgetThought(ctx, summaryD.ID); err != nil {
		t.Fatalf("ForgetThought: %v", err)
	}

	// B should revert to active (no remaining source edges)
	thoughts, _ := store.GetByUniqueIDs(ctx, []string{tB.ID})
	if thoughts[0].State != StateActive {
		t.Fatalf("expected B to be active, got %v", thoughts[0].State)
	}

	// A should stay refined (still has A→E edge)
	thoughts, _ = store.GetByUniqueIDs(ctx, []string{tA.ID})
	if thoughts[0].State != StateRefined {
		t.Fatalf("expected A to stay refined, got %v", thoughts[0].State)
	}

	// D→source edges should be deleted
	var edgeCount int
	store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM refined_thoughts WHERE target_thought_id = (SELECT id FROM thoughts WHERE unique_id = ?)`,
		summaryD.ID).Scan(&edgeCount)
	if edgeCount != 0 {
		t.Fatalf("expected 0 edges for forgotten summary, got %d", edgeCount)
	}
}

func TestForgetSourceDeletesEdges(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "forget-source.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	tA, _ := store.Insert(ctx, "thought A", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	tB, _ := store.Insert(ctx, "thought B", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	store.RefineThoughts(ctx, []string{tA.ID, tB.ID}, "summary",
		[]float32{0.3, 0.4, 0.5, 0.6}, nil, "cli", instID, modelID)

	// Forget source A
	if err := store.ForgetThought(ctx, tA.ID); err != nil {
		t.Fatalf("ForgetThought source: %v", err)
	}

	// Edge A→summary should be deleted
	var edgeCount int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refined_thoughts`).Scan(&edgeCount)
	if edgeCount != 1 { // only B→summary remains
		t.Fatalf("expected 1 remaining edge, got %d", edgeCount)
	}
}
