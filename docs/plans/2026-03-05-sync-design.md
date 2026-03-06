# Sync Design

## Goal

Add gossip-style distributed sync so any thought-store instance can exchange thoughts with any other. Full-state snapshot bundles, no central coordinator, convergence via pairwise sync.

## Decisions

- **Full snapshot bundles** — no deltas, no sync state tracking. Like `git clone`. Idempotent import via `unique_id` deduplication.
- **zstd compression** — fast, good ratios, pure Go via `github.com/klauspost/compress/zstd`
- **Canonical bundle layout** — `brain.db` + `assets/` inside tarball, regardless of local config path
- **WAL checkpoint before export** — `PRAGMA wal_checkpoint(TRUNCATE)` consolidates to single `.db` file
- **Gossip protocol** — every instance is a peer. Transitive: if C syncs to B and B syncs to A, A gets C's thoughts
- **Model mismatch** — import thoughts without embeddings, queue for local re-embed
- **Auth deferred** — open API on trusted networks for now
- **Assets deferred** — tarball structure includes `assets/` but nothing populates it yet

## Bundle Format

zstd-compressed tar archive:

```
brain.db
assets/
```

`brain.db` is a complete SQLite database with the full schema. `assets/` is reserved for future content-addressed blobs.

Before packing: `PRAGMA wal_checkpoint(TRUNCATE)` ensures the WAL is folded into the main DB file. Only `brain.db` goes into the tarball — no `-shm` or `-wal` files.

## API

### `GET /api/sync`

Returns a zstd-compressed tarball of the sender's complete database.

- Content-Type: `application/x-tar+zstd` (or `application/octet-stream`)
- Response: streaming tarball
- No query params needed — always ships the full snapshot

### `POST /api/sync`

Accepts a zstd-compressed tarball, merges it into the local database.

- Content-Type: `application/x-tar+zstd` (or `application/octet-stream`)
- Request body: streaming tarball
- Response: JSON summary of what was imported

```json
{
  "new_instances": 1,
  "new_thoughts": 12,
  "skipped": 27,
  "reembed_queued": 12
}
```

## Import Algorithm

1. Decompress and untar to temp directory
2. Open foreign `brain.db` read-only
3. Read foreign `instances` table. For each `unique_id` not in local DB, create the instance record. This is how we learn about new peers transitively.
4. Query foreign DB for candidate thoughts: all thoughts where `instance.unique_id != my_instance_uid`. These are thoughts that didn't originate from me.
5. From candidates, filter to those whose `unique_id` does NOT exist in local `thoughts` table. These are genuinely new.
6. For each new thought:
   - Map `source` by name (create if needed via `SourceID`)
   - Map `instance` by `unique_id` (already created in step 3)
   - Insert into local `thoughts` table preserving `unique_id` and `created_at`
   - If foreign active model matches local active model: copy embedding
   - If models differ: skip embedding (thought gets re-embedded later)
7. Update in-memory vector index with any new embeddings
8. Clean up temp directory

## CLI

```bash
# Pull from a peer (download their bundle, merge into local DB)
./thought-store sync pull --from http://desktop:3000

# Push to a peer (upload local bundle to their sync endpoint)
./thought-store sync push --to http://desktop:3000

# Bidirectional: pull then push
./thought-store sync --peer http://desktop:3000

# Auto-discover via mDNS
./thought-store sync --peer jim-brain
```

## What Changes

| File | Change |
|------|--------|
| `sync.go` | Bundle creation (checkpoint, tar, zstd) and import algorithm |
| `sync_test.go` | Two-instance sync round-trip, transitive sync, model mismatch |
| `server.go` | Add `GET /api/sync` and `POST /api/sync` handlers |
| `main.go` | Add `sync` subcommand with pull/push/bidirectional modes |
| `sqlite_store.go` | Add `Checkpoint()` method, possibly `ImportThought()` helper |
| `vecindex.go` | No changes — `Add()` already supports appending post-sync |

## What Stays the Same

- `brain.go` — ThoughtStore interface unchanged
- `mcp.go` — MCP tools unchanged
- `remote.go` — untouched (removal is a separate task)
- `templates/` — web UI unchanged
- Schema — no migrations needed, existing tables support sync

## Key Properties

- **Append-only + UUID = CRDT** — thoughts are never modified, only inserted. UUIDs prevent duplicates. Order of syncs doesn't matter.
- **Idempotent** — syncing the same bundle twice changes nothing.
- **Transitive** — knowledge propagates through any chain of pairwise syncs.
- **No coordination** — no leader election, no version vectors, no conflict resolution (because there are no conflicts in an append-only model).
