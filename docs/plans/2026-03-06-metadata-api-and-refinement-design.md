# Metadata API & Thought Refinement Design

## Context

The metadata storage layer is built (MetadataType enum, metadata + thought_metadata tables, EnsureMetadata, AddThoughtMetadata, LoadThoughtMetadata, sync import, ForgetThought cascade). But there's no wire-level API to push metadata in — no MCP tool, no HTTP endpoint.

Additionally, thought refinement was designed in CLAUDE.md but never built. Refinement is tightly coupled with metadata: when the brain refines thoughts, it unions metadata from sources and attaches it to the summary.

This design covers both: the metadata API surface and the refinement mechanism.

## Three-Tier Brain Model

The API must serve all three tiers (see CLAUDE.md "Brain integration"):

| Mode | Who extracts metadata? | How it reaches the store |
|------|------------------------|--------------------------|
| `thought-store mcp` | The calling AI agent | MCP tools |
| `serve --enable-brain` | Embedded brain (LLM) | Direct store method calls |
| `serve --enable-brain --enable-slack ...` | Embedded brain (LLM) | Direct store method calls |

In MCP mode, the agent IS the brain. In brain mode, the store does its own extraction. Both need the same underlying store operations.

## Design

### 1. Extend `capture_thought` with optional metadata

The `capture_thought` MCP tool and `POST /api/thoughts` HTTP endpoint gain an optional `metadata` array:

```json
{
  "content": "met Sarah about the Go migration",
  "source": "conversation",
  "metadata": [
    {"type": "person", "value": "Sarah"},
    {"type": "tag", "value": "Go"},
    {"type": "action_item", "value": "draft migration plan"}
  ]
}
```

**MCP tool schema change:**
```
capture_thought:
  content: string (required)
  source: string (default: "conversation")
  metadata: string (optional, JSON-encoded array of {type, value} objects)
```

Note: MCP tool parameters are primitives. The metadata array is passed as a JSON string and parsed server-side. This is the same pattern other MCP tools use for structured data.

**Store change:** `Capture` in the `ThoughtStore` interface gains an optional metadata parameter. After inserting the thought, it calls `AddThoughtMetadata` for each entry. Since `AddThoughtMetadata` uses `INSERT OR IGNORE` with the `UNIQUE(thought_id, metadata_id)` constraint, this is idempotent.

**HTTP change:** `handleCapture` already parses JSON bodies. Add `Metadata []ThoughtMetadataEntry` to the request struct.

**Response:** The returned Thought JSON already includes `"metadata"` (via `populateMetadata` in the query path). For the capture response, we populate it directly from the input since we just wrote it.

### 2. New `update_thought_metadata` tool

Modifies metadata on an existing thought. **Deletes first, then adds** — this is a replace operation expressed as delete+add.

```json
{
  "thought_id": "<UUID>",
  "delete": [
    {"type": "tag", "value": "wrong-tag"}
  ],
  "add": [
    {"type": "tag", "value": "correct-tag"},
    {"type": "person", "value": "Jim"}
  ]
}
```

**MCP tool schema:**
```
update_thought_metadata:
  thought_id: string (required, the thought's UUID)
  delete: string (optional, JSON array of {type, value} to remove)
  add: string (optional, JSON array of {type, value} to add)
```

**Store methods needed:**
- `RemoveThoughtMetadata(ctx, thoughtUID, MetadataType, value) error` — deletes the specific association row. The metadata label in the `metadata` table is left intact (content-addressed, shared).
- Existing `AddThoughtMetadata` handles the add side (already idempotent).

