# SQLite Migration Design

## Goal

Replace Postgres with SQLite for a portable, sync-capable, zero-infrastructure storage layer. Decouple embeddings from thoughts to support model migration. Introduce provenance tracking for future distributed sync.

## Decisions

- **SQLite via `modernc.org/sqlite`** (pure Go, no CGO) replaces Postgres/pgx
- **`viterin/vek`** for SIMD-accelerated brute-force cosine similarity (AVX2/AVX-512)
- **No pgvector, no HNSW index** — brute force is fast enough at 100K+ scale
- **In-memory vector index** rebuilt from SQLite on startup. Future: revisit at >1M thoughts (~4GB footprint)
- **Integer PKs** for human readability, UUIDs as `unique_id` for sync identity
- **Source vs provenance**: source = capture mechanism (web, cli, mcp), instance = origin machine
- **Sync is transparent**: synced thoughts keep their original source and instance. `instance_id != local` is the signal.

## Schema

```sql
CREATE TABLE sources (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
-- Seeded: web, cli, mcp, seed, conversation

CREATE TABLE instances (
    id          INTEGER PRIMARY KEY,
    unique_id   TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT ''
    -- future: signature BLOB
);

CREATE TABLE models (
    id        INTEGER PRIMARY KEY,
    name      TEXT NOT NULL,
    version   TEXT NOT NULL DEFAULT '',
    dimension INTEGER NOT NULL,
    UNIQUE(name, version)
);

CREATE TABLE active_model (
    id       INTEGER PRIMARY KEY CHECK (id = 1),
    model_id INTEGER NOT NULL REFERENCES models(id)
);

CREATE TABLE thoughts (
    id          INTEGER PRIMARY KEY,
    unique_id   TEXT NOT NULL UNIQUE,
    content     TEXT NOT NULL,
    source_id   INTEGER NOT NULL REFERENCES sources(id),
    instance_id INTEGER NOT NULL REFERENCES instances(id),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE embeddings (
    thought_id INTEGER NOT NULL REFERENCES thoughts(id),
    model_id   INTEGER NOT NULL REFERENCES models(id),
    vector     BLOB NOT NULL,
    PRIMARY KEY (thought_id, model_id)
);
```

Key details:
- `embeddings` composite PK allows multiple models per thought during transitions
- `active_model` locked to single row via `CHECK (id = 1)`
- Timestamps as ISO-8601 text (SQLite convention)
- Vectors stored as raw `[]float32` bytes in BLOB; `models.dimension` validates size
- WAL mode + `PRAGMA foreign_keys=ON` set at connection open

## Storage Layer

- `store.go` rewritten: `*pgxpool.Pool` -> `*sql.DB` with modernc sqlite driver
- Config: `[database] path = "thought-store.db"` replaces `[database] url = "postgres://..."`
- `Migrate()` creates tables, seeds sources, creates local instance (UUID + hostname), seeds mxbai-embed-large model, sets active model

## In-Memory Vector Index

- On startup: load all embeddings for active model into parallel slices (`thoughtIDs []int`, `vectors [][]float32`)
- Search: `vek.CosineSimilarity` against all vectors, partial sort for top-N
- Capture: embed, write to SQLite, append to in-memory slices
- Re-embed: after completion, reload index from SQLite
- Thread safety: owned by actor goroutine, no mutex needed
- Memory: ~400MB at 100K thoughts (1024d float32). At >1M (~4GB), revisit with mmap, quantization, or disk-backed index.

## Postgres Migration Tool

`thought-store migrate-from-postgres` subcommand:
1. Connect to Postgres via `--postgres-url`, open/create SQLite DB via `--config`
2. Run SQLite migrations
3. Export thoughts: map `source` text to `source_id`, create instance record, preserve UUIDs and timestamps
4. Export embeddings: copy vectors as BLOBs with mxbai-embed-large model_id
5. Read-only on Postgres — old server keeps working
6. No re-embed needed (same model, same vectors)

## What Changes

| File | Change |
|------|--------|
| `store.go` | Complete rewrite: pgx -> database/sql + modernc sqlite |
| `config.go` | `DatabaseConfig.URL` -> `DatabaseConfig.Path` |
| `setup.go` | Postgres section (~200 lines) replaced by single path prompt |
| `go.mod` | Drop pgx, pgvector-go; add modernc.org/sqlite, viterin/vek |
| `thought-store.toml` | `[database] path = "thought-store.db"` |
| `store_test.go` | Rewrite against SQLite (simpler: t.TempDir() + file) |
| `e2e_test.go` | Swap store init |
| `reembed_test.go` | Adapt to new store API |

## What Stays the Same

- `brain.go` — ThoughtStore interface unchanged
- `server.go` — HTTP handlers don't touch storage directly
- `mcp.go` — same interface, same tools
- `remote.go` — talks HTTP, never touches DB
- `mdns.go`, `service*.go`, `setup_prompts.go`, `templates/` — unchanged

## Setup Wizard (Simplified)

1. Config file check (same)
2. Detect package manager (only needed for Ollama now)
3. Database path — `inputPrompt("Database path", "thought-store.db")` — that's it
4. Check Ollama (same)
5. Server config (same)
6. mDNS (same)
7. Write config (same)

## Future: Sync Bundles (not in this implementation)

Captured here for context — the schema supports this but implementation is later:
- Sync bundle = compressed SQLite DB + content-addressed asset blobs
- Import algorithm: expand bundle, open foreign DB, import thoughts/assets by `unique_id` (skip if already present), preserve original source and instance provenance
- Re-vectorize on import if local model differs from source model
- `instances.signature` for bundle authenticity verification (future security lift)

## Future: Memory Optimization (not in this implementation)

At >1M thoughts the in-memory vector index exceeds 4GB. Options to explore:
- Memory-mapped BLOB access
- Float16 quantization (halves memory)
- Disk-backed approximate nearest neighbor index
- Sharded index (per time period)
