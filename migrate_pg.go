package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

type MigrateFromPostgresCmd struct {
	Config      string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	PostgresURL string `long:"postgres-url" required:"true" description:"Postgres connection URL"`
}

func (cmd *MigrateFromPostgresCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Open Postgres source
	pgCfg, err := pgxpool.ParseConfig(cmd.PostgresURL)
	if err != nil {
		return fmt.Errorf("parse postgres URL: %w", err)
	}
	pgCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pgPool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pgPool.Close()

	// Open SQLite destination
	store, err := NewSQLiteStore(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("sqlite migrate: %w", err)
	}

	// Setup instance and model
	hostname, _ := os.Hostname()
	instanceID, err := store.EnsureInstance(ctx, hostname, "migrated from postgres")
	if err != nil {
		return fmt.Errorf("ensure instance: %w", err)
	}

	modelID, err := store.EnsureModel(ctx, cfg.Ollama.Model, "", 1024)
	if err != nil {
		return fmt.Errorf("ensure model: %w", err)
	}
	if err := store.SetActiveModel(ctx, modelID); err != nil {
		return fmt.Errorf("set active model: %w", err)
	}

	// Export from Postgres
	rows, err := pgPool.Query(ctx,
		`SELECT id, content, source, embedding, created_at FROM thoughts ORDER BY created_at ASC`)
	if err != nil {
		return fmt.Errorf("query postgres: %w", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var pgID, content, source string
		var embedding pgvector.Vector
		var createdAt time.Time

		if err := rows.Scan(&pgID, &content, &source, &embedding, &createdAt); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		sourceID, err := store.SourceID(ctx, source)
		if err != nil {
			return fmt.Errorf("source %s: %w", source, err)
		}

		// Insert thought with original UUID and timestamp
		_, err = store.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO thoughts (unique_id, content, source_id, instance_id, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			pgID, content, sourceID, instanceID, createdAt.Format("2006-01-02T15:04:05.000Z"))
		if err != nil {
			return fmt.Errorf("insert thought: %w", err)
		}

		var thoughtIntID int
		store.db.QueryRowContext(ctx,
			`SELECT id FROM thoughts WHERE unique_id = ?`, pgID).Scan(&thoughtIntID)

		_, err = store.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO embeddings (thought_id, model_id, vector) VALUES (?, ?, ?)`,
			thoughtIntID, modelID, encodeVector(embedding.Slice()))
		if err != nil {
			return fmt.Errorf("insert embedding: %w", err)
		}

		count++
		if count%100 == 0 {
			fmt.Fprintf(os.Stderr, "  migrated %d thoughts...\n", count)
		}
	}

	fmt.Fprintf(os.Stderr, "Done. Migrated %d thoughts with embeddings.\n", count)
	return nil
}
