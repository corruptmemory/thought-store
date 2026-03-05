package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRemoteBrain(t *testing.T) {
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
	brain := NewLocalBrain(store, ollama)
	srv := NewServer(brain)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go http.Serve(ln, srv)

	base := fmt.Sprintf("http://%s", ln.Addr().String())
	remote := NewRemoteBrain([]string{base}, 5*time.Second)

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

	// Cleanup
	store.pool.Exec(ctx, "DELETE FROM thoughts WHERE source = 'test' AND content LIKE 'remote test:%'")
}

func TestRemoteBrainFallback(t *testing.T) {
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
	brain := NewLocalBrain(store, ollama)
	srv := NewServer(brain)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go http.Serve(ln, srv)

	base := fmt.Sprintf("http://%s", ln.Addr().String())

	// First URL is unreachable, second is the real server
	remote := NewRemoteBrain([]string{"http://127.0.0.1:1", base}, 2*time.Second)

	stats, err := remote.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats with fallback: %v", err)
	}
	if stats.Total == 0 {
		t.Fatal("expected non-zero total via fallback")
	}
}
