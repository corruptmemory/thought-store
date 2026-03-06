package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestSyncHTTPRoundTrip(t *testing.T) {
	ctx := context.Background()

	// Set up server A
	dbPathA := filepath.Join(t.TempDir(), "a.db")
	storeA, _ := NewSQLiteStore(dbPathA)
	defer storeA.Close()
	storeA.Migrate(ctx)
	instIDA, _ := storeA.EnsureInstance(ctx, "server-A", "")
	modelIDA, _ := storeA.EnsureModel(ctx, "test-model", "", 4)
	storeA.SetActiveModel(ctx, modelIDA)
	storeA.Insert(ctx, "thought from A", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instIDA, modelIDA)
	uidA, _ := storeA.InstanceUID(ctx, instIDA)

	ollamaA := NewOllamaClient("http://localhost:11434", "test-model")
	vecIndexA := NewVecIndex()
	brainA := NewLocalBrain(storeA, ollamaA, vecIndexA, instIDA, modelIDA)
	srvA := NewServer(brainA)
	srvA.SetSyncState(storeA, dbPathA, uidA, modelIDA, vecIndexA)

	// Set up server B
	dbPathB := filepath.Join(t.TempDir(), "b.db")
	storeB, _ := NewSQLiteStore(dbPathB)
	defer storeB.Close()
	storeB.Migrate(ctx)
	instIDB, _ := storeB.EnsureInstance(ctx, "server-B", "")
	modelIDB, _ := storeB.EnsureModel(ctx, "test-model", "", 4)
	storeB.SetActiveModel(ctx, modelIDB)
	storeB.Insert(ctx, "thought from B", []float32{0.5, 0.6, 0.7, 0.8}, "web", instIDB, modelIDB)
	uidB, _ := storeB.InstanceUID(ctx, instIDB)

	ollamaB := NewOllamaClient("http://localhost:11434", "test-model")
	vecIndexB := NewVecIndex()
	brainB := NewLocalBrain(storeB, ollamaB, vecIndexB, instIDB, modelIDB)
	srvB := NewServer(brainB)
	srvB.SetSyncState(storeB, dbPathB, uidB, modelIDB, vecIndexB)

	// GET /api/sync from A (download bundle)
	tsA := httptest.NewServer(srvA)
	defer tsA.Close()

	resp, err := http.Get(tsA.URL + "/api/sync")
	if err != nil {
		t.Fatalf("GET /api/sync: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	bundle, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if len(bundle) == 0 {
		t.Fatal("empty bundle")
	}

	// POST /api/sync to B (upload A's bundle)
	tsB := httptest.NewServer(srvB)
	defer tsB.Close()

	resp, err = http.Post(tsB.URL+"/api/sync", "application/octet-stream", bytes.NewReader(bundle))
	if err != nil {
		t.Fatalf("POST /api/sync: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// B should now have 2 thoughts
	thoughts, _ := storeB.List(ctx, 10, 0)
	if len(thoughts) != 2 {
		t.Fatalf("B should have 2 thoughts, got %d", len(thoughts))
	}
}
