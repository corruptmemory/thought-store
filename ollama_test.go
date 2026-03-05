package main

import (
	"context"
	"net/http"
	"testing"
)

func ollamaAvailable(t *testing.T) {
	t.Helper()
	resp, err := http.Get("http://localhost:11434/api/version")
	if err != nil {
		t.Skip("Ollama not running, skipping integration test")
	}
	resp.Body.Close()
}

func TestOllamaEmbed(t *testing.T) {
	ollamaAvailable(t)

	client := NewOllamaClient("http://localhost:11434", "mxbai-embed-large")

	embedding, err := client.Embed(context.Background(), "test thought about fixing bugs")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(embedding) != 1024 {
		t.Fatalf("expected 1024 dimensions, got %d", len(embedding))
	}

	// Verify values are non-zero (not a blank embedding)
	allZero := true
	for _, v := range embedding {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("embedding is all zeros")
	}
}
