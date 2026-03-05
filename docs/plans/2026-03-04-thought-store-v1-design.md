# Thought Store v1 Design

Inspired by [Open Brain](https://promptkit.natebjones.com/20260224_uq1_guide_main) — a personal thought capture and semantic search system. This v1 strips it down to the essentials: capture thoughts, embed them, search by meaning.

## Stack

- **Go** single binary (chi router, templ templates, htmx)
- **Postgres 18.3** + pgvector 0.8.2
- **Ollama** with mxbai-embed-large (1024D vectors, local GPU via ROCm)

## Data Model

```sql
CREATE TABLE thoughts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content     TEXT NOT NULL,
    embedding   vector(1024) NOT NULL,
    source      TEXT NOT NULL DEFAULT 'web',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON thoughts USING hnsw (embedding vector_cosine_ops);
```

No metadata extraction in v1. Just raw text, embedding, source, timestamp.

## API

| Method | Path | Description |
|--------|------|-------------|
| POST | /api/thoughts | Capture a thought. Body: `{"content": "..."}` |
| GET | /api/thoughts | List recent thoughts. `?limit=20&offset=0` |
| GET | /api/search | Semantic search. `?q=...&limit=5` |

## CLI Subcommands

```
thought-store serve              # HTTP server + web UI
thought-store capture "my idea"  # POSTs to running server
thought-store search "auth bug"  # GETs from running server
```

Config via TOML (`--config thought-store.toml`):

```toml
[server]
addr = ":3000"

[database]
url = "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable"

[ollama]
url = "http://localhost:11434"
model = "mxbai-embed-large"
```

## Web UI

Chat-like single page:
- Thought stream (newest at bottom)
- Input bar pinned to bottom, submit on Enter
- Search mode toggle swaps to semantic search with ranked results
- htmx for no-reload interactions, templ fragments for partial updates
- No auth (local only)

## Pipeline (synchronous)

Capture: receive text -> embed via Ollama (~100ms) -> store in Postgres -> return rendered fragment.

Search: embed query -> pgvector cosine similarity -> return ranked results.

## Package Layout

```
thought-store/
├── main.go              # go-flags subcommands, wiring
├── config.go            # TOML config struct
├── server.go            # chi routes, handlers
├── ollama.go            # embedding client
├── store.go             # postgres queries
├── templates/           # .templ files
│   ├── layout.templ
│   ├── home.templ
│   └── thought.templ
├── static/
│   ├── tokens.css
│   ├── app.css
│   └── vendor/
│       └── htmx.min.js
├── build.sh
├── thought-store.toml
└── go.mod
```

## Not in v1

- Metadata extraction (topics, people, action items) — add when we bring in an LLM
- MCP server for AI agent access
- External capture integrations (Slack, etc.)
- Update/delete thoughts
- Authentication
