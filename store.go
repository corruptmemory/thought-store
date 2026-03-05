package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

type Thought struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

type SearchResult struct {
	Thought
	Similarity float64 `json:"similarity"`
}

type StoreStats struct {
	Total    int            `json:"total"`
	OldestAt *time.Time     `json:"oldest_at,omitempty"`
	NewestAt *time.Time     `json:"newest_at,omitempty"`
	BySource map[string]int `json:"by_source"`
}

// ThoughtRow is a minimal row for re-embedding (no vector data loaded).
type ThoughtRow struct {
	ID      string
	Content string
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	// WARNING: The vector dimension (1024) is coupled to the embedding model
	// (mxbai-embed-large). Changing models requires changing this dimension.
	//
	// Future: decouple by moving embeddings to a separate table with a 1:1 FK
	// to thoughts, allowing model switches via re-embed without schema changes.
	// See docs/plans/2026-03-05-setup-design.md "Future: Embedding Dimension Decoupling".
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS thoughts (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			content     TEXT NOT NULL,
			embedding   vector(1024) NOT NULL,
			source      TEXT NOT NULL DEFAULT 'web',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS thoughts_embedding_idx
		ON thoughts USING hnsw (embedding vector_cosine_ops)`)
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	return nil
}

func (s *Store) Insert(ctx context.Context, content string, embedding []float32, source string) (*Thought, error) {
	var t Thought
	err := s.pool.QueryRow(ctx,
		`INSERT INTO thoughts (content, embedding, source)
		 VALUES ($1, $2, $3)
		 RETURNING id, content, source, created_at`,
		content, pgvector.NewVector(embedding), source,
	).Scan(&t.ID, &t.Content, &t.Source, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert thought: %w", err)
	}
	return &t, nil
}

func (s *Store) List(ctx context.Context, limit, offset int) ([]Thought, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, content, source, created_at
		 FROM thoughts ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list thoughts: %w", err)
	}
	defer rows.Close()

	var thoughts []Thought
	for rows.Next() {
		var t Thought
		if err := rows.Scan(&t.ID, &t.Content, &t.Source, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan thought: %w", err)
		}
		thoughts = append(thoughts, t)
	}
	return thoughts, rows.Err()
}

func (s *Store) Stats(ctx context.Context) (*StoreStats, error) {
	var stats StoreStats
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM thoughts`,
	).Scan(&stats.Total, &stats.OldestAt, &stats.NewestAt)
	if err != nil {
		return nil, fmt.Errorf("stats query: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT source, COUNT(*) FROM thoughts GROUP BY source`)
	if err != nil {
		return nil, fmt.Errorf("stats by source: %w", err)
	}
	defer rows.Close()

	stats.BySource = make(map[string]int)
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			return nil, fmt.Errorf("scan source count: %w", err)
		}
		stats.BySource[source] = count
	}
	return &stats, rows.Err()
}

func (s *Store) Diagnostics(ctx context.Context) *PostgresDiag {
	diag := &PostgresDiag{
		SchemaVersion: 1,
		Status:        "connected",
	}

	// Connection info from pool config
	cfg := s.pool.Config().ConnConfig
	diag.Host = cfg.Host
	diag.Port = cfg.Port
	diag.Database = cfg.Database

	// Pool stats
	stat := s.pool.Stat()
	diag.PoolTotal = stat.TotalConns()
	diag.PoolIdle = stat.IdleConns()
	diag.PoolInUse = stat.AcquiredConns()

	// Postgres version
	if err := s.pool.QueryRow(ctx, `SELECT version()`).Scan(&diag.Version); err != nil {
		diag.Errors = append(diag.Errors, fmt.Sprintf("pg version: %v", err))
	}

	// pgvector version
	if err := s.pool.QueryRow(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`,
	).Scan(&diag.PgvectorVersion); err != nil {
		diag.Errors = append(diag.Errors, fmt.Sprintf("pgvector version: %v", err))
	}

	// Thought count
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM thoughts`).Scan(&diag.ThoughtCount); err != nil {
		diag.Errors = append(diag.Errors, fmt.Sprintf("thought count: %v", err))
	}

	// HNSW index
	var indexName string
	if err := s.pool.QueryRow(ctx,
		`SELECT indexname FROM pg_indexes WHERE tablename = 'thoughts' AND indexname = 'thoughts_embedding_idx'`,
	).Scan(&indexName); err != nil {
		diag.Errors = append(diag.Errors, fmt.Sprintf("hnsw index: %v", err))
	} else {
		diag.HNSWIndex = true
	}

	return diag
}

// ListAllContent returns all thought IDs and content for re-embedding.
func (s *Store) ListAllContent(ctx context.Context) ([]ThoughtRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, content FROM thoughts ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all content: %w", err)
	}
	defer rows.Close()

	var thoughts []ThoughtRow
	for rows.Next() {
		var t ThoughtRow
		if err := rows.Scan(&t.ID, &t.Content); err != nil {
			return nil, fmt.Errorf("scan thought row: %w", err)
		}
		thoughts = append(thoughts, t)
	}
	return thoughts, rows.Err()
}

// UpdateEmbeddings updates the embedding column for the given thought IDs in a transaction.
func (s *Store) UpdateEmbeddings(ctx context.Context, ids []string, embeddings [][]float32) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	for i, id := range ids {
		_, err := tx.Exec(ctx,
			`UPDATE thoughts SET embedding = $1 WHERE id = $2`,
			pgvector.NewVector(embeddings[i]), id)
		if err != nil {
			return fmt.Errorf("update embedding for %s: %w", id, err)
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) Search(ctx context.Context, embedding []float32, limit int) ([]SearchResult, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, content, source, created_at,
		        1 - (embedding <=> $1) AS similarity
		 FROM thoughts
		 ORDER BY embedding <=> $1
		 LIMIT $2`,
		pgvector.NewVector(embedding), limit)
	if err != nil {
		return nil, fmt.Errorf("search thoughts: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Content, &r.Source, &r.CreatedAt, &r.Similarity); err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
