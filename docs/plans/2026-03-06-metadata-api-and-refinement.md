# Metadata API & Thought Refinement Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add wire-level APIs (MCP tools + HTTP endpoints) for metadata at capture time, post-capture metadata updates, and thought refinement — plus the `refined_thoughts` table and sync support.

**Architecture:** Extends the ThoughtStore interface with metadata-aware Capture, UpdateThoughtMetadata, and RefineThoughts. MCP tools pass structured metadata as JSON strings (MCP params are primitives). HTTP endpoints accept JSON bodies. The `refined_thoughts` table tracks DAG edges with UUIDs for sync. ForgetThought cascade handles both directions (forgetting a source or a summary).

**Tech Stack:** Go, SQLite (modernc.org/sqlite), mcp-go, chi router, existing test patterns (t.TempDir, table-driven)

---

### Task 1: RemoveThoughtMetadata store method

**Files:**
- Modify: `sqlite_store.go`
- Modify: `metadata_test.go`

**Step 1: Write the failing test**

Add to `metadata_test.go`:

```go
func TestRemoveThoughtMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "remove-meta.db")
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

	// Remove one metadata entry
	err = store.RemoveThoughtMetadata(ctx, thought.ID, MetadataPerson, "Sarah")
	if err != nil {
		t.Fatalf("RemoveThoughtMetadata: %v", err)
	}

	meta, _ := store.LoadThoughtMetadata(ctx, []string{thought.ID})
	if len(meta[thought.ID]) != 1 {
		t.Fatalf("expected 1 remaining entry, got %d", len(meta[thought.ID]))
	}
	if meta[thought.ID][0].Value != "meeting" {
		t.Fatalf("expected 'meeting' to remain, got %q", meta[thought.ID][0].Value)
	}

	// Remove non-existent entry — should not error
	err = store.RemoveThoughtMetadata(ctx, thought.ID, MetadataTag, "nonexistent")
	if err != nil {
		t.Fatalf("RemoveThoughtMetadata non-existent: %v", err)
	}

	// Metadata label should still exist (content-addressed, shared)
	var count int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata`).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 metadata labels to remain, got %d", count)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `store.RemoveThoughtMetadata` undefined

**Step 3: Write minimal implementation**

Add to `sqlite_store.go` after `AddThoughtMetadata` (after line ~247):

