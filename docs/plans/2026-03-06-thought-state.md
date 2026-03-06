# Thought State Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `state` column to the thoughts table with three values (active, refined, forgotten) to support thought lifecycle management and sync propagation.

**Architecture:** Integer `state` column on thoughts table with a Go fake enum (`ThoughtState`). Queries filter by state. `forgotten` is a destructive tombstone that wipes content, deletes embeddings, and propagates through sync. `active` and `refined` are cached/derived values. VecIndex gains a `Remove` method.

**Tech Stack:** Go, SQLite (modernc.org/sqlite), existing test patterns (t.TempDir, table-driven)

---

### Task 1: ThoughtState enum and Thought struct update

**Files:**
- Modify: `types.go`

**Step 1: Write the failing test**

Create `state_test.go`:

```go
package main

import "testing"

func TestThoughtStateString(t *testing.T) {
	tests := []struct {
		state ThoughtState
		want  string
	}{
		{StateActive, "active"},
		{StateRefined, "refined"},
		{StateForgotten, "forgotten"},
		{ThoughtState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("ThoughtState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestThoughtStateMarshalText(t *testing.T) {
	b, err := StateRefined.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "refined" {
		t.Errorf("MarshalText = %q, want %q", b, "refined")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `ThoughtState` not defined

**Step 3: Write minimal implementation**

Add to `types.go`:

```go
// ThoughtState represents the lifecycle state of a thought.
type ThoughtState int

const (
	StateActive    ThoughtState = iota // default, included in search
	StateRefined                       // rolled up into a summary, hidden from default queries
	StateForgotten                     // tombstone: content erased, UUID preserved for sync
)

func (s ThoughtState) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateRefined:
		return "refined"
	case StateForgotten:
		return "forgotten"
	default:
		return "unknown"
	}
}