**HTTP endpoint:** `PATCH /api/thoughts/:uuid/metadata` with the same JSON body (without thought_id, since it's in the URL).

**Operation order:** Process all deletes first, then all adds. This ensures that if the same (type, value) appears in both delete and add, the net result is it exists (add wins).

**Response:** Returns the thought with updated metadata (re-queries via `LoadThoughtMetadata`).

### 3. Thought refinement — `refined_thoughts` table

Many source thoughts roll up into one summary thought. The summary is itself a regular thought with its own UUID and embedding. This is arbitrarily deep — a DAG, not a tree.

**Schema:**
```sql
CREATE TABLE IF NOT EXISTS refined_thoughts (
    id                     INTEGER PRIMARY KEY,
    unique_id              TEXT NOT NULL UNIQUE,
    source_thought_id      INTEGER NOT NULL REFERENCES thoughts(id),
    target_thought_id      INTEGER NOT NULL REFERENCES thoughts(id),
    instance_id            INTEGER NOT NULL REFERENCES instances(id),
    UNIQUE(source_thought_id, target_thought_id)
)
```

- `unique_id` — UUID for the edge itself, needed for sync (same pattern as `thought_metadata`)
- `source_thought_id` — the contributing thought (gets marked `StateRefined`)
- `target_thought_id` — the summary thought (new, `StateActive`)
- `instance_id` — which instance performed this refinement
- `UNIQUE(source_thought_id, target_thought_id)` — prevents duplicate edges

Indexes:
```sql
CREATE INDEX IF NOT EXISTS idx_refined_thoughts_source ON refined_thoughts(source_thought_id);
CREATE INDEX IF NOT EXISTS idx_refined_thoughts_target ON refined_thoughts(target_thought_id);
```

### 4. New `refine_thoughts` MCP tool

```json
{
  "source_ids": ["<UUID1>", "<UUID2>", "<UUID3>"],
  "content": "Summary of the three thoughts about distributed security...",
  "metadata": [
    {"type": "tag", "value": "security"},
    {"type": "tag", "value": "distributed-systems"}
  ]
}
```

**MCP tool schema:**
```
refine_thoughts:
  source_ids: string (required, JSON array of source thought UUIDs)
  content: string (required, the summary/refined thought content)
  metadata: string (optional, JSON array of {type, value} objects)
```

**Store method:** `RefineThoughts(ctx, sourceUIDs []string, content string, metadata []ThoughtMetadataEntry, source string, instanceID int, modelID int) (*Thought, error)`

Within a single transaction:
1. Embed the new content (via Ollama)
2. Insert the new summary thought (`StateActive`)
3. Insert DAG edges into `refined_thoughts` for each source
4. Set each source thought's state to `StateRefined`
5. Remove source embeddings from VecIndex (they drop out of search)
6. Attach metadata to the summary thought (delete-before-add not needed — it's new)
7. Add the new thought's embedding to VecIndex

**HTTP endpoint:** `POST /api/thoughts/refine` with the same JSON body.

**Response:** Returns the new summary thought with metadata populated.

**Brain mode UX flow (from brainstorming):**
1. Human: "bring up stuff related to distributed security"
2. Brain: searches, presents numbered list with metadata
3. Human: "refine these as ..."
4. Brain: unions metadata from sources, extracts additional metadata, confirms with user
5. Brain: calls `refine_thoughts` with source UUIDs, new content, merged metadata

### 5. Sync for refined_thoughts

Same pattern as `importMetadata` in `sync.go`:

1. Query foreign `refined_thoughts` table
2. For each edge, resolve source and target thought UUIDs to local IDs
3. Resolve instance UID to local instance ID via `instanceMap`
4. `INSERT OR IGNORE` with the edge's `unique_id` for idempotent re-import
5. If a source thought exists locally and is still `StateActive`, set it to `StateRefined`
6. Remove newly-refined thoughts from VecIndex

### 6. ForgetThought cascade update

`ForgetThought` already deletes `thought_metadata` rows. It should also delete `refined_thoughts` edges where the forgotten thought is a source. If the forgotten thought is a target (summary), the source thoughts should revert to `StateActive` (they're no longer rolled up into anything).

### 7. Interface changes

The `ThoughtStore` interface needs updating:

```go
type ThoughtStore interface {
    Capture(ctx context.Context, content string, source string, metadata []ThoughtMetadataEntry) (*Thought, error)
    Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
    List(ctx context.Context, limit, offset int) ([]Thought, error)
    Stats(ctx context.Context) (*StoreStats, error)
    Diagnostics(ctx context.Context) (*DiagnosticsReport, error)
    UpdateThoughtMetadata(ctx context.Context, thoughtUID string, deletes []ThoughtMetadataEntry, adds []ThoughtMetadataEntry, instanceID int) (*Thought, error)
    RefineThoughts(ctx context.Context, sourceUIDs []string, content string, metadata []ThoughtMetadataEntry, source string) (*Thought, error)
}
```

Both `LocalBrain` and `RemoteBrain` implement these. `LocalBrain` calls store methods directly. `RemoteBrain` calls HTTP endpoints.

## CRDT properties preserved

- Refinement adds new rows (thoughts + edges). No mutations.
- Two instances independently refining the same sources produce different summaries (different UUIDs). After sync, both exist — not a conflict.
- Metadata on refined thoughts syncs via existing `thought_metadata` mechanism.
- Edge UUIDs ensure idempotent sync import.

## What's NOT in scope

- Brain mode (`--enable-brain`) implementation — that's a separate design
- Chat platform adapters (Slack, WhatsApp, Discord)
- Streamable HTTP MCP transport
- `places` metadata type — we have person, date, tag, actionItem; places could be added later (append-only enum)