```go
// RemoveThoughtMetadata removes the association between a thought and a metadata label.
// The metadata label itself is preserved (content-addressed, shared).
// No error if the association doesn't exist.
func (s *SQLiteStore) RemoveThoughtMetadata(ctx context.Context, thoughtUID string, mt MetadataType, value string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM thought_metadata
		 WHERE thought_id = (SELECT id FROM thoughts WHERE unique_id = ?)
		   AND metadata_id = (SELECT id FROM metadata WHERE metadata_type = ? AND value = ?)`,
		thoughtUID, mt, value)
	if err != nil {
		return fmt.Errorf("remove thought metadata: %w", err)
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
git commit -m "feat: add RemoveThoughtMetadata store method"
```

---

### Task 2: Extend Capture with optional metadata

**Files:**
- Modify: `brain.go` (ThoughtStore interface line 12, LocalBrain.Capture line 50)
- Modify: `remote.go` (RemoteBrain.Capture line 47)
- Modify: `mcp.go` (captureHandler line 82)
- Modify: `server.go` (captureRequest line 92, handleCapture line 96)

**Step 1: Write the failing test**

Add to `metadata_test.go`:

```go
func TestCaptureWithMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "capture-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	meta := []ThoughtMetadataEntry{
		{Type: MetadataPerson, Value: "Sarah"},
		{Type: MetadataTag, Value: "Go"},
	}

	// Insert with metadata directly at the store level
	thought, err := store.InsertWithMetadata(ctx, "met Sarah about Go", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID, meta)
	if err != nil {
		t.Fatalf("InsertWithMetadata: %v", err)
	}

	// Verify metadata is populated on the returned thought
	if len(thought.Metadata) != 2 {
		t.Fatalf("expected 2 metadata entries, got %d", len(thought.Metadata))
	}

	// Verify it round-trips through List
	thoughts, _ := store.List(ctx, 10, 0)
	if len(thoughts[0].Metadata) != 2 {
		t.Fatalf("expected 2 metadata entries from List, got %d", len(thoughts[0].Metadata))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `store.InsertWithMetadata` undefined

**Step 3: Write minimal implementation**

Add to `sqlite_store.go` after `Insert` (after line ~365):

```go
// InsertWithMetadata inserts a thought and attaches metadata entries in one operation.
func (s *SQLiteStore) InsertWithMetadata(ctx context.Context, content string, embedding []float32, source string, instanceID, modelID int, metadata []ThoughtMetadataEntry) (*Thought, error) {
	thought, err := s.Insert(ctx, content, embedding, source, instanceID, modelID)
	if err != nil {
		return nil, err
	}
	for _, m := range metadata {
		if err := s.AddThoughtMetadata(ctx, thought.ID, m.Type, m.Value, instanceID); err != nil {
			return nil, fmt.Errorf("attach metadata: %w", err)
		}
	}
	thought.Metadata = metadata
	return thought, nil
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Update the ThoughtStore interface and all implementations**

In `brain.go`, change the interface (line 12):

```go
type ThoughtStore interface {
	Capture(ctx context.Context, content string, source string, metadata []ThoughtMetadataEntry) (*Thought, error)
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	List(ctx context.Context, limit, offset int) ([]Thought, error)
	Stats(ctx context.Context) (*StoreStats, error)
	Diagnostics(ctx context.Context) (*DiagnosticsReport, error)
}
```

Update `LocalBrain.Capture` (line 50):

```go
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
```

Update `RemoteBrain.Capture` in `remote.go` (line 47):

```go
func (b *RemoteBrain) Capture(ctx context.Context, content string, source string, metadata []ThoughtMetadataEntry) (*Thought, error) {
	type remoteCapture struct {
		Content  string                 `json:"content"`
		Metadata []ThoughtMetadataEntry `json:"metadata,omitempty"`
	}
	body, _ := json.Marshal(remoteCapture{Content: content, Metadata: metadata})
	resp, err := b.doRequest(ctx, "POST", "/api/thoughts", bytes.NewReader(body), http.Header{
		"Content-Type": {"application/json"},
		"X-Source":     {source},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("capture failed (%d): %s", resp.StatusCode, msg)
	}

	var t Thought
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode thought: %w", err)
	}
	return &t, nil
}
```

Update `captureRequest` and `handleCapture` in `server.go` (line 92):

```go
type captureRequest struct {
	Content  string                 `json:"content"`
	Metadata []ThoughtMetadataEntry `json:"metadata,omitempty"`
}
```

And update `handleCapture` (line 120) to pass metadata:

Change:
```go
	thought, err := s.brain.Capture(r.Context(), content, source)
```
To:
```go
	thought, err := s.brain.Capture(r.Context(), content, source, req.Metadata)
```

Note: For form-encoded (htmx) submissions, `req.Metadata` will be nil (no metadata from the web form), which is fine — `InsertWithMetadata` handles nil/empty slices.

But the form-encoded path doesn't set `req` — it reads `content` directly from `r.FormValue`. You need to restructure the handler slightly:

```go
func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var content string
	var metadata []ThoughtMetadataEntry

	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		content = r.FormValue("content")
	} else {
		var req captureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		content = req.Content
		metadata = req.Metadata
	}

	if content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	source := "web"
	if r.Header.Get("X-Source") != "" {
		source = r.Header.Get("X-Source")
	}

	thought, err := s.brain.Capture(r.Context(), content, source, metadata)
	if err != nil {
		http.Error(w, "capture failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if isHTMX(r) {
		data := templates.ThoughtData{
			ID:        thought.ID,
			Content:   thought.Content,
			Source:    thought.Source,
			CreatedAt: thought.CreatedAt,
		}
		templates.ThoughtCard(data).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(thought)
}
```

Update `captureHandler` in `mcp.go` (line 82):

```go
func captureHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content := mcp.ParseString(request, "content", "")
		if content == "" {
			return mcp.NewToolResultError("content is required"), nil
		}
		source := mcp.ParseString(request, "source", "conversation")

		var metadata []ThoughtMetadataEntry
		metaJSON := mcp.ParseString(request, "metadata", "")
		if metaJSON != "" {
			if err := json.Unmarshal([]byte(metaJSON), &metadata); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid metadata JSON: %v", err)), nil
			}
		}

		thought, err := brain.Capture(ctx, content, source, metadata)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("capture failed: %v", err)), nil
		}

		data, _ := json.Marshal(thought)
		return mcp.NewToolResultText(string(data)), nil
	}
}
```

Also update the MCP tool definition in `newMCPServer` (line 19) to declare the metadata parameter:

```go
	srv.AddTool(
		mcp.NewTool("capture_thought",
			mcp.WithDescription("Capture a new thought in the thought store"),
			mcp.WithString("content",
				mcp.Description("The thought content to capture"),
				mcp.Required(),
			),
			mcp.WithString("source",
				mcp.Description("Source of the thought (default: conversation)"),
				mcp.DefaultString("conversation"),
			),
			mcp.WithString("metadata",
				mcp.Description(`Optional metadata as JSON array of {"type":"person|date|tag|action_item","value":"..."} objects`),
			),
		),
		captureHandler(brain),
	)
