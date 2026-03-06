package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Enable WAL mode and foreign keys
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() {
	s.db.Close()
}

// Checkpoint forces a WAL checkpoint, folding the WAL into the main DB file.
// Call before exporting the database for sync.
func (s *SQLiteStore) Checkpoint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

func (s *SQLiteStore) Migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sources (
			id   INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE IF NOT EXISTS instances (
			id          INTEGER PRIMARY KEY,
			unique_id   TEXT NOT NULL UNIQUE,
			name        TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS models (
			id        INTEGER PRIMARY KEY,
			name      TEXT NOT NULL,
			version   TEXT NOT NULL DEFAULT '',
			dimension INTEGER NOT NULL,
			UNIQUE(name, version)
		)`,
		`CREATE TABLE IF NOT EXISTS active_model (
			id       INTEGER PRIMARY KEY CHECK (id = 1),
			model_id INTEGER NOT NULL REFERENCES models(id)
		)`,
		`CREATE TABLE IF NOT EXISTS thoughts (
			id          INTEGER PRIMARY KEY,
			unique_id   TEXT NOT NULL UNIQUE,
			content     TEXT NOT NULL,
			source_id   INTEGER NOT NULL REFERENCES sources(id),
			instance_id INTEGER NOT NULL REFERENCES instances(id),
			created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		)`,
		`CREATE TABLE IF NOT EXISTS embeddings (
			thought_id INTEGER NOT NULL REFERENCES thoughts(id),
			model_id   INTEGER NOT NULL REFERENCES models(id),
			vector     BLOB NOT NULL,
			PRIMARY KEY (thought_id, model_id)
		)`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	// Seed sources
	for _, name := range []string{"web", "cli", "mcp", "seed", "conversation"} {
		_, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO sources (name) VALUES (?)`, name)
		if err != nil {
			return fmt.Errorf("seed source %s: %w", name, err)
		}
	}

	return nil
}

// EnsureInstance creates or retrieves the local instance record.
func (s *SQLiteStore) EnsureInstance(ctx context.Context, name, description string) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM instances WHERE name = ?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}

	uid := uuid.New().String()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO instances (unique_id, name, description) VALUES (?, ?, ?)`,
		uid, name, description)
	if err != nil {
		return 0, fmt.Errorf("create instance: %w", err)
	}
	insertedID, _ := res.LastInsertId()
	return int(insertedID), nil
}

// EnsureModel creates or retrieves a model record.
func (s *SQLiteStore) EnsureModel(ctx context.Context, name, version string, dimension int) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM models WHERE name = ? AND version = ?`, name, version).Scan(&id)
	if err == nil {
		return id, nil
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO models (name, version, dimension) VALUES (?, ?, ?)`,
		name, version, dimension)
	if err != nil {
		return 0, fmt.Errorf("create model: %w", err)
	}
	insertedID, _ := res.LastInsertId()
	return int(insertedID), nil
}

// SetActiveModel sets the active model for search and capture.
func (s *SQLiteStore) SetActiveModel(ctx context.Context, modelID int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO active_model (id, model_id) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET model_id = excluded.model_id`, modelID)
	return err
}

// ActiveModelID returns the current active model ID.
func (s *SQLiteStore) ActiveModelID(ctx context.Context) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx,
		`SELECT model_id FROM active_model WHERE id = 1`).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("no active model set: %w", err)
	}
	return id, nil
}

// SourceID returns the ID for a source name, creating it if it doesn't exist.
func (s *SQLiteStore) SourceID(ctx context.Context, name string) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM sources WHERE name = ?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sources (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("create source %s: %w", name, err)
	}
	insertedID, _ := res.LastInsertId()
	return int(insertedID), nil
}

// Insert stores a thought with its embedding.
func (s *SQLiteStore) Insert(ctx context.Context, content string, embedding []float32, source string, instanceID, modelID int) (*Thought, error) {
	uid := uuid.New().String()

	sourceID, err := s.SourceID(ctx, source)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO thoughts (unique_id, content, source_id, instance_id) VALUES (?, ?, ?, ?)`,
		uid, content, sourceID, instanceID)
	if err != nil {
		return nil, fmt.Errorf("insert thought: %w", err)
	}

	thoughtID, _ := res.LastInsertId()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO embeddings (thought_id, model_id, vector) VALUES (?, ?, ?)`,
		thoughtID, modelID, encodeVector(embedding))
	if err != nil {
		return nil, fmt.Errorf("insert embedding: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Read back the created_at
	var createdAt string
	s.db.QueryRowContext(ctx,
		`SELECT created_at FROM thoughts WHERE id = ?`, thoughtID).Scan(&createdAt)

	t, _ := time.Parse("2006-01-02T15:04:05.000Z", createdAt)

	return &Thought{
		ID:        uid,
		Content:   content,
		Source:    source,
		CreatedAt: t,
	}, nil
}

// List returns recent thoughts.
func (s *SQLiteStore) List(ctx context.Context, limit, offset int) ([]Thought, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.unique_id, t.content, src.name, t.created_at
		 FROM thoughts t
		 JOIN sources src ON src.id = t.source_id
		 ORDER BY t.created_at DESC LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list thoughts: %w", err)
	}
	defer rows.Close()

	var thoughts []Thought
	for rows.Next() {
		var t Thought
		var createdAt string
		if err := rows.Scan(&t.ID, &t.Content, &t.Source, &createdAt); err != nil {
			return nil, fmt.Errorf("scan thought: %w", err)
		}
		t.CreatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", createdAt)
		thoughts = append(thoughts, t)
	}
	return thoughts, rows.Err()
}

