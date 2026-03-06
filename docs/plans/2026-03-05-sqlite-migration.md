# SQLite Migration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace Postgres with SQLite for a portable, sync-capable, zero-infrastructure storage layer with decoupled embeddings and provenance tracking.

**Architecture:** Rewrite `store.go` from pgx to `database/sql` + `modernc.org/sqlite`. Add `viterin/vek` for SIMD-accelerated in-memory vector search. New schema with `sources`, `instances`, `models`, `active_model`, `thoughts`, and `embeddings` tables. Integer PKs + UUIDs for sync. `ThoughtStore` interface unchanged — `LocalBrain` and all HTTP/MCP layers untouched.

**Tech Stack:** `modernc.org/sqlite`, `viterin/vek`, `database/sql`, `github.com/google/uuid`

---

### Task 1: Swap dependencies

**Files:**
- Modify: `go.mod`

**Step 1: Add new dependencies, remove old ones**

```bash
go get modernc.org/sqlite@latest
go get github.com/viterin/vek@latest
go get github.com/google/uuid@latest
```

Do NOT remove pgx/pgvector yet — we'll do that when the store is replaced. We need them for the migration tool.

Run: `go mod tidy`

**Step 2: Verify build**

Run: `./build.sh build`
Expected: builds cleanly

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "Add modernc sqlite, viterin/vek, google/uuid dependencies"
```

---

### Task 2: New SQLite store with schema and migrations

**Files:**
- Create: `sqlite_store.go`
- Create: `sqlite_store_test.go`

This is the core replacement. The new store uses `database/sql` with the modernc sqlite driver.

**Step 1: Create `sqlite_store.go`**

```go
package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
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
// Call after Migrate. Returns the instance ID.
func (s *SQLiteStore) EnsureInstance(ctx context.Context, name, description string) (int, error) {
	// Check if a local instance already exists
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

// EnsureModel creates or retrieves a model record. Returns the model ID.
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

// UpdateEmbeddings replaces embeddings for the given thought unique_ids with the active model.
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

// LoadEmbeddings loads all embeddings for the active model into memory.
// Returns parallel slices of thought unique_ids and vectors.
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
```

**Step 2: Create `sqlite_store_test.go`**

```go
package main

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

func TestSQLiteStoreRoundTrip(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	instanceID, err := store.EnsureInstance(ctx, "test-machine", "test instance")
	if err != nil {
		t.Fatalf("EnsureInstance: %v", err)
	}

	modelID, err := store.EnsureModel(ctx, "test-model", "", 4)
	if err != nil {
		t.Fatalf("EnsureModel: %v", err)
	}
	if err := store.SetActiveModel(ctx, modelID); err != nil {
		t.Fatalf("SetActiveModel: %v", err)
	}

	// Insert
	embedding := []float32{0.5, -0.3, 0.1, 0.9}
	thought, err := store.Insert(ctx, "test thought about auth", embedding, "cli", instanceID, modelID)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if thought.Content != "test thought about auth" {
		t.Fatalf("content mismatch: %q", thought.Content)
	}
	if thought.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if thought.Source != "cli" {
		t.Fatalf("source mismatch: %q", thought.Source)
	}

	// List
	thoughts, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(thoughts) == 0 {
		t.Fatal("expected at least one thought")
	}

	// Stats
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total == 0 {
		t.Fatal("expected non-zero total")
	}
	if stats.BySource["cli"] == 0 {
		t.Fatal("expected cli source count")
	}

	// LoadEmbeddings
	ids, vectors, err := store.LoadEmbeddings(ctx, modelID)
	if err != nil {
		t.Fatalf("LoadEmbeddings: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("expected embeddings")
	}
	if len(vectors[0]) != 4 {
		t.Fatalf("expected 4-dim vector, got %d", len(vectors[0]))
	}
	if vectors[0][0] != 0.5 {
		t.Fatalf("vector mismatch: %v", vectors[0])
	}

	// ListAllContent
	rows, err := store.ListAllContent(ctx)
	if err != nil {
		t.Fatalf("ListAllContent: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected content rows")
	}

	// Diagnostics
	diag := store.Diagnostics(ctx)
	if diag.Version == "" {
		t.Fatal("expected sqlite version")
	}
	if diag.ThoughtCount == 0 {
		t.Fatal("expected non-zero thought count")
	}
}

func TestSQLiteEncodeDecodeVector(t *testing.T) {
	original := []float32{1.0, -2.5, 3.14, 0.0, -0.001}
	encoded := encodeVector(original)
	decoded := decodeVector(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: %d vs %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Fatalf("mismatch at %d: %f vs %f", i, decoded[i], original[i])
		}
	}
}

func TestSQLiteEnsureInstanceIdempotent(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	id1, err := store.EnsureInstance(ctx, "laptop", "Jim's laptop")
	if err != nil {
		t.Fatalf("first EnsureInstance: %v", err)
	}

	id2, err := store.EnsureInstance(ctx, "laptop", "Jim's laptop")
	if err != nil {
		t.Fatalf("second EnsureInstance: %v", err)
	}

	if id1 != id2 {
		t.Fatalf("expected same ID, got %d and %d", id1, id2)
	}
}
```

**Step 3: Verify build and tests**

Run: `./build.sh build`
Expected: builds cleanly

Run: `./build.sh test`
Expected: new SQLite tests pass, existing Postgres tests still pass (or skip if no Postgres)

**Step 4: Commit**

```bash
git add sqlite_store.go sqlite_store_test.go
git commit -m "Add SQLite store with new schema (sources, instances, models, embeddings)"
```

---

### Task 3: In-memory vector index

**Files:**
- Create: `vecindex.go`
- Create: `vecindex_test.go`

**Step 1: Create `vecindex.go`**

```go
package main

import (
	"sort"
	"sync"

	"github.com/viterin/vek"
)

// VecIndex holds an in-memory vector index for fast cosine similarity search.
type VecIndex struct {
	mu      sync.RWMutex
	ids     []string    // thought unique_ids
	vectors [][]float32 // parallel to ids
}

// NewVecIndex creates an empty index.
func NewVecIndex() *VecIndex {
	return &VecIndex{}
}

// Load replaces the index contents (called on startup and after re-embed).
func (idx *VecIndex) Load(ids []string, vectors [][]float32) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.ids = ids
	idx.vectors = vectors
}

