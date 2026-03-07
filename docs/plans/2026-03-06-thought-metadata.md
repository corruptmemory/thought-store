# Thought Metadata Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add metadata tables (labels + associations) so thoughts can be tagged with people, dates, topics, and action items — surfaced in API/MCP responses, synced between peers.

**Architecture:** Two new tables: `metadata` (content-addressed labels with type+value, deduplicated by UNIQUE constraint) and `thought_metadata` (UUID-tracked associations between thoughts and metadata, synced like thoughts). MetadataType fake enum. Metadata loaded via a second query after fetching thoughts. ForgetThought deletes associations. Sync imports metadata labels then associations with ID remapping.

**Tech Stack:** Go, SQLite (modernc.org/sqlite), existing test patterns (t.TempDir, table-driven)

---

### Task 1: MetadataType enum and types

**Files:**
- Modify: `types.go`

**Step 1: Write the failing test**

Create `metadata_test.go`:

```go
package main

import "testing"

func TestMetadataTypeString(t *testing.T) {
	tests := []struct {
		mt   MetadataType
		want string
	}{
		{MetadataPerson, "person"},
		{MetadataDate, "date"},
		{MetadataTag, "tag"},
		{MetadataActionItem, "action_item"},
		{MetadataType(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.mt.String(); got != tt.want {
			t.Errorf("MetadataType(%d).String() = %q, want %q", tt.mt, got, tt.want)
		}
	}
}

func TestMetadataTypeMarshalText(t *testing.T) {
	b, err := MetadataTag.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "tag" {
		t.Errorf("MarshalText = %q, want %q", b, "tag")
	}
}

func TestMetadataTypeUnmarshalText(t *testing.T) {
	tests := []struct {
		text string
		want MetadataType
	}{
		{"person", MetadataPerson},
		{"date", MetadataDate},
		{"tag", MetadataTag},
		{"action_item", MetadataActionItem},
		{"bogus", MetadataPerson},
	}
	for _, tt := range tests {
		var got MetadataType
		if err := got.UnmarshalText([]byte(tt.text)); err != nil {
			t.Fatalf("UnmarshalText(%q): %v", tt.text, err)
		}
		if got != tt.want {
			t.Errorf("UnmarshalText(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `MetadataType` not defined

**Step 3: Write minimal implementation**

Add to `types.go`:

```go
// MetadataType classifies a metadata entry.
// IMPORTANT: These integer values are stored in the database.
// Order must be preserved forever. You can only add new values, never delete or reorder.
type MetadataType int

const (
	MetadataPerson     MetadataType = iota // 0 — person mentioned
	MetadataDate                           // 1 — date referenced (YYYY-MM-DD)
	MetadataTag                            // 2 — topic tag
	MetadataActionItem                     // 3 — implied to-do
)

func (m MetadataType) String() string {
	switch m {
	case MetadataPerson:
		return "person"
	case MetadataDate:
		return "date"
	case MetadataTag:
		return "tag"
	case MetadataActionItem:
		return "action_item"
	default:
		return "unknown"
	}
}

func (m MetadataType) MarshalText() ([]byte, error) {
	return []byte(m.String()), nil
}

func (m *MetadataType) UnmarshalText(text []byte) error {
	switch string(text) {
	case "person":
		*m = MetadataPerson
	case "date":
		*m = MetadataDate
	case "tag":
		*m = MetadataTag
	case "action_item":
		*m = MetadataActionItem
	default:
		*m = MetadataPerson
	}
	return nil
}
```

Also add the metadata entry type and update Thought struct:

```go
// ThoughtMetadataEntry is a single metadata tag on a thought.
type ThoughtMetadataEntry struct {
	Type  MetadataType `json:"type"`
	Value string       `json:"value"`
}

