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

	// Diagnostics
	resp, err = client.Get(base + "/api/diagnostics")
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	defer resp.Body.Close()

	var diag DiagnosticsReport
	json.NewDecoder(resp.Body).Decode(&diag)
	if diag.Mode != BrainModeLocal {
		t.Fatalf("expected local mode, got %s", diag.Mode)
	}
	if diag.Postgres == nil {
		t.Fatal("expected postgres diagnostics")
	}
	if diag.Postgres.ThoughtCount == 0 {
		t.Fatal("expected non-zero thought count in diagnostics")
	}
	if !diag.Postgres.HNSWIndex {
		t.Fatal("expected HNSW index to exist")
	}
	if diag.Ollama == nil {
		t.Fatal("expected ollama diagnostics")
	}
	if diag.Ollama.EmbeddingDim != 1024 {
		t.Fatalf("expected embedding dim 1024, got %d", diag.Ollama.EmbeddingDim)
	}
	if diag.GoVersion == "" {
		t.Fatal("expected non-empty Go version")
	}
	if len(diag.Postgres.Errors) > 0 {
		t.Fatalf("unexpected postgres errors: %v", diag.Postgres.Errors)
	}
	if len(diag.Ollama.Errors) > 0 {
		t.Fatalf("unexpected ollama errors: %v", diag.Ollama.Errors)
	}

	// Cleanup
	store.pool.Exec(ctx, "DELETE FROM thoughts WHERE source = 'web' AND content LIKE 'e2e test:%'")
}