// Stats returns aggregate statistics.
func (s *SQLiteStore) Stats(ctx context.Context) (*StoreStats, error) {
	var stats StoreStats
	var oldestAt, newestAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM thoughts`,
	).Scan(&stats.Total, &oldestAt, &newestAt)
	if err != nil {
		return nil, fmt.Errorf("stats query: %w", err)
	}

	if oldestAt.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05.000Z", oldestAt.String)
		stats.OldestAt = &t
	}
	if newestAt.Valid {
		t, _ := time.Parse("2006-01-02T15:04:05.000Z", newestAt.String)
		stats.NewestAt = &t
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT src.name, COUNT(*) FROM thoughts t
		 JOIN sources src ON src.id = t.source_id
		 GROUP BY src.name`)
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

// ListAllContent returns all thought IDs and content for re-embedding.
func (s *SQLiteStore) ListAllContent(ctx context.Context) ([]ThoughtRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT unique_id, content FROM thoughts ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all content: %w", err)
	}
	defer rows.Close()

	var thoughts []ThoughtRow
	for rows.Next() {
		var t ThoughtRow
		if err := rows.Scan(&t.ID, &t.Content); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		thoughts = append(thoughts, t)
	}
	return thoughts, rows.Err()
}

// UpdateEmbeddings replaces embeddings for the given thought unique_ids with the specified model.
func (s *SQLiteStore) UpdateEmbeddings(ctx context.Context, uniqueIDs []string, embeddings [][]float32, modelID int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	for i, uid := range uniqueIDs {
		var thoughtID int
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM thoughts WHERE unique_id = ?`, uid).Scan(&thoughtID); err != nil {
			return fmt.Errorf("find thought %s: %w", uid, err)
		}

		_, err := tx.ExecContext(ctx,
			`INSERT INTO embeddings (thought_id, model_id, vector) VALUES (?, ?, ?)
			 ON CONFLICT(thought_id, model_id) DO UPDATE SET vector = excluded.vector`,
			thoughtID, modelID, encodeVector(embeddings[i]))
		if err != nil {
			return fmt.Errorf("update embedding for %s: %w", uid, err)
		}
	}

	return tx.Commit()
}

// LoadEmbeddings loads all embeddings for the given model into memory.
func (s *SQLiteStore) LoadEmbeddings(ctx context.Context, modelID int) ([]string, [][]float32, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.unique_id, e.vector
		 FROM embeddings e
		 JOIN thoughts t ON t.id = e.thought_id
		 WHERE e.model_id = ?`, modelID)
	if err != nil {
		return nil, nil, fmt.Errorf("load embeddings: %w", err)
	}
	defer rows.Close()

	var ids []string
	var vectors [][]float32
	for rows.Next() {
		var uid string
		var blob []byte
		if err := rows.Scan(&uid, &blob); err != nil {
			return nil, nil, fmt.Errorf("scan embedding: %w", err)
		}
		ids = append(ids, uid)
		vectors = append(vectors, decodeVector(blob))
	}
	return ids, vectors, rows.Err()
}

// Diagnostics returns SQLite-specific diagnostic info.
func (s *SQLiteStore) Diagnostics(ctx context.Context) *SQLiteDiag {
	diag := &SQLiteDiag{Status: "connected"}

	var version string
	if err := s.db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		diag.Errors = append(diag.Errors, fmt.Sprintf("version: %v", err))
	} else {
		diag.Version = version
	}

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thoughts`).Scan(&diag.ThoughtCount); err != nil {
		diag.Errors = append(diag.Errors, fmt.Sprintf("thought count: %v", err))
	}

	// DB file size
	var dbPath string
	s.db.QueryRowContext(ctx, `PRAGMA database_list`).Scan(nil, nil, &dbPath)
	if info, err := os.Stat(dbPath); err == nil {
		diag.FileSizeBytes = info.Size()
	}

	return diag
}

// GetByUniqueIDs fetches thoughts by their unique_ids, preserving order.
func (s *SQLiteStore) GetByUniqueIDs(ctx context.Context, uids []string) ([]Thought, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(uids))
	args := make([]any, len(uids))
	for i, uid := range uids {
		placeholders[i] = "?"
		args[i] = uid
	}

	query := fmt.Sprintf(
		`SELECT t.unique_id, t.content, src.name, t.created_at
		 FROM thoughts t
		 JOIN sources src ON src.id = t.source_id
		 WHERE t.unique_id IN (%s)`,
		strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get by uids: %w", err)
	}
	defer rows.Close()

	byUID := make(map[string]Thought)
	for rows.Next() {
		var t Thought
		var createdAt string
		if err := rows.Scan(&t.ID, &t.Content, &t.Source, &createdAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		t.CreatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", createdAt)
		byUID[t.ID] = t
	}

	result := make([]Thought, 0, len(uids))
	for _, uid := range uids {
		if t, ok := byUID[uid]; ok {
			result = append(result, t)
		}
	}
	return result, rows.Err()
}

// encodeVector converts []float32 to raw bytes for BLOB storage.
func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVector converts raw bytes back to []float32.
func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