// Thought represents a single captured thought.
type Thought struct {
	ID        string                 `json:"id"`
	Content   string                 `json:"content"`
	Source    string                 `json:"source"`
	State    ThoughtState           `json:"state"`
	Metadata []ThoughtMetadataEntry `json:"metadata,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
}
```

Also add the append-only comment to ThoughtState:

```go
// ThoughtState represents the lifecycle state of a thought.
// IMPORTANT: These integer values are stored in the database.
// Order must be preserved forever. You can only add new values, never delete or reorder.
type ThoughtState int
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add types.go metadata_test.go
git commit -m "feat: add MetadataType enum and ThoughtMetadataEntry type"
```

---

### Task 2: Schema migration — metadata and thought_metadata tables

**Files:**
- Modify: `sqlite_store.go` (Migrate method, line 53)

**Step 1: Write the failing test**

Add to `metadata_test.go`:

```go
import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateCreatesMetadataTables(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migrate-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify metadata table exists
	var name string
	err = store.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='metadata'`).Scan(&name)
	if err != nil {
		t.Fatalf("metadata table not found: %v", err)
	}

	// Verify thought_metadata table exists
	err = store.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='thought_metadata'`).Scan(&name)
	if err != nil {
		t.Fatalf("thought_metadata table not found: %v", err)
	}

	// Verify unique constraint on metadata
	_, err = store.db.ExecContext(ctx,
		`INSERT INTO metadata (metadata_type, value) VALUES (0, 'Sarah')`)
	if err != nil {
		t.Fatalf("insert metadata: %v", err)
	}
	_, err = store.db.ExecContext(ctx,
		`INSERT INTO metadata (metadata_type, value) VALUES (0, 'Sarah')`)
	if err == nil {
		t.Fatal("expected unique constraint violation on duplicate metadata")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `metadata table not found`

**Step 3: Write minimal implementation**

In `sqlite_store.go`, add two CREATE TABLE statements to the `statements` slice in `Migrate()`, after the `embeddings` table (before the closing `}`):

```go
		`CREATE TABLE IF NOT EXISTS metadata (
			id            INTEGER PRIMARY KEY,
			metadata_type INTEGER NOT NULL,
			value         TEXT NOT NULL,
			UNIQUE(metadata_type, value)
		)`,
		`CREATE TABLE IF NOT EXISTS thought_metadata (
			id          INTEGER PRIMARY KEY,
			unique_id   TEXT NOT NULL UNIQUE,
			thought_id  INTEGER NOT NULL REFERENCES thoughts(id),
			metadata_id INTEGER NOT NULL REFERENCES metadata(id),
			instance_id INTEGER NOT NULL REFERENCES instances(id)
		)`,
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go metadata_test.go
git commit -m "feat: add metadata and thought_metadata tables to schema"
```

---

### Task 3: EnsureMetadata and AddThoughtMetadata store methods

**Files:**
- Modify: `sqlite_store.go`

**Step 1: Write the failing test**

Add to `metadata_test.go`:

```go
func TestEnsureMetadataIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ensure-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)

	id1, err := store.EnsureMetadata(ctx, MetadataPerson, "Sarah")
	if err != nil {
		t.Fatalf("EnsureMetadata: %v", err)
	}

	id2, err := store.EnsureMetadata(ctx, MetadataPerson, "Sarah")
	if err != nil {
		t.Fatalf("EnsureMetadata second: %v", err)
	}

	if id1 != id2 {
		t.Fatalf("expected same ID, got %d and %d", id1, id2)
	}

	// Different type, same value — should be different
	id3, err := store.EnsureMetadata(ctx, MetadataTag, "Sarah")
	if err != nil {
		t.Fatalf("EnsureMetadata tag: %v", err)
	}
	if id3 == id1 {
		t.Fatal("different type should produce different ID")
	}
}

func TestAddThoughtMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "add-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, _ := store.Insert(ctx, "met Sarah today", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)

	err = store.AddThoughtMetadata(ctx, thought.ID, MetadataPerson, "Sarah", instID)
	if err != nil {
		t.Fatalf("AddThoughtMetadata: %v", err)
	}

	err = store.AddThoughtMetadata(ctx, thought.ID, MetadataTag, "meeting", instID)
	if err != nil {
		t.Fatalf("AddThoughtMetadata tag: %v", err)
	}

	// Verify rows exist
	var count int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thought_metadata`).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 thought_metadata rows, got %d", count)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `store.EnsureMetadata` undefined

**Step 3: Write minimal implementation**

Add to `sqlite_store.go`:

```go
// EnsureMetadata creates or retrieves a metadata label.
// Content-addressed: deduplicated by (metadata_type, value).
func (s *SQLiteStore) EnsureMetadata(ctx context.Context, mt MetadataType, value string) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM metadata WHERE metadata_type = ? AND value = ?`, mt, value).Scan(&id)
	if err == nil {
		return id, nil
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO metadata (metadata_type, value) VALUES (?, ?)`, mt, value)
	if err != nil {
		return 0, fmt.Errorf("create metadata: %w", err)
	}
	insertedID, _ := res.LastInsertId()
	return int(insertedID), nil
}

