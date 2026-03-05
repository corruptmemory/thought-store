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
