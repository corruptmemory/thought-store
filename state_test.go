package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestThoughtStateString(t *testing.T) {
	tests := []struct {
		state ThoughtState
		want  string
	}{
		{StateActive, "active"},
		{StateRefined, "refined"},
		{StateForgotten, "forgotten"},
		{ThoughtState(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.state.String()
		if got != tt.want {
			t.Errorf("ThoughtState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestThoughtStateMarshalText(t *testing.T) {
	tests := []struct {
		state ThoughtState
		want  string
	}{
		{StateActive, "active"},
		{StateRefined, "refined"},
		{StateForgotten, "forgotten"},
		{ThoughtState(42), "unknown"},
	}

	for _, tt := range tests {
		got, err := tt.state.MarshalText()
		if err != nil {
			t.Fatalf("ThoughtState(%d).MarshalText() returned error: %v", tt.state, err)
		}
		if string(got) != tt.want {
			t.Errorf("ThoughtState(%d).MarshalText() = %q, want %q", tt.state, string(got), tt.want)
		}
	}
}

func TestVecIndexRemove(t *testing.T) {
	idx := NewVecIndex()

	vecA := []float32{1, 0, 0}
	vecB := []float32{0, 1, 0}
	vecC := []float32{0, 0, 1}

	idx.Add("a", vecA)
	idx.Add("b", vecB)
	idx.Add("c", vecC)

	if idx.Len() != 3 {
		t.Fatalf("expected Len()==3, got %d", idx.Len())
	}

	// Remove "b"
	idx.Remove("b")

	if idx.Len() != 2 {
		t.Fatalf("expected Len()==2 after remove, got %d", idx.Len())
	}

	// Search for vecB — "b" should not appear in results
	results := idx.Search(vecB, 10)
	for _, r := range results {
		if r.ID == "b" {
			t.Errorf("found removed ID %q in search results", "b")
		}
	}

	// Remove nonexistent — should be a no-op
	idx.Remove("nonexistent")
	if idx.Len() != 2 {
		t.Fatalf("expected Len()==2 after no-op remove, got %d", idx.Len())
	}
}

func TestMigrateAddsStateColumn(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migrate-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// Insert a thought and check default
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, err := store.Insert(ctx, "test thought", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	if err != nil {
		t.Fatal(err)
	}

	var state int
	err = store.db.QueryRowContext(ctx,
		`SELECT state FROM thoughts WHERE unique_id = ?`, thought.ID).Scan(&state)
	if err != nil {
		t.Fatalf("select state: %v", err)
	}
	if state != 0 {
		t.Fatalf("expected state=0 (active), got %d", state)
	}
}

func TestListFiltersActiveOnly(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "list-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "active thought", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.Insert(ctx, "another active", []float32{0.5, 0.6, 0.7, 0.8}, "web", instID, modelID)

	// Manually set one thought to forgotten
	store.db.ExecContext(ctx, `UPDATE thoughts SET state = 2, content = '' WHERE content = 'another active'`)

	thoughts, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(thoughts) != 1 {
		t.Fatalf("expected 1 active thought, got %d", len(thoughts))
	}
	if thoughts[0].Content != "active thought" {
		t.Fatalf("expected 'active thought', got %q", thoughts[0].Content)
	}
}

func TestStatsCountsActiveOnly(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "stats-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "one", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.Insert(ctx, "two", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	store.db.ExecContext(ctx, `UPDATE thoughts SET state = 2, content = '' WHERE content = 'two'`)

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 {
		t.Fatalf("expected total=1, got %d", stats.Total)
	}
}

func TestListAllContentSkipsForgotten(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "listall-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "keep", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.Insert(ctx, "forget", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	store.db.ExecContext(ctx, `UPDATE thoughts SET state = 2, content = '' WHERE content = 'forget'`)

	rows, err := store.ListAllContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

func TestLoadEmbeddingsSkipsForgotten(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "embed-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "keep", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.Insert(ctx, "forget", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	store.db.ExecContext(ctx, `UPDATE thoughts SET state = 2, content = '' WHERE content = 'forget'`)

	ids, vecs, err := store.LoadEmbeddings(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(ids))
	}
	if len(vecs) != 1 {
		t.Fatalf("expected 1 vector, got %d", len(vecs))
	}
}

func TestForgetThought(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "forget.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, _ := store.Insert(ctx, "remember this", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)

	// Verify embedding exists
	ids, _, _ := store.LoadEmbeddings(ctx, modelID)
	if len(ids) != 1 {
		t.Fatalf("expected 1 embedding before forget, got %d", len(ids))
	}

	if err := store.ForgetThought(ctx, thought.ID); err != nil {
		t.Fatalf("ForgetThought: %v", err)
	}

	// Content should be empty
	var content string
	var state int
	store.db.QueryRowContext(ctx,
		`SELECT content, state FROM thoughts WHERE unique_id = ?`, thought.ID).Scan(&content, &state)
	if content != "" {
		t.Fatalf("expected empty content, got %q", content)
	}
	if state != int(StateForgotten) {
		t.Fatalf("expected state=%d (forgotten), got %d", StateForgotten, state)
	}

	// Embedding should be deleted (LoadEmbeddings filters by state, so also check directly)
	var embCount int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings`).Scan(&embCount)
	if embCount != 0 {
		t.Fatalf("expected 0 embeddings after forget, got %d", embCount)
	}

	// Should not appear in List
	thoughts, _ := store.List(ctx, 10, 0)
	if len(thoughts) != 0 {
		t.Fatalf("expected 0 thoughts in list, got %d", len(thoughts))
	}

	// Forget again — idempotent
	if err := store.ForgetThought(ctx, thought.ID); err != nil {
		t.Fatalf("ForgetThought second call: %v", err)
	}
}

func TestThoughtStateUnmarshalText(t *testing.T) {
	tests := []struct {
		text string
		want ThoughtState
	}{
		{"active", StateActive},
		{"refined", StateRefined},
		{"forgotten", StateForgotten},
		{"bogus", StateActive},
	}

	for _, tt := range tests {
		var got ThoughtState
		if err := got.UnmarshalText([]byte(tt.text)); err != nil {
			t.Fatalf("UnmarshalText(%q) returned error: %v", tt.text, err)
		}
		if got != tt.want {
			t.Errorf("UnmarshalText(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}