// AddThoughtMetadata associates a metadata label with a thought.
// Creates the metadata label if it doesn't exist. Generates a UUID for the association.
func (s *SQLiteStore) AddThoughtMetadata(ctx context.Context, thoughtUID string, mt MetadataType, value string, instanceID int) error {
	metaID, err := s.EnsureMetadata(ctx, mt, value)
	if err != nil {
		return err
	}

	var thoughtID int
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM thoughts WHERE unique_id = ?`, thoughtUID).Scan(&thoughtID)
	if err != nil {
		return fmt.Errorf("find thought %s: %w", thoughtUID, err)
	}

	uid := uuid.New().String()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO thought_metadata (unique_id, thought_id, metadata_id, instance_id) VALUES (?, ?, ?, ?)`,
		uid, thoughtID, metaID, instanceID)
	if err != nil {
		return fmt.Errorf("add thought metadata: %w", err)
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go metadata_test.go
git commit -m "feat: add EnsureMetadata and AddThoughtMetadata store methods"
```

---

### Task 4: LoadThoughtMetadata — fetch metadata for thoughts

**Files:**
- Modify: `sqlite_store.go`

**Step 1: Write the failing test**

Add to `metadata_test.go`:

```go
func TestLoadThoughtMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "load-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	t1, _ := store.Insert(ctx, "met Sarah", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	t2, _ := store.Insert(ctx, "no metadata", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	store.AddThoughtMetadata(ctx, t1.ID, MetadataPerson, "Sarah", instID)
	store.AddThoughtMetadata(ctx, t1.ID, MetadataTag, "meeting", instID)

	meta, err := store.LoadThoughtMetadata(ctx, []string{t1.ID, t2.ID})
	if err != nil {
		t.Fatalf("LoadThoughtMetadata: %v", err)
	}

	entries := meta[t1.ID]
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for t1, got %d", len(entries))
	}

	// t2 should have no metadata
	if len(meta[t2.ID]) != 0 {
		t.Fatalf("expected 0 entries for t2, got %d", len(meta[t2.ID]))
	}
}

func TestLoadThoughtMetadataEmpty(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "load-meta-empty.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)

	meta, err := store.LoadThoughtMetadata(ctx, nil)
	if err != nil {
		t.Fatalf("LoadThoughtMetadata nil: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil map")
	}
	if len(meta) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(meta))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `store.LoadThoughtMetadata` undefined

**Step 3: Write minimal implementation**

Add to `sqlite_store.go`:

```go
// LoadThoughtMetadata returns metadata entries grouped by thought unique_id.
func (s *SQLiteStore) LoadThoughtMetadata(ctx context.Context, thoughtUIDs []string) (map[string][]ThoughtMetadataEntry, error) {
	result := make(map[string][]ThoughtMetadataEntry)
	if len(thoughtUIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(thoughtUIDs))
	args := make([]any, len(thoughtUIDs))
	for i, uid := range thoughtUIDs {
		placeholders[i] = "?"
		args[i] = uid
	}

	query := fmt.Sprintf(
		`SELECT t.unique_id, m.metadata_type, m.value
		 FROM thought_metadata tm
		 JOIN thoughts t ON t.id = tm.thought_id
		 JOIN metadata m ON m.id = tm.metadata_id
		 WHERE t.unique_id IN (%s)
		 ORDER BY t.unique_id, m.metadata_type, m.value`,
		strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load thought metadata: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var thoughtUID string
		var mt int
		var value string
		if err := rows.Scan(&thoughtUID, &mt, &value); err != nil {
			return nil, fmt.Errorf("scan metadata: %w", err)
		}
		result[thoughtUID] = append(result[thoughtUID], ThoughtMetadataEntry{
			Type:  MetadataType(mt),
			Value: value,
		})
	}
	return result, rows.Err()
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go metadata_test.go
git commit -m "feat: add LoadThoughtMetadata for batch metadata retrieval"
```

---

### Task 5: Populate metadata in List and GetByUniqueIDs

**Files:**
- Modify: `sqlite_store.go` (List at line 240, GetByUniqueIDs at line 409)

**Step 1: Write the failing test**

Add to `metadata_test.go`:

