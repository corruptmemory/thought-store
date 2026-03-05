# Thought Store

## Project overview

Personal thought capture and semantic search system. Single Go binary with web UI, CLI, and JSON API. Stores thoughts in Postgres with pgvector embeddings from local Ollama.

## Inspiration

Inspired by Nate Jones's [Open Brain](https://promptkit.natebjones.com/20260224_uq1_guide_main) concept and [PromptKit Prompt 1](https://promptkit.natebjones.com/20260224_uq1_promptkit_1) (memory seed migration).

**Nate's approach:** Cloud-hosted on free tiers (Supabase + OpenRouter + Slack). Supabase Edge Functions handle ingestion and MCP serving. OpenRouter provides embeddings (text-embedding-3-small) and metadata extraction (gpt-4o-mini). Slack is the primary capture interface. Thoughts get LLM-extracted structured metadata (type, people, topics, action items). MCP server is a remote HTTP endpoint that Claude Desktop, ChatGPT, Cursor, etc. connect to.

**How we differ:**

| | Nate's Open Brain | This project |
|---|---|---|
| **Hosting** | Cloud (Supabase, OpenRouter) | Self-hosted (local Postgres, Ollama) |
| **Embeddings** | OpenRouter text-embedding-3-small (1536d) | Local Ollama mxbai-embed-large (1024d) |
| **Metadata** | LLM-extracted (type, people, topics, actions) | Source tag only (provenance, not classification) |
| **Capture** | Slack webhooks | Web UI, CLI, JSON API, MCP |
| **MCP transport** | Remote HTTP (Edge Function) | Stdio subprocess (local) or HTTP (remote via Tailscale) |
| **Binary** | Serverless functions | Single Go binary, all assets embedded |
| **Cost** | ~$0.10-0.30/mo API costs | $0 (runs entirely on local hardware) |
| **Network** | Requires internet | Works offline; Tailscale for remote access |

**Shared DNA:** Both use Postgres+pgvector for storage, cosine similarity for retrieval, MCP for AI client integration, and the same 4-tool interface (capture, search, list, stats). Both treat semantic search as primary and metadata as secondary. Nate's Prompt 1 "seed" concept — harvesting what an AI already knows about you into the store — was used directly to bootstrap this thought store.

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
mcp.go           MCP server setup (5 tools: capture, search, list, stats, diagnostics)
diagnostics.go   Diagnostic report types, BrainMode enum, NetworkType classifier
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
- **Diagnostics**: `Diagnostics(ctx)` on the interface returns a structured report. LocalBrain queries Postgres and Ollama directly; RemoteBrain fetches `/api/diagnostics` and nests the server's report. `BrainMode` is a fake enum with `String()` and `MarshalText()`.

## Tests

Integration tests against real Postgres and Ollama (no mocks). Tests skip gracefully if services unavailable.

- `ollama_test.go` — embedding dimension and non-zero check
- `store_test.go` — insert/list/search round-trip
- `remote_test.go` — RemoteBrain round-trip and URL fallback chain
- `e2e_test.go` — full HTTP pipeline on random port, including diagnostics endpoint

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