func (s ThoughtState) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}
```

Also add `State` field to `Thought` struct:

```go
type Thought struct {
	ID        string       `json:"id"`
	Content   string       `json:"content"`
	Source    string       `json:"source"`
	State     ThoughtState `json:"state"`
	CreatedAt time.Time    `json:"created_at"`
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add types.go state_test.go
git commit -m "feat: add ThoughtState enum and State field to Thought"
```

---

### Task 2: Schema migration — add state column

**Files:**
- Modify: `sqlite_store.go` (Migrate method, line 53)

**Step 1: Write the failing test**

Add to `state_test.go`:

```go
func TestMigrateAddsStateColumn(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migrate-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify column exists with default 0
	var state int
	err = store.db.QueryRowContext(ctx,
		`SELECT state FROM thoughts LIMIT 0`).Scan(&state)
	// No rows is fine — we just need the query to not error on column name
	// Insert a thought and check default
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, err := store.Insert(ctx, "test thought", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	if err != nil {
		t.Fatal(err)
	}

	err = store.db.QueryRowContext(ctx,
		`SELECT state FROM thoughts WHERE unique_id = ?`, thought.ID).Scan(&state)
	if err != nil {
		t.Fatalf("select state: %v", err)
	}
	if state != 0 {
		t.Fatalf("expected state=0 (active), got %d", state)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `no such column: state`

**Step 3: Write minimal implementation**

In `sqlite_store.go`, modify the `thoughts` CREATE TABLE in `Migrate()` to add the state column:

```sql
CREATE TABLE IF NOT EXISTS thoughts (
    id          INTEGER PRIMARY KEY,
    unique_id   TEXT NOT NULL UNIQUE,
    content     TEXT NOT NULL,
    source_id   INTEGER NOT NULL REFERENCES sources(id),
    instance_id INTEGER NOT NULL REFERENCES instances(id),
    state       INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
)
```

Also add an `ALTER TABLE` migration at the end of `Migrate()` for existing databases:

```go
// Add state column to existing databases (idempotent)
s.db.ExecContext(ctx, `ALTER TABLE thoughts ADD COLUMN state INTEGER NOT NULL DEFAULT 0`)
```

The ALTER TABLE will fail silently on new databases (column already exists) — that's fine, we ignore the error.

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go state_test.go
git commit -m "feat: add state column to thoughts table schema"
```

---

### Task 3: Filter List and Stats by state

**Files:**
- Modify: `sqlite_store.go` (List at line 236, Stats at line 262)

**Step 1: Write the failing test**

Add to `state_test.go`:

```go
func TestListFiltersActiveOnly(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "list-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "active thought", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.Insert(ctx, "another active", []float32{0.5, 0.6, 0.7, 0.8}, "web", instID, modelID)

	// Manually set one thought to forgotten
	store.db.ExecContext(ctx, `UPDATE thoughts SET state = 2, content = '' WHERE content = 'another active'`)

	thoughts, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(thoughts) != 1 {
		t.Fatalf("expected 1 active thought, got %d", len(thoughts))
	}
	if thoughts[0].Content != "active thought" {
		t.Fatalf("expected 'active thought', got %q", thoughts[0].Content)
	}
}

func TestStatsCountsActiveOnly(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "stats-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "one", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.Insert(ctx, "two", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	// Forget one
	store.db.ExecContext(ctx, `UPDATE thoughts SET state = 2, content = '' WHERE content = 'two'`)

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 {
		t.Fatalf("expected total=1, got %d", stats.Total)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — List returns 2 (no state filter), Stats.Total = 2

**Step 3: Write minimal implementation**

Modify `List()` query to add `WHERE t.state = 0`:

```go
func (s *SQLiteStore) List(ctx context.Context, limit, offset int) ([]Thought, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.unique_id, t.content, src.name, t.created_at
		 FROM thoughts t
		 JOIN sources src ON src.id = t.source_id
		 WHERE t.state = 0
		 ORDER BY t.created_at DESC LIMIT ? OFFSET ?`,
		limit, offset)
```

Modify `Stats()` queries to add `WHERE state = 0`:

```go
func (s *SQLiteStore) Stats(ctx context.Context) (*StoreStats, error) {
	var stats StoreStats
	var oldestAt, newestAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM thoughts WHERE state = 0`,
	).Scan(&stats.Total, &oldestAt, &newestAt)
	// ...

	rows, err := s.db.QueryContext(ctx,
		`SELECT src.name, COUNT(*) FROM thoughts t
		 JOIN sources src ON src.id = t.source_id
		 WHERE t.state = 0
		 GROUP BY src.name`)
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go state_test.go
git commit -m "feat: filter List and Stats to active-only thoughts"
```

---

### Task 4: Filter ListAllContent and LoadEmbeddings by state

**Files:**
- Modify: `sqlite_store.go` (ListAllContent at line 303, LoadEmbeddings at line 350)

**Step 1: Write the failing test**

Add to `state_test.go`:

```go
func TestListAllContentSkipsForgotten(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "listall-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "keep", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.Insert(ctx, "forget", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	// Forget one
	store.db.ExecContext(ctx, `UPDATE thoughts SET state = 2, content = '' WHERE content = 'forget'`)

	rows, err := store.ListAllContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

func TestLoadEmbeddingsSkipsForgotten(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "embed-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "keep", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.Insert(ctx, "forget", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	// Forget — but leave embedding in place (ForgetThought will clean up, but
	// LoadEmbeddings should also filter by state as defense-in-depth)
	store.db.ExecContext(ctx, `UPDATE thoughts SET state = 2, content = '' WHERE content = 'forget'`)

	ids, vecs, err := store.LoadEmbeddings(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(ids))
	}
	if len(vecs) != 1 {
		t.Fatalf("expected 1 vector, got %d", len(vecs))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — returns 2 rows/embeddings instead of 1

**Step 3: Write minimal implementation**

Modify `ListAllContent()` to filter by state:

```go
func (s *SQLiteStore) ListAllContent(ctx context.Context) ([]ThoughtRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT unique_id, content FROM thoughts WHERE state IN (0, 1) ORDER BY created_at ASC`)
```

Modify `LoadEmbeddings()` to filter by state:

```go
func (s *SQLiteStore) LoadEmbeddings(ctx context.Context, modelID int) ([]string, [][]float32, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.unique_id, e.vector
		 FROM embeddings e
		 JOIN thoughts t ON t.id = e.thought_id
		 WHERE e.model_id = ? AND t.state IN (0, 1)`, modelID)
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go state_test.go
git commit -m "feat: filter ListAllContent and LoadEmbeddings to active+refined"
```

---

### Task 5: VecIndex.Remove method

**Files:**
- Modify: `vecindex.go`

**Step 1: Write the failing test**

Add to `state_test.go`:

```go
func TestVecIndexRemove(t *testing.T) {
	idx := NewVecIndex()
	idx.Add("a", []float32{1, 0, 0, 0})
	idx.Add("b", []float32{0, 1, 0, 0})
	idx.Add("c", []float32{0, 0, 1, 0})

	if idx.Len() != 3 {
		t.Fatalf("expected 3, got %d", idx.Len())
	}

	idx.Remove("b")

	if idx.Len() != 2 {
		t.Fatalf("expected 2 after remove, got %d", idx.Len())
	}

	// Search should not find "b"
	results := idx.Search([]float32{0, 1, 0, 0}, 3)
	for _, r := range results {
		if r.ID == "b" {
			t.Fatal("removed ID 'b' should not appear in search results")
		}
	}

	// Remove non-existent ID — should be no-op
	idx.Remove("nonexistent")
	if idx.Len() != 2 {
		t.Fatalf("expected 2 after no-op remove, got %d", idx.Len())
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `idx.Remove` undefined

**Step 3: Write minimal implementation**

Add to `vecindex.go`:

```go
// Remove deletes an entry by ID. No-op if not found.
func (idx *VecIndex) Remove(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	for i, existing := range idx.ids {
		if existing == id {
			last := len(idx.ids) - 1
			idx.ids[i] = idx.ids[last]
			idx.vectors[i] = idx.vectors[last]
			idx.ids = idx.ids[:last]
			idx.vectors = idx.vectors[:last]
			return
		}
	}
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add vecindex.go state_test.go
git commit -m "feat: add Remove method to VecIndex"
```

---

### Task 6: ForgetThought store method

**Files:**
- Modify: `sqlite_store.go`

**Step 1: Write the failing test**

Add to `state_test.go`:

```go
func TestForgetThought(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "forget.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, _ := store.Insert(ctx, "remember this", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)

	// Verify embedding exists
	ids, _, _ := store.LoadEmbeddings(ctx, modelID)
	if len(ids) != 1 {
		t.Fatalf("expected 1 embedding before forget, got %d", len(ids))
	}

	if err := store.ForgetThought(ctx, thought.ID); err != nil {
		t.Fatalf("ForgetThought: %v", err)
	}

	// Content should be empty
	var content string
	var state int
	store.db.QueryRowContext(ctx,
		`SELECT content, state FROM thoughts WHERE unique_id = ?`, thought.ID).Scan(&content, &state)
	if content != "" {
		t.Fatalf("expected empty content, got %q", content)
	}
	if state != int(StateForgotten) {
		t.Fatalf("expected state=%d (forgotten), got %d", StateForgotten, state)
	}

	// Embedding should be deleted
	ids, _, _ = store.LoadEmbeddings(ctx, modelID)
	if len(ids) != 0 {
		t.Fatalf("expected 0 embeddings after forget, got %d", len(ids))
	}

	// Should not appear in List
	thoughts, _ := store.List(ctx, 10, 0)
	if len(thoughts) != 0 {
		t.Fatalf("expected 0 thoughts in list, got %d", len(thoughts))
	}

	// Forget again — idempotent
	if err := store.ForgetThought(ctx, thought.ID); err != nil {
		t.Fatalf("ForgetThought second call: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `store.ForgetThought` undefined

**Step 3: Write minimal implementation**

Add to `sqlite_store.go`:

```go
// ForgetThought sets a thought to the forgotten state: wipes content, deletes embeddings.
// The row is preserved as a tombstone (unique_id intact for sync propagation).
// Idempotent — forgetting an already-forgotten thought is a no-op.
func (s *SQLiteStore) ForgetThought(ctx context.Context, uniqueID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var thoughtID int
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM thoughts WHERE unique_id = ?`, uniqueID).Scan(&thoughtID)
	if err != nil {
		return fmt.Errorf("find thought %s: %w", uniqueID, err)
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE thoughts SET state = ?, content = '' WHERE id = ?`,
		StateForgotten, thoughtID)
	if err != nil {
		return fmt.Errorf("update thought state: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`DELETE FROM embeddings WHERE thought_id = ?`, thoughtID)
	if err != nil {
		return fmt.Errorf("delete embeddings: %w", err)
	}

	return tx.Commit()
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go state_test.go
git commit -m "feat: add ForgetThought method for destructive tombstone"
```

---

### Task 7: Sync import — propagate forgotten state

**Files:**
- Modify: `sync.go` (ImportBundle at line 76, ImportResult at line 16)

**Step 1: Write the failing test**

Add to `sync_test.go`:

```go
func TestSyncPropagatesForgotten(t *testing.T) {
	ctx := context.Background()

	// A and B each have their own stores
	storeA, dbPathA, instA, modelA := newTestStoreAt(t, "machine-A")
	storeB, _, instB, modelB := newTestStoreAt(t, "machine-B")

	// A captures a thought
	thought, _ := storeA.Insert(ctx, "will be forgotten", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instA, modelA)

	// B pulls from A — gets the thought
	uidB, _ := storeB.InstanceUID(ctx, instB)
	bundleA := exportToFile(t, ctx, storeA, dbPathA)
	result := importFromFile(t, ctx, storeB, uidB, modelB, bundleA)
	if result.NewThoughts != 1 {
		t.Fatalf("expected 1 new thought, got %d", result.NewThoughts)
	}

	// A forgets the thought
	if err := storeA.ForgetThought(ctx, thought.ID); err != nil {
		t.Fatalf("ForgetThought: %v", err)
	}

	// B pulls from A again — should propagate forgotten state
	bundleA2 := exportToFile(t, ctx, storeA, dbPathA)
	result2 := importFromFile(t, ctx, storeB, uidB, modelB, bundleA2)
	if result2.Forgotten != 1 {
		t.Fatalf("expected 1 forgotten propagation, got %d", result2.Forgotten)
	}

	// B should not list the thought
	thoughts, _ := storeB.List(ctx, 10, 0)
	for _, th := range thoughts {
		if th.ID == thought.ID {
			t.Fatal("forgotten thought should not appear in list")
		}
	}

	// B's thought should have empty content
	var content string
	var state int
	storeB.db.QueryRowContext(ctx,
		`SELECT content, state FROM thoughts WHERE unique_id = ?`, thought.ID).Scan(&content, &state)
	if content != "" {
		t.Fatalf("expected empty content, got %q", content)
	}
	if state != int(StateForgotten) {
		t.Fatalf("expected state=%d, got %d", StateForgotten, state)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `result2.Forgotten` undefined, no propagation logic

**Step 3: Write minimal implementation**

Add `Forgotten` field to `ImportResult` in `sync.go`:

```go
type ImportResult struct {
	NewInstances  int `json:"new_instances"`
	NewThoughts   int `json:"new_thoughts"`
	Skipped       int `json:"skipped"`
	ReembedQueued int `json:"reembed_queued"`
	Forgotten     int `json:"forgotten"`
}
```

Add a new step in `ImportBundle()` after the existing thought import loop (before the return). This step queries the foreign DB for forgotten thoughts and propagates that state locally:

```go
// Step 4: Propagate forgotten state
forgottenRows, err := foreignStore.db.QueryContext(ctx,
	`SELECT t.unique_id FROM thoughts t
	 JOIN instances i ON i.id = t.instance_id
	 WHERE t.state = ? AND i.unique_id != ?`,
	StateForgotten, myInstanceUID)
if err != nil {
	return nil, fmt.Errorf("query foreign forgotten: %w", err)
}
defer forgottenRows.Close()

for forgottenRows.Next() {
	var thoughtUID string
	if err := forgottenRows.Scan(&thoughtUID); err != nil {
		return nil, fmt.Errorf("scan forgotten: %w", err)
	}

	// Check if we have this thought and it's not already forgotten
	var localState int
	err := store.db.QueryRowContext(ctx,
		`SELECT state FROM thoughts WHERE unique_id = ?`, thoughtUID).Scan(&localState)
	if err != nil {
		continue // we don't have it locally
	}
	if localState == int(StateForgotten) {
		continue // already forgotten
	}

	if err := store.ForgetThought(ctx, thoughtUID); err != nil {
		return nil, fmt.Errorf("propagate forget %s: %w", thoughtUID, err)
	}
	result.Forgotten++
	if vecIndex != nil {
		vecIndex.Remove(thoughtUID)
	}
}
if err := forgottenRows.Err(); err != nil {
	return nil, fmt.Errorf("forgotten rows: %w", err)
}
```

Also update the foreign thought query (Step 3) to skip forgotten thoughts — we don't want to import content for a forgotten thought:

Change the WHERE clause from:
```sql
WHERE i.unique_id != ?
```
to:
```sql
WHERE i.unique_id != ? AND t.state != 2
```

Note: We use `2` (the integer value) in the foreign query because the foreign DB may not have our Go constants. Alternatively, use `StateForgotten` since both sides run the same binary — but the integer is safer for cross-version compat.

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sync.go sync_test.go
git commit -m "feat: propagate forgotten state through sync import"
```

---

### Task 8: ImportThought accepts state parameter

**Files:**
- Modify: `sqlite_store.go` (ImportThought at line 480)
- Modify: `sync.go` (call site at line 170)

**Step 1: Write the failing test**

Add to `state_test.go`:

```go
func TestImportThoughtWithState(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "import-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	err = store.ImportThought(ctx, "uid-1", "refined thought", "cli", instID, modelID, nil, StateRefined, "2026-03-06T12:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}

	var state int
	store.db.QueryRowContext(ctx,
		`SELECT state FROM thoughts WHERE unique_id = 'uid-1'`).Scan(&state)
	if state != int(StateRefined) {
		t.Fatalf("expected state=%d (refined), got %d", StateRefined, state)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `ImportThought` has wrong number of arguments (missing state param)

**Step 3: Write minimal implementation**

Update `ImportThought` signature to accept state:

```go
func (s *SQLiteStore) ImportThought(ctx context.Context, uniqueID, content, source string, instanceID, modelID int, embedding []float32, state ThoughtState, createdAt string) error {
```

Update the INSERT to include state:

```go
res, err := tx.ExecContext(ctx,
	`INSERT INTO thoughts (unique_id, content, source_id, instance_id, state, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
	uniqueID, content, sourceID, instanceID, state, createdAt)
```

Update the call site in `sync.go` `ImportBundle()` to pass `StateActive`:

```go
err := store.ImportThought(ctx, thoughtUID, content, sourceName, localInstanceID, localModelID, embedding, StateActive, createdAt)
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go sync.go state_test.go
git commit -m "feat: ImportThought accepts state parameter"
```

---

### Task 9: Include state in foreign thought query for sync

**Files:**
- Modify: `sync.go` (foreign thought SELECT at line 126)

This task updates the sync import to also read the `state` column from the foreign DB, so that if the foreign DB has a thought with `state=refined`, we import it as refined rather than active.

**Step 1: Write the failing test**

Add to `sync_test.go`:

```go
func TestSyncPreservesRefinedState(t *testing.T) {
	ctx := context.Background()

	storeA, dbPathA, instA, modelA := newTestStoreAt(t, "machine-A")
	storeB, _, instB, modelB := newTestStoreAt(t, "machine-B")

	// A captures a thought then marks it refined
	thought, _ := storeA.Insert(ctx, "refined on A", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instA, modelA)
	storeA.db.ExecContext(ctx, `UPDATE thoughts SET state = ? WHERE unique_id = ?`, StateRefined, thought.ID)

	// B pulls from A
	uidB, _ := storeB.InstanceUID(ctx, instB)
	bundle := exportToFile(t, ctx, storeA, dbPathA)
	importFromFile(t, ctx, storeB, uidB, modelB, bundle)

	// B should have the thought with refined state
	var state int
	err := storeB.db.QueryRowContext(ctx,
		`SELECT state FROM thoughts WHERE unique_id = ?`, thought.ID).Scan(&state)
	if err != nil {
		t.Fatalf("query state: %v", err)
	}
	if state != int(StateRefined) {
		t.Fatalf("expected state=%d (refined), got %d", StateRefined, state)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — imported thought has state=0 (active) instead of 1 (refined)

**Step 3: Write minimal implementation**

Update the foreign thought SELECT in `ImportBundle()` to include `t.state`:

```go
rows, err := foreignStore.db.QueryContext(ctx,
	`SELECT t.unique_id, t.content, src.name, i.unique_id, t.created_at, t.state
	 FROM thoughts t
	 JOIN sources src ON src.id = t.source_id
	 JOIN instances i ON i.id = t.instance_id
	 WHERE i.unique_id != ? AND t.state != ?`, myInstanceUID, StateForgotten)
```

Update the Scan to include state:

```go
var thoughtUID, content, sourceName, instanceUID, createdAt string
var foreignState int
if err := rows.Scan(&thoughtUID, &content, &sourceName, &instanceUID, &createdAt, &foreignState); err != nil {
```

Pass the state to `ImportThought`:

```go
err := store.ImportThought(ctx, thoughtUID, content, sourceName, localInstanceID, localModelID, embedding, ThoughtState(foreignState), createdAt)
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sync.go sync_test.go
git commit -m "feat: preserve thought state during sync import"
```

---

### Task 10: Populate State field in query results

**Files:**
- Modify: `sqlite_store.go` (List, GetByUniqueIDs)

**Step 1: Write the failing test**

Add to `state_test.go`:

```go
func TestListPopulatesStateField(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "populate-state.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	store.Insert(ctx, "active thought", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)

	thoughts, _ := store.List(ctx, 10, 0)
	if len(thoughts) != 1 {
		t.Fatal("expected 1 thought")
	}
	if thoughts[0].State != StateActive {
		t.Fatalf("expected State=StateActive, got %v", thoughts[0].State)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: PASS (State defaults to zero value which is StateActive=0). This test may pass already since the zero value of `ThoughtState` is `StateActive`. That's fine — it validates the contract. Skip to step 5.

**Step 3: Update queries to SELECT state**

Even though the test passes, we should properly SELECT the state column for correctness. Update `List()` and `GetByUniqueIDs()` to include `t.state` in the SELECT and scan it:

In `List()`:
```go
rows, err := s.db.QueryContext(ctx,
	`SELECT t.unique_id, t.content, src.name, t.state, t.created_at
	 FROM thoughts t
	 JOIN sources src ON src.id = t.source_id
	 WHERE t.state = 0
	 ORDER BY t.created_at DESC LIMIT ? OFFSET ?`,
	limit, offset)
// ...
var state int
if err := rows.Scan(&t.ID, &t.Content, &t.Source, &state, &createdAt); err != nil {
// ...
t.State = ThoughtState(state)
```

In `GetByUniqueIDs()`:
```go
query := fmt.Sprintf(
	`SELECT t.unique_id, t.content, src.name, t.state, t.created_at
	 FROM thoughts t
	 JOIN sources src ON src.id = t.source_id
	 WHERE t.unique_id IN (%s)`,
	strings.Join(placeholders, ","))
// ...
var state int
if err := rows.Scan(&t.ID, &t.Content, &t.Source, &state, &createdAt); err != nil {
// ...
t.State = ThoughtState(state)
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go state_test.go
git commit -m "feat: populate State field in List and GetByUniqueIDs results"
```

---

### Task 11: Update sync pull/push CLI output for Forgotten count

**Files:**
- Modify: `main.go` (SyncPullCmd.Execute at line 614, SyncPushCmd.Execute at line 686)

**Step 1: Update the format strings**

In `SyncPullCmd.Execute()`, update the output:

```go
fmt.Fprintf(os.Stderr, "Imported: %d new instances, %d new thoughts, %d skipped, %d queued for re-embed, %d forgotten\n",
	result.NewInstances, result.NewThoughts, result.Skipped, result.ReembedQueued, result.Forgotten)
```

In `SyncPushCmd.Execute()`, update:

```go
fmt.Fprintf(os.Stderr, "Peer imported: %d new instances, %d new thoughts, %d skipped, %d queued for re-embed, %d forgotten\n",
	result.NewInstances, result.NewThoughts, result.Skipped, result.ReembedQueued, result.Forgotten)
```

**Step 2: Run tests**

Run: `./build.sh test`
Expected: ALL PASS

**Step 3: Commit**

```bash
git add main.go
git commit -m "feat: include forgotten count in sync CLI output"
```

---

### Task 12: Update CLAUDE.md and memory files

**Files:**
- Modify: `CLAUDE.md`
- Modify: `/home/jim/.claude/projects/-home-jim-projects-thought-store/memory/MEMORY.md`

**Step 1: Update CLAUDE.md**

Add to the "Key patterns" section a bullet about thought state:

```
- **Thought state lifecycle**: `ThoughtState` fake enum (active=0, refined=1, forgotten=2). Active and refined are cached/derived. Forgotten is a destructive tombstone — content erased, embedding deleted, UUID preserved for sync propagation. List/Stats/Search default to active-only. LoadEmbeddings includes active+refined. Sync propagates forgotten state: if foreign thought is forgotten, local copy gets forgotten too.
```

Update the SQLite store description to mention state filtering.

**Step 2: Update memory**

Update MEMORY.md with the state column design.

**Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: document thought state lifecycle in CLAUDE.md"
```