```go
func TestListIncludesMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "list-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, _ := store.Insert(ctx, "met Sarah about Go", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.AddThoughtMetadata(ctx, thought.ID, MetadataPerson, "Sarah", instID)
	store.AddThoughtMetadata(ctx, thought.ID, MetadataTag, "Go", instID)

	thoughts, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(thoughts) != 1 {
		t.Fatalf("expected 1 thought, got %d", len(thoughts))
	}
	if len(thoughts[0].Metadata) != 2 {
		t.Fatalf("expected 2 metadata entries, got %d", len(thoughts[0].Metadata))
	}
}

func TestGetByUniqueIDsIncludesMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "getbyuid-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, _ := store.Insert(ctx, "tagged thought", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.AddThoughtMetadata(ctx, thought.ID, MetadataTag, "architecture", instID)

	thoughts, err := store.GetByUniqueIDs(ctx, []string{thought.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(thoughts) != 1 {
		t.Fatalf("expected 1 thought, got %d", len(thoughts))
	}
	if len(thoughts[0].Metadata) != 1 {
		t.Fatalf("expected 1 metadata entry, got %d", len(thoughts[0].Metadata))
	}
	if thoughts[0].Metadata[0].Value != "architecture" {
		t.Fatalf("expected 'architecture', got %q", thoughts[0].Metadata[0].Value)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `Metadata` field is nil/empty (not populated)

**Step 3: Write minimal implementation**

Add a helper function to `sqlite_store.go` that populates metadata on a slice of thoughts:

```go
// populateMetadata loads and attaches metadata to a slice of thoughts.
func (s *SQLiteStore) populateMetadata(ctx context.Context, thoughts []Thought) error {
	if len(thoughts) == 0 {
		return nil
	}
	uids := make([]string, len(thoughts))
	for i, t := range thoughts {
		uids[i] = t.ID
	}
	meta, err := s.LoadThoughtMetadata(ctx, uids)
	if err != nil {
		return err
	}
	for i := range thoughts {
		if entries, ok := meta[thoughts[i].ID]; ok {
			thoughts[i].Metadata = entries
		}
	}
	return nil
}
```

Then add a call to `populateMetadata` at the end of `List()`, before the return:

```go
	// existing code...
	// after the rows.Next() loop and rows.Err() check:
	if err := s.populateMetadata(ctx, thoughts); err != nil {
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	return thoughts, nil
```

And the same at the end of `GetByUniqueIDs()`, before the return:

```go
	// after building the result slice:
	if err := s.populateMetadata(ctx, result); err != nil {
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	return result, nil
```

Note: You need to adjust both methods — `List()` currently returns `thoughts, rows.Err()` and `GetByUniqueIDs()` returns `result, rows.Err()`. You need to capture the `rows.Err()` first, check it, then call `populateMetadata`, then return. For example in `List()`:

```go
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.populateMetadata(ctx, thoughts); err != nil {
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	return thoughts, nil
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go metadata_test.go
git commit -m "feat: populate metadata in List and GetByUniqueIDs results"
```

---

### Task 6: ForgetThought deletes metadata associations

**Files:**
- Modify: `sqlite_store.go` (ForgetThought at line 530)

**Step 1: Write the failing test**

Add to `metadata_test.go`:

```go
func TestForgetThoughtDeletesMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "forget-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, _ := store.Insert(ctx, "tagged thought", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.AddThoughtMetadata(ctx, thought.ID, MetadataPerson, "Sarah", instID)
	store.AddThoughtMetadata(ctx, thought.ID, MetadataTag, "meeting", instID)

	// Verify metadata exists
	var count int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thought_metadata`).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 metadata rows before forget, got %d", count)
	}

	if err := store.ForgetThought(ctx, thought.ID); err != nil {
		t.Fatalf("ForgetThought: %v", err)
	}

	// Metadata associations should be deleted
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thought_metadata`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 metadata rows after forget, got %d", count)
	}

	// The metadata labels should still exist (they're content-addressed, shared)
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata`).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 metadata labels to remain, got %d", count)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `thought_metadata` rows still exist after ForgetThought

**Step 3: Write minimal implementation**

In `ForgetThought()`, add a DELETE for `thought_metadata` after the existing DELETE for `embeddings` (line 554) and before `return tx.Commit()`:

```go
	_, err = tx.ExecContext(ctx,
		`DELETE FROM thought_metadata WHERE thought_id = ?`, thoughtID)
	if err != nil {
		return fmt.Errorf("delete thought metadata: %w", err)
	}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go metadata_test.go
git commit -m "feat: ForgetThought deletes metadata associations"
```

---

### Task 7: Sync import — metadata labels and associations

**Files:**
- Modify: `sync.go`

**Step 1: Write the failing test**

Add to `sync_test.go`:

```go
func TestSyncImportsMetadata(t *testing.T) {
	ctx := context.Background()

	storeA, dbPathA, instA, modelA := newTestStoreAt(t, "machine-A")
	storeB, _, instB, modelB := newTestStoreAt(t, "machine-B")

	// A captures a thought with metadata
	thought, _ := storeA.Insert(ctx, "met Sarah about Go", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instA, modelA)
	storeA.AddThoughtMetadata(ctx, thought.ID, MetadataPerson, "Sarah", instA)
	storeA.AddThoughtMetadata(ctx, thought.ID, MetadataTag, "Go", instA)

	// B pulls from A
	uidB, _ := storeB.InstanceUID(ctx, instB)
	bundle := exportToFile(t, ctx, storeA, dbPathA)
	result := importFromFile(t, ctx, storeB, uidB, modelB, bundle)
	if result.NewThoughts != 1 {
		t.Fatalf("expected 1 new thought, got %d", result.NewThoughts)
	}

	// B should have the metadata
	meta, err := storeB.LoadThoughtMetadata(ctx, []string{thought.ID})
	if err != nil {
		t.Fatalf("LoadThoughtMetadata: %v", err)
	}
	entries := meta[thought.ID]
	if len(entries) != 2 {
		t.Fatalf("expected 2 metadata entries, got %d", len(entries))
	}

	// Import again — should be idempotent
	bundle2 := exportToFile(t, ctx, storeA, dbPathA)
	importFromFile(t, ctx, storeB, uidB, modelB, bundle2)

	// Still only 2 metadata associations
	var count int
	storeB.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thought_metadata`).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 thought_metadata rows after re-import, got %d", count)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `entries` is empty (no metadata synced)

**Step 3: Write minimal implementation**

Add a new Step 5 in `ImportBundle()` in `sync.go`, after the forgotten propagation (Step 4) and before `return result, nil`:

```go
	// Step 5: Import metadata labels and associations
	if err := importMetadata(ctx, store, foreignStore, myInstanceUID, instanceMap); err != nil {
		return nil, fmt.Errorf("import metadata: %w", err)
	}
```

Add the `importMetadata` function:

```go
// importMetadata imports metadata labels and thought_metadata associations from the foreign DB.
func importMetadata(ctx context.Context, localStore, foreignStore *SQLiteStore, myInstanceUID string, instanceMap map[string]int) error {
	// Step 5a: Import metadata labels (content-addressed, dedup by type+value)
	metaMap := make(map[int]int) // foreign metadata.id -> local metadata.id

	metaRows, err := foreignStore.db.QueryContext(ctx,
		`SELECT id, metadata_type, value FROM metadata`)
	if err != nil {
		return fmt.Errorf("query foreign metadata: %w", err)
	}
	defer metaRows.Close()

	for metaRows.Next() {
		var foreignID, mt int
		var value string
		if err := metaRows.Scan(&foreignID, &mt, &value); err != nil {
			return fmt.Errorf("scan metadata: %w", err)
		}
		localID, err := localStore.EnsureMetadata(ctx, MetadataType(mt), value)
		if err != nil {
			return fmt.Errorf("ensure metadata: %w", err)
		}
		metaMap[foreignID] = localID
	}
	if err := metaRows.Err(); err != nil {
		return fmt.Errorf("metadata rows: %w", err)
	}

	// Step 5b: Import thought_metadata associations (UUID-tracked)
	tmRows, err := foreignStore.db.QueryContext(ctx,
		`SELECT tm.unique_id, t.unique_id, tm.metadata_id, i.unique_id
		 FROM thought_metadata tm
		 JOIN thoughts t ON t.id = tm.thought_id
		 JOIN instances i ON i.id = tm.instance_id`)
	if err != nil {
		return fmt.Errorf("query foreign thought_metadata: %w", err)
	}
	defer tmRows.Close()

	for tmRows.Next() {
		var tmUID, thoughtUID, instanceUID string
		var foreignMetaID int
		if err := tmRows.Scan(&tmUID, &thoughtUID, &foreignMetaID, &instanceUID); err != nil {
			return fmt.Errorf("scan thought_metadata: %w", err)
		}

		// Skip if already imported
		var exists int
		if localStore.db.QueryRowContext(ctx,
			`SELECT 1 FROM thought_metadata WHERE unique_id = ?`, tmUID).Scan(&exists) == nil {
			continue
		}

		// Resolve local IDs
		var localThoughtID int
		if localStore.db.QueryRowContext(ctx,
			`SELECT id FROM thoughts WHERE unique_id = ?`, thoughtUID).Scan(&localThoughtID) != nil {
			continue // thought not imported (maybe from our own instance)
		}

		localMetaID, ok := metaMap[foreignMetaID]
		if !ok {
			continue
		}

		localInstanceID, ok := instanceMap[instanceUID]
		if !ok {
			continue
		}

		_, err := localStore.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO thought_metadata (unique_id, thought_id, metadata_id, instance_id) VALUES (?, ?, ?, ?)`,
			tmUID, localThoughtID, localMetaID, localInstanceID)
		if err != nil {
			return fmt.Errorf("import thought_metadata %s: %w", tmUID, err)
		}
	}
	return tmRows.Err()
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sync.go sync_test.go
git commit -m "feat: sync metadata labels and associations between peers"
```

---

### Task 8: Add enum stability comments and update docs

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`
- Modify: `diagnostics.go` (BrainMode enum, line 10)

**Step 1: Add enum stability comment to BrainMode**

In `diagnostics.go`, add comment before `BrainMode`:

```go
// BrainMode indicates whether the server is running with a local or remote brain.
// IMPORTANT: These integer values are stored in the database.
// Order must be preserved forever. You can only add new values, never delete or reorder.
type BrainMode int
```

Note: `ThoughtState` and `MetadataType` already have this comment from earlier tasks.

**Step 2: Update CLAUDE.md**

Add to the "Key patterns" section, after the thought state bullet:

```
- **Thought metadata**: Two-table design — `metadata` (content-addressed labels, deduplicated by type+value) and `thought_metadata` (UUID-tracked associations for sync). `MetadataType` enum: person, date, tag, actionItem. Metadata is populated externally (future brain layer), not at capture time. ForgetThought deletes associations. Sync imports labels then associations with ID remapping.
```

Add to the "Key patterns" section (or as a standalone note):

```
- **Enum stability**: All integer enums stored in the DB (`ThoughtState`, `MetadataType`, `BrainMode`) are append-only. Order must be preserved forever. You can only add new values, never delete or reorder.
```

Update the architecture section for `sqlite_store.go`:

```
sqlite_store.go   SQLite store (insert, list, search, embeddings, metadata, import, forget)
```

Update the tests section:

```
- `metadata_test.go` — MetadataType enum, schema migration, EnsureMetadata, AddThoughtMetadata, LoadThoughtMetadata, metadata in List/GetByUniqueIDs, ForgetThought deletes metadata
```

**Step 3: Update README.md**

Add a "Metadata" bullet to the "What it does" section:

```
- **Tag** thoughts with people, dates, topics, and action items (populated by a future "brain" layer or manually via API)
```

**Step 4: Run tests**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add CLAUDE.md README.md diagnostics.go
git commit -m "docs: document metadata design, enum stability invariant"
```

---

### Task 9: Update memory files

**Files:**
- Modify: `/home/jim/.claude/projects/-home-jim-projects-thought-store/memory/MEMORY.md`

**Step 1: Add metadata section to MEMORY.md**

Add after the "Thought State" section:

```markdown
## Thought Metadata (implemented)
- Two tables: `metadata` (content-addressed labels) and `thought_metadata` (UUID-tracked associations)
- `MetadataType` fake enum: person(0), date(1), tag(2), actionItem(3)
- Labels deduplicated by (metadata_type, value); associations synced by unique_id
- Populated externally (future brain layer), not at capture time
- ForgetThought deletes associations; labels persist (shared)
- Sync imports labels (INSERT OR IGNORE) then associations with ID remapping
- All DB-stored enums are append-only (ThoughtState, MetadataType, BrainMode)
```

**Step 2: Commit**

No git commit needed for memory files (they're outside the repo).
