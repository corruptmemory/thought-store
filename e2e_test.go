package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestE2E(t *testing.T) {
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
	client := &http.Client{Timeout: 10 * time.Second}

	// Capture
	body, _ := json.Marshal(captureRequest{Content: "e2e test: the authentication system needs refactoring"})
	resp, err := client.Post(base+"/api/thoughts", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("capture status: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Search
	resp, err = client.Get(base + "/api/search?q=auth+refactor")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	defer resp.Body.Close()

	var results []SearchResult
	json.NewDecoder(resp.Body).Decode(&results)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].Similarity < 0.5 {
		t.Fatalf("expected high similarity, got %.2f", results[0].Similarity)
	}

	// Stats
	resp, err = client.Get(base + "/api/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	defer resp.Body.Close()

	var stats StoreStats
	json.NewDecoder(resp.Body).Decode(&stats)
	if stats.Total == 0 {
		t.Fatal("expected non-zero total in stats")
	}

	// Cleanup
	store.pool.Exec(ctx, "DELETE FROM thoughts WHERE source = 'web' AND content LIKE 'e2e test:%'")
}
