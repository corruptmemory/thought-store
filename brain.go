package main

import (
	"context"
	"runtime"
	"time"
)

// ThoughtStore is the core interface for interacting with the thought store.
// Both LocalBrain (direct SQLite+Ollama) and RemoteBrain (HTTP client) implement it.
type ThoughtStore interface {
	Capture(ctx context.Context, content string, source string, metadata []ThoughtMetadataEntry) (*Thought, error)
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	List(ctx context.Context, limit, offset int) ([]Thought, error)
	Stats(ctx context.Context) (*StoreStats, error)
	Diagnostics(ctx context.Context) (*DiagnosticsReport, error)
}

var _ ThoughtStore = (*LocalBrain)(nil)

// LocalBrain implements ThoughtStore using direct SQLite and Ollama access.
type LocalBrain struct {
	store       *SQLiteStore
	ollama      *OllamaClient
	vecIndex    *VecIndex
	instanceID  int
	modelID     int
	startedAt   time.Time
	mdnsName    string
	mdnsService string
}

// SetMDNS records that mDNS advertising is active for diagnostics.
func (b *LocalBrain) SetMDNS(name, service string) {
	b.mdnsName = name
	b.mdnsService = service
}

func NewLocalBrain(store *SQLiteStore, ollama *OllamaClient, vecIndex *VecIndex, instanceID, modelID int) *LocalBrain {
	return &LocalBrain{
		store:      store,
		ollama:     ollama,
		vecIndex:   vecIndex,
		instanceID: instanceID,
		modelID:    modelID,
		startedAt:  time.Now(),
	}
}

func (b *LocalBrain) Capture(ctx context.Context, content string, source string, metadata []ThoughtMetadataEntry) (*Thought, error) {
	embedding, err := b.ollama.Embed(ctx, content)
	if err != nil {
		return nil, err
	}
	thought, err := b.store.InsertWithMetadata(ctx, content, embedding, source, b.instanceID, b.modelID, metadata)
	if err != nil {
		return nil, err
	}
	b.vecIndex.Add(thought.ID, embedding)
	return thought, nil
}

func (b *LocalBrain) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	embedding, err := b.ollama.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	vecResults := b.vecIndex.Search(embedding, limit)

	uids := make([]string, len(vecResults))
	simMap := make(map[string]float64)
	for i, vr := range vecResults {
		uids[i] = vr.ID
		simMap[vr.ID] = vr.Similarity
	}

	thoughts, err := b.store.GetByUniqueIDs(ctx, uids)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, len(thoughts))
	for i, t := range thoughts {
		results[i] = SearchResult{
			Thought:    t,
			Similarity: simMap[t.ID],
		}
	}
	return results, nil
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
		SQLite:    b.store.Diagnostics(ctx),
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