```

Also find all other callers of `Capture` in the codebase. The CLI `capture` subcommand in `main.go` calls it too:

```bash
grep -rn '\.Capture(' *.go
```

Update each caller to pass `nil` for the metadata parameter if they don't have metadata to pass.

**Step 6: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 7: Commit**

```bash
git add brain.go remote.go mcp.go server.go sqlite_store.go metadata_test.go main.go
git commit -m "feat: extend Capture with optional metadata parameter"
```

---

### Task 3: update_thought_metadata MCP tool and HTTP endpoint

**Files:**
- Modify: `mcp.go`
- Modify: `server.go`
- Modify: `brain.go` (ThoughtStore interface)
- Modify: `remote.go` (RemoteBrain)
- Modify: `metadata_test.go`

**Step 1: Write the failing test**

Add to `metadata_test.go`:

```go
func TestUpdateThoughtMetadata(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "update-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	thought, _ := store.Insert(ctx, "thought with metadata", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	store.AddThoughtMetadata(ctx, thought.ID, MetadataPerson, "Sarah", instID)
	store.AddThoughtMetadata(ctx, thought.ID, MetadataTag, "wrong-tag", instID)

	// Delete wrong-tag, add correct-tag — deletes first, then adds
	deletes := []ThoughtMetadataEntry{{Type: MetadataTag, Value: "wrong-tag"}}
	adds := []ThoughtMetadataEntry{
		{Type: MetadataTag, Value: "correct-tag"},
		{Type: MetadataTag, Value: "Go"},
	}

	updated, err := store.UpdateMetadata(ctx, thought.ID, deletes, adds, instID)
	if err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	// Should have: person:Sarah, tag:correct-tag, tag:Go (3 entries)
	if len(updated.Metadata) != 3 {
		t.Fatalf("expected 3 metadata entries, got %d", len(updated.Metadata))
	}

	// Verify wrong-tag is gone
	for _, m := range updated.Metadata {
		if m.Value == "wrong-tag" {
			t.Fatal("wrong-tag should have been deleted")
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `store.UpdateMetadata` undefined

**Step 3: Write minimal implementation**

Add to `sqlite_store.go`:

```go
// UpdateMetadata performs delete-then-add on a thought's metadata.
// Returns the thought with updated metadata populated.
func (s *SQLiteStore) UpdateMetadata(ctx context.Context, thoughtUID string, deletes, adds []ThoughtMetadataEntry, instanceID int) (*Thought, error) {
	// Delete first
	for _, d := range deletes {
		if err := s.RemoveThoughtMetadata(ctx, thoughtUID, d.Type, d.Value); err != nil {
			return nil, fmt.Errorf("delete metadata: %w", err)
		}
	}
	// Then add
	for _, a := range adds {
		if err := s.AddThoughtMetadata(ctx, thoughtUID, a.Type, a.Value, instanceID); err != nil {
			return nil, fmt.Errorf("add metadata: %w", err)
		}
	}

	// Return updated thought
	thoughts, err := s.GetByUniqueIDs(ctx, []string{thoughtUID})
	if err != nil || len(thoughts) == 0 {
		return nil, fmt.Errorf("find thought %s: %w", thoughtUID, err)
	}
	return &thoughts[0], nil
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Add to ThoughtStore interface and wire MCP + HTTP**

Add to `ThoughtStore` interface in `brain.go`:

```go
type ThoughtStore interface {
	Capture(ctx context.Context, content string, source string, metadata []ThoughtMetadataEntry) (*Thought, error)
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	List(ctx context.Context, limit, offset int) ([]Thought, error)
	Stats(ctx context.Context) (*StoreStats, error)
	Diagnostics(ctx context.Context) (*DiagnosticsReport, error)
	UpdateThoughtMetadata(ctx context.Context, thoughtUID string, deletes, adds []ThoughtMetadataEntry) (*Thought, error)
}
```

Add `LocalBrain.UpdateThoughtMetadata`:

```go
func (b *LocalBrain) UpdateThoughtMetadata(ctx context.Context, thoughtUID string, deletes, adds []ThoughtMetadataEntry) (*Thought, error) {
	return b.store.UpdateMetadata(ctx, thoughtUID, deletes, adds, b.instanceID)
}
```

Add `RemoteBrain.UpdateThoughtMetadata`:

```go
func (b *RemoteBrain) UpdateThoughtMetadata(ctx context.Context, thoughtUID string, deletes, adds []ThoughtMetadataEntry) (*Thought, error) {
	type updateRequest struct {
		Delete []ThoughtMetadataEntry `json:"delete,omitempty"`
		Add    []ThoughtMetadataEntry `json:"add,omitempty"`
	}
	body, _ := json.Marshal(updateRequest{Delete: deletes, Add: adds})
	resp, err := b.doRequest(ctx, "PATCH", "/api/thoughts/"+thoughtUID+"/metadata", bytes.NewReader(body), http.Header{
		"Content-Type": {"application/json"},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("update metadata failed (%d): %s", resp.StatusCode, msg)
	}

	var t Thought
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode thought: %w", err)
	}
	return &t, nil
}
```

Add MCP tool in `newMCPServer` in `mcp.go`:

```go
	srv.AddTool(
		mcp.NewTool("update_thought_metadata",
			mcp.WithDescription("Update metadata on an existing thought. Deletes are applied before adds."),
			mcp.WithString("thought_id",
				mcp.Description("UUID of the thought to update"),
				mcp.Required(),
			),
			mcp.WithString("delete",
				mcp.Description(`JSON array of {"type":"person|date|tag|action_item","value":"..."} to remove`),
			),
			mcp.WithString("add",
				mcp.Description(`JSON array of {"type":"person|date|tag|action_item","value":"..."} to add`),
			),
		),
		updateMetadataHandler(brain),
	)
```

Add handler in `mcp.go`:

```go
func updateMetadataHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		thoughtID := mcp.ParseString(request, "thought_id", "")
		if thoughtID == "" {
			return mcp.NewToolResultError("thought_id is required"), nil
		}

		var deletes, adds []ThoughtMetadataEntry
		if deleteJSON := mcp.ParseString(request, "delete", ""); deleteJSON != "" {
			if err := json.Unmarshal([]byte(deleteJSON), &deletes); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid delete JSON: %v", err)), nil
			}
		}
		if addJSON := mcp.ParseString(request, "add", ""); addJSON != "" {
			if err := json.Unmarshal([]byte(addJSON), &adds); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid add JSON: %v", err)), nil
			}
		}

		thought, err := brain.UpdateThoughtMetadata(ctx, thoughtID, deletes, adds)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update failed: %v", err)), nil
		}

		data, _ := json.Marshal(thought)
		return mcp.NewToolResultText(string(data)), nil
	}
}
```

Add HTTP endpoint in `server.go`. Add route (after line 38):

```go
	s.router.Patch("/api/thoughts/{uuid}/metadata", s.handleUpdateMetadata)
