package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestE2E(t *testing.T) {
	ollamaAvailable(t)

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	instanceID, err := store.EnsureInstance(ctx, "test", "e2e test")
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
	if diag.SQLite == nil {
		t.Fatal("expected sqlite diagnostics")
	}
	if diag.SQLite.ThoughtCount == 0 {
		t.Fatal("expected non-zero thought count in diagnostics")
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
}