// Add appends a single entry (called on capture).
func (idx *VecIndex) Add(id string, vector []float32) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.ids = append(idx.ids, id)
	idx.vectors = append(idx.vectors, vector)
}

// SearchResult pairs an ID with its similarity score.
type VecSearchResult struct {
	ID         string
	Similarity float64
}

// Search returns the top-N most similar vectors to the query.
func (idx *VecIndex) Search(query []float32, limit int) []VecSearchResult {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.vectors) == 0 {
		return nil
	}

	type scored struct {
		idx        int
		similarity float64
	}

	results := make([]scored, len(idx.vectors))
	for i, v := range idx.vectors {
		results[i] = scored{idx: i, similarity: float64(vek.CosineSimilarity(query, v))}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	if limit > len(results) {
		limit = len(results)
	}

	out := make([]VecSearchResult, limit)
	for i := 0; i < limit; i++ {
		out[i] = VecSearchResult{
			ID:         idx.ids[results[i].idx],
			Similarity: results[i].similarity,
		}
	}
	return out
}

// Len returns the number of vectors in the index.
func (idx *VecIndex) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.ids)
}
```

**Step 2: Create `vecindex_test.go`**

```go
package main

import (
	"math"
	"testing"
)

func TestVecIndexSearch(t *testing.T) {
	idx := NewVecIndex()

	// Three 4-dim vectors
	idx.Load(
		[]string{"a", "b", "c"},
		[][]float32{
			{1, 0, 0, 0},
			{0, 1, 0, 0},
			{0.9, 0.1, 0, 0}, // most similar to {1,0,0,0}
		},
	)

	results := idx.Search([]float32{1, 0, 0, 0}, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First result should be "a" (exact match)
	if results[0].ID != "a" {
		t.Fatalf("expected 'a' first, got %q", results[0].ID)
	}
	if math.Abs(results[0].Similarity-1.0) > 0.001 {
		t.Fatalf("expected similarity ~1.0, got %f", results[0].Similarity)
	}

	// Second should be "c" (close match)
	if results[1].ID != "c" {
		t.Fatalf("expected 'c' second, got %q", results[1].ID)
	}
}

func TestVecIndexAdd(t *testing.T) {
	idx := NewVecIndex()
	idx.Add("x", []float32{1, 0, 0})
	idx.Add("y", []float32{0, 1, 0})

	if idx.Len() != 2 {
		t.Fatalf("expected 2, got %d", idx.Len())
	}

	results := idx.Search([]float32{1, 0, 0}, 1)
	if results[0].ID != "x" {
		t.Fatalf("expected 'x', got %q", results[0].ID)
	}
}

func TestVecIndexEmpty(t *testing.T) {
	idx := NewVecIndex()
	results := idx.Search([]float32{1, 0, 0}, 5)
	if len(results) != 0 {
		t.Fatalf("expected 0 results from empty index, got %d", len(results))
	}
}
```

**Step 3: Verify**

Run: `./build.sh test`
Expected: all tests pass

**Step 4: Commit**

```bash
git add vecindex.go vecindex_test.go
git commit -m "Add SIMD-accelerated in-memory vector index using viterin/vek"
```

---

### Task 4: Update diagnostics for SQLite

**Files:**
- Modify: `diagnostics.go`

**Step 1: Add SQLiteDiag type alongside PostgresDiag**

Add to `diagnostics.go`:

```go
// SQLiteDiag contains diagnostics for the SQLite database.
type SQLiteDiag struct {
	Version       string   `json:"version"`
	Status        string   `json:"status"`
	ThoughtCount  int      `json:"thought_count"`
	FileSizeBytes int64    `json:"file_size_bytes,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}
```

Add a `SQLite` field to `DiagnosticsReport`:

```go
type DiagnosticsReport struct {
	Mode      BrainMode     `json:"mode"`
	StartedAt time.Time     `json:"started_at"`
	Uptime    string        `json:"uptime"`
	GoVersion string        `json:"go_version"`
	Postgres  *PostgresDiag `json:"postgres,omitempty"`
	SQLite    *SQLiteDiag   `json:"sqlite,omitempty"`
	Ollama    *OllamaDiag   `json:"ollama,omitempty"`
	Remote    *RemoteDiag   `json:"remote,omitempty"`
	MDNS      *MDNSDiag     `json:"mdns,omitempty"`
}
```

**Step 2: Verify**

Run: `./build.sh build`
Expected: builds cleanly

**Step 3: Commit**

```bash
git add diagnostics.go
git commit -m "Add SQLiteDiag type to diagnostics report"
```

---

### Task 5: Update config for SQLite

**Files:**
- Modify: `config.go`

**Step 1: Change DatabaseConfig**

Replace:
```go
type DatabaseConfig struct {
	URL string `toml:"url"`
}
```

With:
```go
type DatabaseConfig struct {
	Path string `toml:"path"` // SQLite database file path
	URL  string `toml:"url"`  // Legacy Postgres URL (for migration only)
}
```

Keep both fields — `Path` for the new SQLite store, `URL` retained so the migration tool can read the old Postgres URL from the same config.

**Step 2: Verify**

Run: `./build.sh build`
Expected: builds cleanly

**Step 3: Commit**

```bash
git add config.go
git commit -m "Add database.path config for SQLite, retain url for migration"
```

---

### Task 6: Wire LocalBrain to SQLiteStore + VecIndex

**Files:**
- Modify: `brain.go`

**Step 1: Update LocalBrain to use SQLiteStore and VecIndex**

The `LocalBrain` currently holds `*Store` (Postgres). Change it to hold `*SQLiteStore` and `*VecIndex`. Update all methods.

Replace the LocalBrain struct and methods:

```go
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

func (b *LocalBrain) Capture(ctx context.Context, content string, source string) (*Thought, error) {
	embedding, err := b.ollama.Embed(ctx, content)
	if err != nil {
		return nil, err
	}
	thought, err := b.store.Insert(ctx, content, embedding, source, b.instanceID, b.modelID)
	if err != nil {
		return nil, err
	}
	// Update in-memory index
	b.vecIndex.Add(thought.ID, embedding)
	return thought, nil
}

func (b *LocalBrain) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	embedding, err := b.ollama.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	vecResults := b.vecIndex.Search(embedding, limit)

	results := make([]SearchResult, 0, len(vecResults))
	for _, vr := range vecResults {
		// Look up the thought by unique_id
		thoughts, err := b.store.List(ctx, 1, 0) // We need a GetByUID method
		_ = thoughts
		_ = err
		// TODO: This needs a store.GetByUniqueID method — add it
	}

	// ALTERNATIVE: simpler approach — store enough in VecIndex to avoid DB round-trip for search
	// Actually, we should just do an IN query. See implementation notes below.
	return results, nil
}
```

**IMPORTANT NOTE:** The Search method needs a way to fetch thoughts by unique_id. Add a `GetByUniqueIDs` method to `SQLiteStore`:

```go
// GetByUniqueIDs fetches thoughts by their unique_ids, preserving order.
func (s *SQLiteStore) GetByUniqueIDs(ctx context.Context, uids []string) ([]Thought, error) {
	if len(uids) == 0 {
		return nil, nil
	}

	// Build placeholders
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

	// Preserve order from input
	result := make([]Thought, 0, len(uids))
	for _, uid := range uids {
		if t, ok := byUID[uid]; ok {
			result = append(result, t)
		}
	}
	return result, rows.Err()
}
```

Then the real Search becomes:

```go
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
```

Complete the remaining methods:

```go
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
```

Add `"strings"` to imports in `sqlite_store.go` for `GetByUniqueIDs`.

**Step 2: Verify**

Run: `./build.sh build`
Expected: builds cleanly

**Step 3: Commit**

```bash
git add brain.go sqlite_store.go
git commit -m "Wire LocalBrain to SQLiteStore + VecIndex"
```

---

### Task 7: Update main.go serve and mcp commands

**Files:**
- Modify: `main.go`

**Step 1: Update ServeCmd.Execute**

Replace the Postgres store init with SQLite:

```go
func (cmd *ServeCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	ctx := context.Background()

	store, err := NewSQLiteStore(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	hostname, _ := os.Hostname()
	instanceID, err := store.EnsureInstance(ctx, hostname, "")
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

	ollama := NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)

	// Load vector index
	vecIndex := NewVecIndex()
	ids, vectors, err := store.LoadEmbeddings(ctx, modelID)
	if err != nil {
		return fmt.Errorf("load embeddings: %w", err)
	}
	vecIndex.Load(ids, vectors)
	fmt.Fprintf(os.Stderr, "Loaded %d embeddings into memory\n", vecIndex.Len())

	brain := NewLocalBrain(store, ollama, vecIndex, instanceID, modelID)
	srv := NewServer(brain)

	// mDNS advertising (unchanged)
	// ... existing mDNS code ...

	fmt.Printf("Starting server on %s\n", cfg.Server.Addr)
	return http.ListenAndServe(cfg.Server.Addr, srv)
}
```

**Step 2: Update MCPCmd.Execute (local mode only)**

Same pattern as serve — use SQLiteStore + VecIndex instead of Postgres Store.

**Step 3: Update ReEmbedCmd.Execute**

Change from `NewStore` (Postgres) to `NewSQLiteStore`. Update `UpdateEmbeddings` call to include `modelID`. After re-embed completes, the vector index reload happens on next serve start.

**Step 4: Verify**

Run: `./build.sh build`
Expected: builds cleanly

**Step 5: Commit**

```bash
git add main.go
git commit -m "Switch serve, mcp, and re-embed commands to SQLite"
```

---

### Task 8: Update setup wizard

**Files:**
- Modify: `setup.go`

**Step 1: Replace Postgres setup with database path prompt**

Delete: `setupPostgres`, `setupRemotePostgres`, `setupLocalPostgres`, `isPostgresRunning`, `checkPsqlConnection`, `checkPgvectorInstalled`, `userExists`, `dbExists`, `createDBUser`, `createDatabase`, `enablePgvector` — all of them.

Replace with:

```go
func (cmd *SetupCmd) setupDatabase(cfg *Config) error {
	printHeader("Database")
	fmt.Fprintln(os.Stderr, "  Thought Store uses SQLite — no server required.")

	path, err := inputPrompt("Database file path", "thought-store.db")
	if err != nil {
		return err
	}
	cfg.Database.Path = path
	printSuccess(fmt.Sprintf("Database will be stored at %s", path))
	return nil
}
```

Update `Execute()` to call `setupDatabase(cfg)` instead of `setupPostgres(cfg, pm)`.

Remove `"strings"` import if no longer needed.

**Step 2: Verify**

Run: `./build.sh build`
Expected: builds cleanly

**Step 3: Commit**

```bash
git add setup.go
git commit -m "Simplify setup wizard: replace Postgres provisioning with SQLite path"
```

---

### Task 9: Update tests

**Files:**
- Modify: `e2e_test.go`
- Modify: `reembed_test.go`
- Delete: `store_test.go` (replaced by `sqlite_store_test.go` from Task 2)

**Step 1: Rewrite e2e_test.go to use SQLite**

Replace the Postgres store init with SQLite. Remove Postgres skip check. Use `t.TempDir()` for the DB file. Update diagnostics assertions: check `diag.SQLite` instead of `diag.Postgres`, remove HNSW index check.

**Step 2: Rewrite reembed_test.go to use SQLite**

Replace Postgres store init. Update `UpdateEmbeddings` call to include `modelID`. Use `t.TempDir()`.

**Step 3: Delete store_test.go**

The old Postgres-based `TestStoreRoundTrip` is replaced by `TestSQLiteStoreRoundTrip` in `sqlite_store_test.go`.

**Step 4: Verify**

Run: `./build.sh test`
Expected: ALL tests pass. Tests no longer require a running Postgres.

**Step 5: Commit**

```bash
git add e2e_test.go reembed_test.go
git rm store_test.go
git commit -m "Migrate all tests from Postgres to SQLite"
```

---

### Task 10: Postgres migration tool

**Files:**
- Create: `migrate_pg.go`

**Step 1: Create `migrate_pg.go`**

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
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
	pgPool, err := pgxpool.New(ctx, cmd.PostgresURL)
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
		var embedding []float32
		var createdAt time.Time

		if err := rows.Scan(&pgID, &content, &source, &embedding, &createdAt); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		// Get source ID
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

		// Get the thought's integer ID
		var thoughtIntID int
		store.db.QueryRowContext(ctx,
			`SELECT id FROM thoughts WHERE unique_id = ?`, pgID).Scan(&thoughtIntID)

		// Insert embedding
		_, err = store.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO embeddings (thought_id, model_id, vector) VALUES (?, ?, ?)`,
			thoughtIntID, modelID, encodeVector(embedding))
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
```

Add the subcommand to `main.go` opts:

```go
MigrateFromPG MigrateFromPostgresCmd `command:"migrate-from-postgres" description:"Import thoughts from a Postgres database"`
```

**Step 2: Verify**

Run: `./build.sh build`
Expected: builds cleanly

**Step 3: Commit**

```bash
git add migrate_pg.go main.go
git commit -m "Add migrate-from-postgres subcommand"
```

---

### Task 11: Remove old Postgres store

**Files:**
- Delete: `store.go`
- Modify: `go.mod` (remove pgx, pgvector-go if no longer needed)

**Step 1: Delete store.go**

The old Postgres store is fully replaced by `sqlite_store.go`. Delete it.

Note: `migrate_pg.go` still imports `pgx` directly — that's fine, it's the migration tool.

**Step 2: Run go mod tidy**

`pgvector-go` may still be needed by `migrate_pg.go` for scanning the `embedding` column from Postgres. If not, it'll get cleaned up by tidy.

Run: `go mod tidy`

**Step 3: Verify**

Run: `./build.sh build && ./build.sh test`
Expected: all pass

**Step 4: Commit**

```bash
git rm store.go
git add go.mod go.sum
git commit -m "Remove old Postgres store (replaced by SQLite)"
```

---

### Task 12: Update config file and documentation

**Files:**
- Modify: `thought-store.toml`
- Modify: `CLAUDE.md`
- Modify: `README.md`

**Step 1: Update thought-store.toml**

Add `path` to `[database]` section:

```toml
[database]
path = "thought-store.db"
url = "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable"
```

Keep `url` for the migration tool. After migration it can be removed.

**Step 2: Update CLAUDE.md**

- Architecture: replace `store.go Postgres + pgvector` with `sqlite_store.go SQLite + in-memory vector search`, add `vecindex.go`, `migrate_pg.go`
- Key patterns: update storage description, add vector index pattern
- Dependencies: replace pgx/pgvector with modernc.org/sqlite, viterin/vek
- Config: mention `[database] path`
- Tests: update test descriptions

**Step 3: Update README.md**

- Prerequisites: remove Postgres, keep Ollama
- Stack: mention SQLite instead of Postgres
- Config: show `path` instead of `url`
- Add Migration section for `migrate-from-postgres`

**Step 4: Commit**

```bash
git add thought-store.toml CLAUDE.md README.md
git commit -m "Update config and docs for SQLite migration"
```

---

## Summary

| Task | Description | Files |
|------|-------------|-------|
| 1 | Add new dependencies | go.mod, go.sum |
| 2 | SQLite store with new schema | sqlite_store.go, sqlite_store_test.go |
| 3 | In-memory vector index | vecindex.go, vecindex_test.go |
| 4 | SQLiteDiag type | diagnostics.go |
| 5 | Config: database.path | config.go |
| 6 | Wire LocalBrain to SQLite + VecIndex | brain.go, sqlite_store.go |
| 7 | Switch serve/mcp/re-embed to SQLite | main.go |
| 8 | Simplify setup wizard | setup.go |
| 9 | Migrate all tests to SQLite | e2e_test.go, reembed_test.go, -store_test.go |
| 10 | Postgres migration tool | migrate_pg.go, main.go |
| 11 | Remove old Postgres store | -store.go, go.mod |
| 12 | Update config and docs | thought-store.toml, CLAUDE.md, README.md |
