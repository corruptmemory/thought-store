package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func newTestLocalServer(t *testing.T) (string, *SQLiteStore) {
	t.Helper()
	ollamaAvailable(t)

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "remote-test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	instanceID, err := store.EnsureInstance(ctx, "test", "remote test")
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

	vecIndex := NewVecIndex()
	ids, vectors, err := store.LoadEmbeddings(ctx, modelID)
	if err != nil {
		t.Fatalf("LoadEmbeddings: %v", err)
	}
	vecIndex.Load(ids, vectors)

	brain := NewLocalBrain(store, ollama, vecIndex, instanceID, modelID)
	srv := NewServer(brain)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go http.Serve(ln, srv)

	base := fmt.Sprintf("http://%s", ln.Addr().String())
	return base, store
}

func TestRemoteBrain(t *testing.T) {
	base, _ := newTestLocalServer(t)

	ctx := context.Background()
	remote := NewRemoteBrain([]string{base}, nil, 5*time.Second)

	// Capture
	thought, err := remote.Capture(ctx, "remote test: distributed systems are hard", "test")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if thought.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// Search
	results, err := remote.Search(ctx, "distributed computing", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results")
	}

	// List
	thoughts, err := remote.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(thoughts) == 0 {
		t.Fatal("expected thoughts")
	}

	// Stats
	stats, err := remote.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total == 0 {
		t.Fatal("expected non-zero total")
	}
}

func TestRemoteBrainFallback(t *testing.T) {
	base, _ := newTestLocalServer(t)

	ctx := context.Background()

	// Seed a thought so stats returns non-zero
	direct := NewRemoteBrain([]string{base}, nil, 5*time.Second)
	if _, err := direct.Capture(ctx, "fallback test: seed thought", "test"); err != nil {
		t.Fatalf("seed Capture: %v", err)
	}

	// First URL is unreachable, second is the real server
	remote := NewRemoteBrain([]string{"http://127.0.0.1:1", base}, nil, 2*time.Second)

	stats, err := remote.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats with fallback: %v", err)
	}
	if stats.Total == 0 {
		t.Fatal("expected non-zero total via fallback")
	}
}
