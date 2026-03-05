# Thought Store

## Project overview

Personal thought capture and semantic search system. Single Go binary with web UI, CLI, and JSON API. Stores thoughts in Postgres with pgvector embeddings from local Ollama.

## Build

Always use `./build.sh` — never invoke `go build`, `go test`, or `templ generate` directly.

```bash
./build.sh build          # templ generate + go build
./build.sh test           # templ generate + go test ./... -v
./build.sh --refresh-htmx # download vendored htmx
```

## Architecture

```
main.go          CLI entrypoint (go-flags: serve, capture, search, mcp)
config.go        TOML config loading
server.go        Chi router, API handlers with htmx content negotiation
brain.go         ThoughtStore interface, LocalBrain (direct Postgres+Ollama)
remote.go        RemoteBrain (HTTP client with URL fallback chain)
mcp.go           MCP server setup (4 tools: capture, search, list, stats)
store.go         Postgres + pgvector (insert, list, cosine search)
ollama.go        Ollama embedding client (/api/embeddings endpoint)
templates/       Templ components (layout, home, thought card)
static/          CSS (tokens.css, app.css) and vendored htmx
```

## Key patterns

- **Content negotiation**: Handlers check `HX-Request` header. htmx gets HTML fragments, API clients get JSON.
- **Form + JSON**: `handleCapture` accepts both `application/x-www-form-urlencoded` (web UI) and `application/json` (CLI/API).
- **X-Source header**: Thoughts are tagged with source (`web`, `cli`, `seed`, `conversation`) for provenance.
- **pgvector registration**: `pgxvec.RegisterTypes` in pool's `AfterConnect` hook ensures every connection handles vector types.
- **ThoughtStore interface**: Both HTTP server and MCP server consume the same interface. LocalBrain talks directly to Postgres+Ollama; RemoteBrain talks HTTP with a URL fallback chain.
- **MCP subcommand**: `./thought-store mcp` speaks stdio MCP for Claude Code. `--remote` flag switches from LocalBrain to RemoteBrain.

## Tests

Integration tests against real Postgres and Ollama (no mocks). Tests skip gracefully if services unavailable.

- `ollama_test.go` — embedding dimension and non-zero check
- `store_test.go` — insert/list/search round-trip
- `remote_test.go` — RemoteBrain round-trip and URL fallback chain
- `e2e_test.go` — full HTTP pipeline on random port

## Config

`thought-store.toml` with sections: `[server]`, `[database]`, `[ollama]`, `[mcp]`.

## Dependencies

- `github.com/go-chi/chi/v5` — router
- `github.com/a-h/templ` — type-safe HTML templates
- `github.com/jessevdk/go-flags` — CLI parsing
- `github.com/BurntSushi/toml` — config
- `github.com/jackc/pgx/v5` — Postgres driver
- `github.com/pgvector/pgvector-go` — pgvector support
- `github.com/mark3labs/mcp-go` — MCP server (stdio transport)