```

Add handler:

```go
func (s *Server) handleUpdateMetadata(w http.ResponseWriter, r *http.Request) {
	uuid := chi.URLParam(r, "uuid")
	if uuid == "" {
		http.Error(w, "uuid is required", http.StatusBadRequest)
		return
	}

	var req struct {
		Delete []ThoughtMetadataEntry `json:"delete,omitempty"`
		Add    []ThoughtMetadataEntry `json:"add,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	thought, err := s.brain.UpdateThoughtMetadata(r.Context(), uuid, req.Delete, req.Add)
	if err != nil {
		http.Error(w, "update failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(thought)
}
```

**Step 6: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 7: Commit**

```bash
git add brain.go remote.go mcp.go server.go sqlite_store.go metadata_test.go
git commit -m "feat: add update_thought_metadata MCP tool and HTTP endpoint"
```

---

### Task 4: refined_thoughts schema and store method

**Files:**
- Modify: `sqlite_store.go` (Migrate, new RefineThoughts method)
- Create: `refine_test.go`

**Step 1: Write the failing test**

Create `refine_test.go`:

```go
package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateCreatesRefinedThoughtsTable(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migrate-refine.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var name string
	err = store.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='refined_thoughts'`).Scan(&name)
	if err != nil {
		t.Fatalf("refined_thoughts table not found: %v", err)
	}
}

