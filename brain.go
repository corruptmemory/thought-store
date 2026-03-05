package main

import (
	"context"
	"runtime"
	"time"
)

// ThoughtStore is the core interface for interacting with the thought store.
// Both LocalBrain (direct Postgres+Ollama) and RemoteBrain (HTTP client) implement it.
type ThoughtStore interface {
	Capture(ctx context.Context, content string, source string) (*Thought, error)
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	List(ctx context.Context, limit, offset int) ([]Thought, error)
	Stats(ctx context.Context) (*StoreStats, error)
	Diagnostics(ctx context.Context) (*DiagnosticsReport, error)
}

var _ ThoughtStore = (*LocalBrain)(nil)

// LocalBrain implements ThoughtStore using direct Postgres and Ollama access.
type LocalBrain struct {
	store       *Store
	ollama      *OllamaClient
	startedAt   time.Time
	mdnsName    string
	mdnsService string
}

// SetMDNS records that mDNS advertising is active for diagnostics.
func (b *LocalBrain) SetMDNS(name, service string) {
	b.mdnsName = name
	b.mdnsService = service
}

func NewLocalBrain(store *Store, ollama *OllamaClient) *LocalBrain {
	return &LocalBrain{store: store, ollama: ollama, startedAt: time.Now()}
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

func (b *LocalBrain) Diagnostics(ctx context.Context) (*DiagnosticsReport, error) {
	report := &DiagnosticsReport{
		Mode:      BrainModeLocal,
		StartedAt: b.startedAt,
		Uptime:    time.Since(b.startedAt).Truncate(time.Second).String(),
		GoVersion: runtime.Version(),
		Postgres:  b.store.Diagnostics(ctx),
		Ollama:    b.ollama.Diagnostics(ctx),
	}
	if b.mdnsName != "" {
		report.MDNS = &MDNSDiag{
			Advertising: true,
			Name:        b.mdnsName,
			Service:     b.mdnsService,
		}
	}
	return report, nil
}
