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
main.go          CLI entrypoint (go-flags: serve, capture, search, mcp, re-embed, scan, service, setup)
config.go        TOML config loading
server.go        Chi router, API handlers with htmx content negotiation
brain.go         ThoughtStore interface, LocalBrain (direct Postgres+Ollama)
remote.go        RemoteBrain (HTTP client with URL fallback chain)
mcp.go           MCP server setup (5 tools: capture, search, list, stats, diagnostics)
diagnostics.go   Diagnostic report types, BrainMode enum, NetworkType classifier
store.go         Postgres + pgvector (insert, list, cosine search)
ollama.go        Ollama embedding client (/api/embeddings endpoint)
mdns.go          mDNS service advertisement and discovery (hashicorp/mdns)
service.go       Platform-agnostic ServiceManager interface and types
service_linux.go systemd implementation (install, uninstall, enable, disable, status, logs)
service_unsupported.go  Stub for non-Linux platforms
setup.go         Interactive guided setup wizard (Postgres, Ollama, config generation)
setup_prompts.go huh/lipgloss prompt wrappers for setup
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
- **Re-embed**: `./thought-store re-embed` re-vectorizes all thoughts using the current Ollama model. Dry-run by default; `--apply` to execute. Uses Ollama's `/api/embed` batch endpoint (50 texts/call by default, tunable via `--batch-size`).
- **mDNS discovery**: `[mdns].name` enables both advertising (on `serve`) and discovery (on `mcp --remote`). `./thought-store scan` finds all instances and writes the selection to config. Uses `hashicorp/mdns` (pure Go, cross-platform).
- **Setup wizard**: `./thought-store setup` interactively checks/installs Postgres and Ollama, provisions the database (user, DB, pgvector), and generates `thought-store.toml`. Uses `charmbracelet/huh` for forms and `lipgloss` for styled output. Detects package managers (paru/yay/pacman/brew). Supports local and remote Postgres.
- **Service management**: `./thought-store service` subcommand with install/uninstall/enable/disable/status/logs. Platform-agnostic `ServiceManager` interface with build-tagged implementations: systemd on Linux, stub on other platforms. User services by default, `--system` for system-level. Reusable pattern for future projects.

## Tests

Integration tests against real Postgres and Ollama (no mocks). Tests skip gracefully if services unavailable.

- `ollama_test.go` — embedding dimension and non-zero check
- `store_test.go` — insert/list/search round-trip
- `remote_test.go` — RemoteBrain round-trip and URL fallback chain
- `e2e_test.go` — full HTTP pipeline on random port, including diagnostics endpoint
- `reembed_test.go` — batch embed, update embeddings, and post-re-embed search verification
- `mdns_test.go` — mDNS advertise/discover round-trip, ParsePort unit test
- `service_test.go` — ResolvePaths with temp files, missing file error handling
- `service_linux_test.go` — unit file path generation, systemctl show output parsing
- `setup_test.go` — package manager detection, install command generation, config file checks

## Future direction: SQLite + distributed sync

The project is moving from Postgres to SQLite (`modernc.org/sqlite`, pure Go) to enable a distributed, sync-capable architecture. See `docs/plans/2026-03-05-sqlite-migration-design.md` for the full design.

- **SQLite over Postgres**: eliminates server dependency, enables single-file portable brains, simplifies setup to zero infrastructure. WAL mode for concurrent reads, single-writer serialization.
- **In-process vector search**: `viterin/vek` (SIMD-accelerated, pure Go) for brute-force cosine similarity. In-memory index rebuilt from SQLite on startup. At 100K×1024d with AVX2, search is single-digit milliseconds.
- **Schema redesign**: integer PKs (human-readable) + UUIDs (sync identity). Separate tables for `sources` (capture mechanism: web/cli/mcp), `instances` (provenance: which machine), `models` (embedding model metadata), `embeddings` (decoupled from thoughts, supports model transitions), `active_model` (single-row, FK-constrained).
- **Sync bundles** (future): compressed SQLite DB + content-addressed asset blobs. Import by `unique_id`, preserve original source and instance provenance. Sync is transparent — not a capture mechanism, just transport.
- **Re-vectorize on import** (future): if local model differs from source, imported thoughts get re-embedded.
- **Rich media** (future): supplemental files, images, data sets, code snippets, audio, video, calendar data — stored as content-addressed blobs alongside the SQLite DB.
- **Memory optimization** (future): at >1M thoughts (~4GB index), explore mmap, float16 quantization, or disk-backed ANN index.

## Config

`thought-store.toml` with sections: `[server]`, `[database]`, `[ollama]`, `[mcp]`, `[mdns]`.

## Dependencies

- `github.com/go-chi/chi/v5` — router
- `github.com/a-h/templ` — type-safe HTML templates
- `github.com/jessevdk/go-flags` — CLI parsing
- `github.com/BurntSushi/toml` — config
- `github.com/jackc/pgx/v5` — Postgres driver
- `github.com/pgvector/pgvector-go` — pgvector support
- `github.com/mark3labs/mcp-go` — MCP server (stdio transport)
- `github.com/hashicorp/mdns` — mDNS service advertisement and discovery
- `github.com/charmbracelet/huh` — interactive terminal forms
- `github.com/charmbracelet/lipgloss` — styled terminal output