func TestRefineThoughts(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "refine.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	// Create three source thoughts
	t1, _ := store.Insert(ctx, "thought about security", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	t2, _ := store.Insert(ctx, "thought about distributed systems", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)
	t3, _ := store.Insert(ctx, "thought about encryption", []float32{0.2, 0.3, 0.4, 0.5}, "cli", instID, modelID)

	// Add metadata to sources
	store.AddThoughtMetadata(ctx, t1.ID, MetadataTag, "security", instID)
	store.AddThoughtMetadata(ctx, t2.ID, MetadataTag, "distributed", instID)
	store.AddThoughtMetadata(ctx, t3.ID, MetadataTag, "security", instID)

	// Refine
	meta := []ThoughtMetadataEntry{
		{Type: MetadataTag, Value: "security"},
		{Type: MetadataTag, Value: "distributed"},
		{Type: MetadataTag, Value: "summary"},
	}
	summary, err := store.RefineThoughts(ctx,
		[]string{t1.ID, t2.ID, t3.ID},
		"Summary of distributed security thoughts",
		[]float32{0.3, 0.4, 0.5, 0.6},
		meta, "cli", instID, modelID)
	if err != nil {
		t.Fatalf("RefineThoughts: %v", err)
	}

	// Summary should be active with metadata
	if summary.State != StateActive {
		t.Fatalf("expected summary to be active, got %v", summary.State)
	}
	if len(summary.Metadata) != 3 {
		t.Fatalf("expected 3 metadata entries on summary, got %d", len(summary.Metadata))
	}

	// Sources should be refined
	sources, _ := store.GetByUniqueIDs(ctx, []string{t1.ID, t2.ID, t3.ID})
	for _, s := range sources {
		if s.State != StateRefined {
			t.Fatalf("expected source %s to be refined, got %v", s.ID, s.State)
		}
	}

	// DAG edges should exist
	var edgeCount int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refined_thoughts`).Scan(&edgeCount)
	if edgeCount != 3 {
		t.Fatalf("expected 3 DAG edges, got %d", edgeCount)
	}
}

func TestRefineThoughtsSharedSource(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "refine-shared.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	// A, B → D (refinement 1)
	tA, _ := store.Insert(ctx, "thought A", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	tB, _ := store.Insert(ctx, "thought B", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)
	tC, _ := store.Insert(ctx, "thought C", []float32{0.2, 0.3, 0.4, 0.5}, "cli", instID, modelID)

	store.RefineThoughts(ctx, []string{tA.ID, tB.ID}, "summary D",
		[]float32{0.3, 0.4, 0.5, 0.6}, nil, "cli", instID, modelID)

	// A, C → E (refinement 2 — shares source A)
	_, err = store.RefineThoughts(ctx, []string{tA.ID, tC.ID}, "summary E",
		[]float32{0.4, 0.5, 0.6, 0.7}, nil, "cli", instID, modelID)
	if err != nil {
		t.Fatalf("second RefineThoughts: %v", err)
	}

	// A should still be refined (participated in both)
	thoughts, _ := store.GetByUniqueIDs(ctx, []string{tA.ID})
	if thoughts[0].State != StateRefined {
		t.Fatalf("expected A to be refined, got %v", thoughts[0].State)
	}

	// Should have 4 edges total (2 for D, 2 for E)
	var edgeCount int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refined_thoughts`).Scan(&edgeCount)
	if edgeCount != 4 {
		t.Fatalf("expected 4 DAG edges, got %d", edgeCount)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `refined_thoughts table not found`

**Step 3: Write minimal implementation**

Add to `Migrate()` in `sqlite_store.go` (after thought_metadata indexes, before closing `}`):

```go
		`CREATE TABLE IF NOT EXISTS refined_thoughts (
			id                INTEGER PRIMARY KEY,
			unique_id         TEXT NOT NULL UNIQUE,
			source_thought_id INTEGER NOT NULL REFERENCES thoughts(id),
			target_thought_id INTEGER NOT NULL REFERENCES thoughts(id),
			instance_id       INTEGER NOT NULL REFERENCES instances(id),
			UNIQUE(source_thought_id, target_thought_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_refined_thoughts_source ON refined_thoughts(source_thought_id)`,
		`CREATE INDEX IF NOT EXISTS idx_refined_thoughts_target ON refined_thoughts(target_thought_id)`,
```

Add `RefineThoughts` method to `sqlite_store.go`:

```go
// RefineThoughts creates a summary thought from multiple sources.
// Sources are marked as StateRefined. DAG edges are recorded.
// The embedding is provided by the caller (already generated).
func (s *SQLiteStore) RefineThoughts(ctx context.Context, sourceUIDs []string, content string, embedding []float32, metadata []ThoughtMetadataEntry, source string, instanceID, modelID int) (*Thought, error) {
	// Insert the summary thought
	summary, err := s.Insert(ctx, content, embedding, source, instanceID, modelID)
	if err != nil {
		return nil, fmt.Errorf("insert summary: %w", err)
	}

	// Get summary's integer ID
	var targetID int
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM thoughts WHERE unique_id = ?`, summary.ID).Scan(&targetID)
	if err != nil {
		return nil, fmt.Errorf("find summary: %w", err)
	}

	// For each source: record edge, set state to refined
	for _, srcUID := range sourceUIDs {
		var srcID int
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM thoughts WHERE unique_id = ?`, srcUID).Scan(&srcID)
		if err != nil {
			return nil, fmt.Errorf("find source %s: %w", srcUID, err)
		}

		edgeUID := uuid.New().String()
		_, err = s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO refined_thoughts (unique_id, source_thought_id, target_thought_id, instance_id)
			 VALUES (?, ?, ?, ?)`,
			edgeUID, srcID, targetID, instanceID)
		if err != nil {
			return nil, fmt.Errorf("insert edge: %w", err)
		}

		_, err = s.db.ExecContext(ctx,
			`UPDATE thoughts SET state = ? WHERE id = ? AND state = ?`,
			StateRefined, srcID, StateActive)
		if err != nil {
			return nil, fmt.Errorf("mark source refined: %w", err)
		}
	}

	// Attach metadata
	for _, m := range metadata {
		if err := s.AddThoughtMetadata(ctx, summary.ID, m.Type, m.Value, instanceID); err != nil {
			return nil, fmt.Errorf("attach metadata: %w", err)
		}
	}
	summary.Metadata = metadata

	return summary, nil
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go refine_test.go
git commit -m "feat: add refined_thoughts table and RefineThoughts store method"
```

---

### Task 5: ForgetThought cascade for refinement edges

**Files:**
- Modify: `sqlite_store.go` (ForgetThought at line 671)
- Modify: `refine_test.go`

**Step 1: Write the failing test**

Add to `refine_test.go`:

```go
func TestForgetSummaryRevertsSourcesConditionally(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "forget-summary.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	// A, B → D (refinement 1)
	tA, _ := store.Insert(ctx, "thought A", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	tB, _ := store.Insert(ctx, "thought B", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)
	tC, _ := store.Insert(ctx, "thought C", []float32{0.2, 0.3, 0.4, 0.5}, "cli", instID, modelID)

	summaryD, _ := store.RefineThoughts(ctx, []string{tA.ID, tB.ID}, "summary D",
		[]float32{0.3, 0.4, 0.5, 0.6}, nil, "cli", instID, modelID)

	// A, C → E (refinement 2 — shares source A)
	_, err = store.RefineThoughts(ctx, []string{tA.ID, tC.ID}, "summary E",
		[]float32{0.4, 0.5, 0.6, 0.7}, nil, "cli", instID, modelID)
	if err != nil {
		t.Fatal(err)
	}

	// Forget D
	if err := store.ForgetThought(ctx, summaryD.ID); err != nil {
		t.Fatalf("ForgetThought: %v", err)
	}

	// B should revert to active (no remaining source edges)
	thoughts, _ := store.GetByUniqueIDs(ctx, []string{tB.ID})
	if thoughts[0].State != StateActive {
		t.Fatalf("expected B to be active, got %v", thoughts[0].State)
	}

	// A should stay refined (still has A→E edge)
	thoughts, _ = store.GetByUniqueIDs(ctx, []string{tA.ID})
	if thoughts[0].State != StateRefined {
		t.Fatalf("expected A to stay refined, got %v", thoughts[0].State)
	}

	// D→source edges should be deleted
	var edgeCount int
	store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM refined_thoughts WHERE target_thought_id = (SELECT id FROM thoughts WHERE unique_id = ?)`,
		summaryD.ID).Scan(&edgeCount)
	if edgeCount != 0 {
		t.Fatalf("expected 0 edges for forgotten summary, got %d", edgeCount)
	}
}

func TestForgetSourceDeletesEdges(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "forget-source.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	tA, _ := store.Insert(ctx, "thought A", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instID, modelID)
	tB, _ := store.Insert(ctx, "thought B", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instID, modelID)

	store.RefineThoughts(ctx, []string{tA.ID, tB.ID}, "summary",
		[]float32{0.3, 0.4, 0.5, 0.6}, nil, "cli", instID, modelID)

	// Forget source A
	if err := store.ForgetThought(ctx, tA.ID); err != nil {
		t.Fatalf("ForgetThought source: %v", err)
	}

	// Edge A→summary should be deleted
	var edgeCount int
	store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refined_thoughts`).Scan(&edgeCount)
	if edgeCount != 1 { // only B→summary remains
		t.Fatalf("expected 1 remaining edge, got %d", edgeCount)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — edges still exist after ForgetThought, B not reverted to active

**Step 3: Write minimal implementation**

Update `ForgetThought` in `sqlite_store.go` (line 671). Add after the `DELETE FROM thought_metadata` block and before `return tx.Commit()`:

```go
	// Delete refinement edges where this thought is a source
	_, err = tx.ExecContext(ctx,
		`DELETE FROM refined_thoughts WHERE source_thought_id = ?`, thoughtID)
	if err != nil {
		return fmt.Errorf("delete source edges: %w", err)
	}

	// If this thought is a summary (target), delete those edges
	// and conditionally revert sources to active
	rows, err := tx.QueryContext(ctx,
		`SELECT source_thought_id FROM refined_thoughts WHERE target_thought_id = ?`, thoughtID)
	if err != nil {
		return fmt.Errorf("find target edges: %w", err)
	}
	var sourceIDs []int
	for rows.Next() {
		var srcID int
		if err := rows.Scan(&srcID); err != nil {
			rows.Close()
			return fmt.Errorf("scan source id: %w", err)
		}
		sourceIDs = append(sourceIDs, srcID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("source ids: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`DELETE FROM refined_thoughts WHERE target_thought_id = ?`, thoughtID)
	if err != nil {
		return fmt.Errorf("delete target edges: %w", err)
	}

	// Revert sources to active only if they have no remaining outgoing edges
	for _, srcID := range sourceIDs {
		_, err = tx.ExecContext(ctx,
			`UPDATE thoughts SET state = ?
			 WHERE id = ? AND state = ?
			   AND NOT EXISTS (SELECT 1 FROM refined_thoughts WHERE source_thought_id = ?)`,
			StateActive, srcID, StateRefined, srcID)
		if err != nil {
			return fmt.Errorf("revert source %d: %w", srcID, err)
		}
	}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sqlite_store.go refine_test.go
git commit -m "feat: ForgetThought cascades refinement edges with conditional revert"
```

---

### Task 6: refine_thoughts MCP tool and HTTP endpoint

**Files:**
- Modify: `brain.go` (ThoughtStore interface, LocalBrain)
- Modify: `remote.go` (RemoteBrain)
- Modify: `mcp.go`
- Modify: `server.go`

**Step 1: Write the failing test**

Add to `refine_test.go`:

```go
func TestRefineThoughtsViaInterface(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "refine-iface.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Migrate(ctx)
	instID, _ := store.EnsureInstance(ctx, "test", "")
	modelID, _ := store.EnsureModel(ctx, "test-model", "", 4)
	store.SetActiveModel(ctx, modelID)

	// This test just verifies the ThoughtStore interface is satisfied
	// The actual refinement logic is tested in TestRefineThoughts
	var brain ThoughtStore
	_ = brain // Compile-time interface check is via var _ ThoughtStore = (*LocalBrain)(nil) in brain.go
}
```

Note: This task is primarily about wiring — the store method already works. The "test" is really the compile-time interface check plus verifying all callers compile.

**Step 2: Add to ThoughtStore interface and implement**

Add to `ThoughtStore` interface in `brain.go`:

```go
type ThoughtStore interface {
	Capture(ctx context.Context, content string, source string, metadata []ThoughtMetadataEntry) (*Thought, error)
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	List(ctx context.Context, limit, offset int) ([]Thought, error)
	Stats(ctx context.Context) (*StoreStats, error)
	Diagnostics(ctx context.Context) (*DiagnosticsReport, error)
	UpdateThoughtMetadata(ctx context.Context, thoughtUID string, deletes, adds []ThoughtMetadataEntry) (*Thought, error)
	RefineThoughts(ctx context.Context, sourceUIDs []string, content string, metadata []ThoughtMetadataEntry, source string) (*Thought, error)
}
```

Add `LocalBrain.RefineThoughts`:

```go
func (b *LocalBrain) RefineThoughts(ctx context.Context, sourceUIDs []string, content string, metadata []ThoughtMetadataEntry, source string) (*Thought, error) {
	embedding, err := b.ollama.Embed(ctx, content)
	if err != nil {
		return nil, err
	}
	summary, err := b.store.RefineThoughts(ctx, sourceUIDs, content, embedding, metadata, source, b.instanceID, b.modelID)
	if err != nil {
		return nil, err
	}
	// Update VecIndex: remove sources, add summary
	for _, uid := range sourceUIDs {
		b.vecIndex.Remove(uid)
	}
	b.vecIndex.Add(summary.ID, embedding)
	return summary, nil
}
```

Add `RemoteBrain.RefineThoughts`:

```go
func (b *RemoteBrain) RefineThoughts(ctx context.Context, sourceUIDs []string, content string, metadata []ThoughtMetadataEntry, source string) (*Thought, error) {
	type refineRequest struct {
		SourceIDs []string               `json:"source_ids"`
		Content   string                 `json:"content"`
		Metadata  []ThoughtMetadataEntry `json:"metadata,omitempty"`
	}
	body, _ := json.Marshal(refineRequest{SourceIDs: sourceUIDs, Content: content, Metadata: metadata})
	resp, err := b.doRequest(ctx, "POST", "/api/thoughts/refine", bytes.NewReader(body), http.Header{
		"Content-Type": {"application/json"},
		"X-Source":     {source},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refine failed (%d): %s", resp.StatusCode, msg)
	}

	var t Thought
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode thought: %w", err)
	}
	return &t, nil
}
```

Add MCP tool in `newMCPServer`:

```go
	srv.AddTool(
		mcp.NewTool("refine_thoughts",
			mcp.WithDescription("Refine multiple thoughts into a summary. Sources are marked as refined and drop out of search. Creates a new thought with the summary content."),
			mcp.WithString("source_ids",
				mcp.Description("JSON array of source thought UUIDs to refine"),
				mcp.Required(),
			),
			mcp.WithString("content",
				mcp.Description("The refined/summary thought content"),
				mcp.Required(),
			),
			mcp.WithString("metadata",
				mcp.Description(`Optional metadata as JSON array of {"type":"person|date|tag|action_item","value":"..."} objects`),
			),
		),
		refineHandler(brain),
	)
```

Add handler in `mcp.go`:

```go
func refineHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sourceIDsJSON := mcp.ParseString(request, "source_ids", "")
		if sourceIDsJSON == "" {
			return mcp.NewToolResultError("source_ids is required"), nil
		}
		var sourceIDs []string
		if err := json.Unmarshal([]byte(sourceIDsJSON), &sourceIDs); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid source_ids JSON: %v", err)), nil
		}
		if len(sourceIDs) == 0 {
			return mcp.NewToolResultError("source_ids must not be empty"), nil
		}

		content := mcp.ParseString(request, "content", "")
		if content == "" {
			return mcp.NewToolResultError("content is required"), nil
		}

		var metadata []ThoughtMetadataEntry
		if metaJSON := mcp.ParseString(request, "metadata", ""); metaJSON != "" {
			if err := json.Unmarshal([]byte(metaJSON), &metadata); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid metadata JSON: %v", err)), nil
			}
		}

		thought, err := brain.RefineThoughts(ctx, sourceIDs, content, metadata, "conversation")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("refine failed: %v", err)), nil
		}

		data, _ := json.Marshal(thought)
		return mcp.NewToolResultText(string(data)), nil
	}
}
```

Add HTTP endpoint in `server.go`. Add route:

```go
	s.router.Post("/api/thoughts/refine", s.handleRefine)
```

Add handler:

```go
func (s *Server) handleRefine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceIDs []string               `json:"source_ids"`
		Content   string                 `json:"content"`
		Metadata  []ThoughtMetadataEntry `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.SourceIDs) == 0 || req.Content == "" {
		http.Error(w, "source_ids and content are required", http.StatusBadRequest)
		return
	}

	source := "web"
	if r.Header.Get("X-Source") != "" {
		source = r.Header.Get("X-Source")
	}

	thought, err := s.brain.RefineThoughts(r.Context(), req.SourceIDs, req.Content, req.Metadata, source)
	if err != nil {
		http.Error(w, "refine failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(thought)
}
```

**Step 3: Run test to verify it compiles and passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 4: Commit**

```bash
git add brain.go remote.go mcp.go server.go refine_test.go
git commit -m "feat: add refine_thoughts MCP tool and HTTP endpoint"
```

---

### Task 7: Sync import for refined_thoughts

**Files:**
- Modify: `sync.go`
- Modify: `sync_test.go`

**Step 1: Write the failing test**

Add to `sync_test.go`:

```go
func TestSyncImportsRefinedThoughts(t *testing.T) {
	ctx := context.Background()

	storeA, dbPathA, instA, modelA := newTestStoreAt(t, "machine-A")
	storeB, _, instB, modelB := newTestStoreAt(t, "machine-B")

	// A creates thoughts and refines them
	t1, _ := storeA.Insert(ctx, "thought 1", []float32{0.1, 0.2, 0.3, 0.4}, "cli", instA, modelA)
	t2, _ := storeA.Insert(ctx, "thought 2", []float32{0.5, 0.6, 0.7, 0.8}, "cli", instA, modelA)
	summary, _ := storeA.RefineThoughts(ctx, []string{t1.ID, t2.ID}, "summary of 1 and 2",
		[]float32{0.3, 0.4, 0.5, 0.6}, nil, "cli", instA, modelA)

	// Verify sources are refined on A
	sources, _ := storeA.GetByUniqueIDs(ctx, []string{t1.ID, t2.ID})
	for _, s := range sources {
		if s.State != StateRefined {
			t.Fatalf("source %s should be refined on A, got %v", s.ID, s.State)
		}
	}

	// B pulls from A
	uidB, _ := storeB.InstanceUID(ctx, instB)
	bundle := exportToFile(t, ctx, storeA, dbPathA)
	result := importFromFile(t, ctx, storeB, uidB, modelB, bundle)
	if result.NewThoughts != 3 { // 2 sources + 1 summary
		t.Fatalf("expected 3 new thoughts, got %d", result.NewThoughts)
	}

	// Sources should be refined on B
	sourcesB, _ := storeB.GetByUniqueIDs(ctx, []string{t1.ID, t2.ID})
	for _, s := range sourcesB {
		if s.State != StateRefined {
			t.Fatalf("source %s should be refined on B, got %v", s.ID, s.State)
		}
	}

	// Summary should be active on B
	summaryB, _ := storeB.GetByUniqueIDs(ctx, []string{summary.ID})
	if summaryB[0].State != StateActive {
		t.Fatalf("summary should be active on B, got %v", summaryB[0].State)
	}

	// DAG edges should exist on B
	var edgeCount int
	storeB.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refined_thoughts`).Scan(&edgeCount)
	if edgeCount != 2 {
		t.Fatalf("expected 2 DAG edges on B, got %d", edgeCount)
	}

	// Import again — should be idempotent
	bundle2 := exportToFile(t, ctx, storeA, dbPathA)
	importFromFile(t, ctx, storeB, uidB, modelB, bundle2)

	storeB.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refined_thoughts`).Scan(&edgeCount)
	if edgeCount != 2 {
		t.Fatalf("expected 2 DAG edges after re-import, got %d", edgeCount)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — 0 DAG edges on B (refinement not synced)

**Step 3: Write minimal implementation**

Add to `sync.go`, after `importMetadata`. Call it from `ImportBundle` as Step 6:

In ImportBundle, after the `importMetadata` call (line 231), add:

```go
	// Step 6: Import refined_thoughts edges
	if err := importRefinedThoughts(ctx, store, foreignStore, instanceMap); err != nil {
		return nil, fmt.Errorf("import refined thoughts: %w", err)
	}
```

Add the function:

```go
// importRefinedThoughts imports refined_thoughts DAG edges from the foreign DB.
func importRefinedThoughts(ctx context.Context, localStore, foreignStore *SQLiteStore, instanceMap map[string]int) error {
	rows, err := foreignStore.db.QueryContext(ctx,
		`SELECT rt.unique_id, src.unique_id, tgt.unique_id, i.unique_id
		 FROM refined_thoughts rt
		 JOIN thoughts src ON src.id = rt.source_thought_id
		 JOIN thoughts tgt ON tgt.id = rt.target_thought_id
		 JOIN instances i ON i.id = rt.instance_id`)
	if err != nil {
		return fmt.Errorf("query foreign refined_thoughts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var edgeUID, srcUID, tgtUID, instanceUID string
		if err := rows.Scan(&edgeUID, &srcUID, &tgtUID, &instanceUID); err != nil {
			return fmt.Errorf("scan refined_thoughts: %w", err)
		}

		// Skip if already imported
		var exists int
		if localStore.db.QueryRowContext(ctx,
			`SELECT 1 FROM refined_thoughts WHERE unique_id = ?`, edgeUID).Scan(&exists) == nil {
			continue
		}

		// Resolve local IDs
		var localSrcID, localTgtID int
		if localStore.db.QueryRowContext(ctx,
			`SELECT id FROM thoughts WHERE unique_id = ?`, srcUID).Scan(&localSrcID) != nil {
			continue
		}
		if localStore.db.QueryRowContext(ctx,
			`SELECT id FROM thoughts WHERE unique_id = ?`, tgtUID).Scan(&localTgtID) != nil {
			continue
		}

		localInstanceID, ok := instanceMap[instanceUID]
		if !ok {
			continue
		}

		_, err := localStore.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO refined_thoughts (unique_id, source_thought_id, target_thought_id, instance_id)
			 VALUES (?, ?, ?, ?)`,
			edgeUID, localSrcID, localTgtID, localInstanceID)
		if err != nil {
			return fmt.Errorf("import refined_thoughts %s: %w", edgeUID, err)
		}

		// Mark source as refined if currently active
		_, err = localStore.db.ExecContext(ctx,
			`UPDATE thoughts SET state = ? WHERE id = ? AND state = ?`,
			StateRefined, localSrcID, StateActive)
		if err != nil {
			return fmt.Errorf("mark source refined: %w", err)
		}
	}
	return rows.Err()
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add sync.go sync_test.go
git commit -m "feat: sync refined_thoughts edges between peers"
```

---

### Task 8: Update docs and memory

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`

**Step 1: Update CLAUDE.md**

Update the architecture section for `sqlite_store.go`:

```
sqlite_store.go   SQLite store (insert, list, search, embeddings, metadata, refinement, import, forget)
```

Add to key patterns after the metadata bullet:

```
- **Thought refinement**: `refined_thoughts` table tracks DAG edges (source → summary). `RefineThoughts` creates summary + edges + marks sources in one operation. ForgetThought cascades: forgetting a summary conditionally reverts sources to active (only if they have no remaining outgoing edges). Sources participating in multiple refinements stay refined until all summaries are forgotten.
```

Update MCP description:

```
mcp.go           MCP server setup (7 tools: capture, search, list, stats, diagnostics, update_metadata, refine)
```

Update test list — add:

```
- `refine_test.go` — RefineThoughts, shared-source refinement, ForgetThought cascade with conditional revert, sync import of refinement edges
```

Update the "Thought refinement and forgetting" future section to mark refinement as implemented. Update forgetting section to note it's implemented (as `ThoughtState` + `ForgetThought`).

**Step 2: Update README.md**

Add after the "Tag" bullet:

```
- **Refine** multiple thoughts into summaries — sources drop out of search, summaries take their place
```

**Step 3: Run tests**

Run: `./build.sh test`
Expected: ALL PASS

**Step 4: Commit**

```bash
git add CLAUDE.md README.md
git commit -m "docs: document metadata API, refinement, and updated MCP tools"
```

---

### Task 9: Update memory files

**Files:**
- Modify: `/home/jim/.claude/projects/-home-jim-projects-thought-store/memory/MEMORY.md`

**Step 1: Add metadata API and refinement sections**

Update the "Thought Metadata" section to note the API surface:

```markdown
## Thought Metadata (implemented)
- Two tables: `metadata` (content-addressed labels) and `thought_metadata` (UUID-tracked associations)
- `MetadataType` fake enum: person(0), date(1), tag(2), actionItem(3)
- Labels deduplicated by (metadata_type, value); associations synced by unique_id
- API: capture_thought accepts optional metadata array; update_thought_metadata does delete-before-add
- MCP params are primitives — metadata arrays passed as JSON strings
- ForgetThought deletes associations; labels persist (shared)
- Sync imports labels (INSERT OR IGNORE) then associations with ID remapping
- All DB-stored enums are append-only (ThoughtState, MetadataType, BrainMode)
```

Update the "Future: Thought Refinement" section to mark as implemented:

```markdown
## Thought Refinement (implemented)
- `refined_thoughts` table: DAG edges (source → summary) with UUID for sync
- `RefineThoughts`: creates summary thought, records edges, marks sources as StateRefined
- Arbitrarily deep DAG — summaries can be refined into further summaries
- ForgetThought cascade: forgetting summary conditionally reverts sources to active
  - Only reverts if source has zero remaining outgoing refinement edges
  - Shared sources (in multiple refinements) stay refined until all summaries forgotten
- MCP tool: refine_thoughts (source_ids + content + optional metadata)
- Sync: imports edges with UUID dedup and ID remapping
```

**Step 2: No git commit needed** (memory files are outside the repo).
