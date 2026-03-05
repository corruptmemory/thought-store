package main

import "context"

// ThoughtStore is the core interface for interacting with the thought store.
// Both LocalBrain (direct Postgres+Ollama) and RemoteBrain (HTTP client) implement it.
type ThoughtStore interface {
	Capture(ctx context.Context, content string, source string) (*Thought, error)
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	List(ctx context.Context, limit, offset int) ([]Thought, error)
	Stats(ctx context.Context) (*StoreStats, error)
}

// LocalBrain implements ThoughtStore using direct Postgres and Ollama access.
type LocalBrain struct {
	store  *Store
	ollama *OllamaClient
}

func NewLocalBrain(store *Store, ollama *OllamaClient) *LocalBrain {
	return &LocalBrain{store: store, ollama: ollama}
}

func (b *LocalBrain) Capture(ctx context.Context, content string, source string) (*Thought, error) {
	embedding, err := b.ollama.Embed(ctx, content)
	if err != nil {
		return nil, err
	}
	return b.store.Insert(ctx, content, embedding, source)
}

func (b *LocalBrain) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	embedding, err := b.ollama.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	return b.store.Search(ctx, embedding, limit)
}

func (b *LocalBrain) List(ctx context.Context, limit, offset int) ([]Thought, error) {
	return b.store.List(ctx, limit, offset)
}

func (b *LocalBrain) Stats(ctx context.Context) (*StoreStats, error) {
	return b.store.Stats(ctx)
}
