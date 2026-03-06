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
