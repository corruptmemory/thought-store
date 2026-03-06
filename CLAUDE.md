# Thought Store

## Project overview

Personal thought capture and semantic search system. Single Go binary with web UI, CLI, and JSON API. Stores thoughts in SQLite with in-memory SIMD vector search, embeddings from local Ollama.

## Inspiration

Inspired by Nate Jones's [Open Brain](https://promptkit.natebjones.com/20260224_uq1_guide_main) concept and [PromptKit Prompt 1](https://promptkit.natebjones.com/20260224_uq1_promptkit_1) (memory seed migration).

**Nate's approach:** Cloud-hosted on free tiers (Supabase + OpenRouter + Slack). Supabase Edge Functions handle ingestion and MCP serving. OpenRouter provides embeddings (text-embedding-3-small) and metadata extraction (gpt-4o-mini). Slack is the primary capture interface. Thoughts get LLM-extracted structured metadata (type, people, topics, action items). MCP server is a remote HTTP endpoint that Claude Desktop, ChatGPT, Cursor, etc. connect to.

**How we differ:**

| | Nate's Open Brain | This project |
|---|---|---|
| **Hosting** | Cloud (Supabase, OpenRouter) | Self-hosted (local SQLite, Ollama) |
| **Embeddings** | OpenRouter text-embedding-3-small (1536d) | Local Ollama mxbai-embed-large (1024d) |
| **Metadata** | LLM-extracted (type, people, topics, actions) | Source tag only (provenance, not classification) |
| **Capture** | Slack webhooks | Web UI, CLI, JSON API, MCP |
| **MCP transport** | Remote HTTP (Edge Function) | Stdio subprocess (local) or HTTP (remote via Tailscale) |
| **Binary** | Serverless functions | Single Go binary, all assets embedded |
| **Cost** | ~$0.10-0.30/mo API costs | $0 (runs entirely on local hardware) |
| **Network** | Requires internet | Works offline; Tailscale for remote access |

**Shared DNA:** Both use cosine similarity for retrieval, MCP for AI client integration, and the same 4-tool interface (capture, search, list, stats). Both treat semantic search as primary and metadata as secondary. Nate's Prompt 1 "seed" concept — harvesting what an AI already knows about you into the store — was used directly to bootstrap this thought store.

## Build

Always use `./build.sh` — never invoke `go build`, `go test`, or `templ generate` directly.

```bash
./build.sh build          # templ generate + go build
./build.sh test           # templ generate + go test ./... -v
./build.sh --refresh-htmx # download vendored htmx
```

## Architecture

```
main.go          CLI entrypoint (go-flags: serve, capture, search, mcp, re-embed, scan, service, setup, sync)
config.go        TOML config loading
server.go        Chi router, API handlers, sync endpoints, reload endpoint
brain.go         ThoughtStore interface, LocalBrain (direct SQLite+Ollama)
remote.go        RemoteBrain (HTTP client with URL fallback chain)
mcp.go           MCP server setup (5 tools: capture, search, list, stats, diagnostics)
diagnostics.go   Diagnostic report types, BrainMode enum, NetworkType classifier
sqlite_store.go   SQLite store (insert, list, search, embeddings, import helpers)
vecindex.go      SIMD-accelerated in-memory vector index (viterin/vek)
sync.go          Bundle export/import: zstd tarballs, gossip-style merge
ollama.go        Ollama embedding client (/api/embeddings endpoint)
mdns.go          mDNS service advertisement and discovery (hashicorp/mdns)
service.go       Platform-agnostic ServiceManager interface and types
service_linux.go systemd implementation (install, uninstall, enable, disable, status, logs)
service_unsupported.go  Stub for non-Linux platforms
setup.go         Interactive guided setup wizard (SQLite, Ollama, config generation)
setup_prompts.go huh/lipgloss prompt wrappers for setup
templates/       Templ components (layout, home, thought card)
static/          CSS (tokens.css, app.css) and vendored htmx
```

## Key patterns

- **Content negotiation**: Handlers check `HX-Request` header. htmx gets HTML fragments, API clients get JSON.
- **Form + JSON**: `handleCapture` accepts both `application/x-www-form-urlencoded` (web UI) and `application/json` (CLI/API).
- **X-Source header**: Thoughts are tagged with source (`web`, `cli`, `seed`, `conversation`) for provenance.
- **ThoughtStore interface**: Both HTTP server and MCP server consume the same interface. LocalBrain talks directly to SQLite+Ollama; RemoteBrain talks HTTP with a URL fallback chain.
- **In-memory vector index**: On startup, all embeddings for the active model are loaded into memory. Search uses SIMD-accelerated cosine similarity via viterin/vek. Capture appends to the index.
- **MCP subcommand**: `./thought-store mcp` speaks stdio MCP for Claude Code. `--remote` flag switches from LocalBrain to RemoteBrain.
- **Diagnostics**: `Diagnostics(ctx)` on the interface returns a structured report. LocalBrain queries SQLite and Ollama directly; RemoteBrain fetches `/api/diagnostics` and nests the server's report. `BrainMode` is a fake enum with `String()` and `MarshalText()`.
- **Re-embed**: `./thought-store re-embed` re-vectorizes all thoughts using the current Ollama model. Dry-run by default; `--apply` to execute. Uses Ollama's `/api/embed` batch endpoint (50 texts/call by default, tunable via `--batch-size`).
- **mDNS discovery**: `[mdns].name` enables both advertising (on `serve`) and discovery (on `mcp --remote`). `./thought-store scan` finds all instances and writes the selection to config. Uses `hashicorp/mdns` (pure Go, cross-platform).
- **Setup wizard**: `./thought-store setup` interactively checks/installs Ollama, prompts for SQLite path, and generates `thought-store.toml`. Uses `charmbracelet/huh` for forms and `lipgloss` for styled output. Detects package managers (paru/yay/pacman/brew).
- **Service management**: `./thought-store service` subcommand with install/uninstall/enable/disable/status/logs. Platform-agnostic `ServiceManager` interface with build-tagged implementations: systemd on Linux, stub on other platforms. User services by default, `--system` for system-level. Reusable pattern for future projects.
- **Distributed sync**: Gossip-style full-state snapshot exchange. `ExportBundle` checkpoints WAL, packs `brain.db` + `assets/` into a zstd tarball with canonical `brain.db` name. `ImportBundle` opens the foreign DB read-only, imports new instances by UUID, deduplicates thoughts by `unique_id`, transfers embeddings when models match (queues for re-embed when they don't). Append-only thoughts + UUIDs = CRDT by construction. CLI: `sync pull --from URL`, `sync push --to URL`. HTTP: `GET /api/sync` (export), `POST /api/sync` (import). `POST /api/reload` re-reads embeddings into VecIndex after external DB changes; `sync pull` calls it automatically.

## Tests

Integration tests against real Ollama (no mocks). SQLite tests use t.TempDir() for isolation. Tests skip gracefully if Ollama is unavailable.

- `ollama_test.go` — embedding dimension and non-zero check
- `sqlite_store_test.go` — SQLite insert/list/search/embeddings round-trip
- `remote_test.go` — RemoteBrain round-trip and URL fallback chain
- `e2e_test.go` — full HTTP pipeline on random port, including diagnostics endpoint
- `reembed_test.go` — batch embed, update embeddings, and post-re-embed search verification
- `mdns_test.go` — mDNS advertise/discover round-trip, ParsePort unit test
- `service_test.go` — ResolvePaths with temp files, missing file error handling
- `service_linux_test.go` — unit file path generation, systemctl show output parsing
- `setup_test.go` — package manager detection, install command generation, config file checks
- `sync_test.go` — export, unpack round-trip, import, transitive sync (C→B→A), model mismatch
- `sync_http_test.go` — HTTP sync round-trip (GET export, POST import between two servers)

## Future direction

Design docs: `docs/plans/2026-03-05-sync-design.md`, `docs/plans/2026-03-05-sync.md`

### Security (needs research)

No auth on any endpoint currently. Sync endpoints (`/api/sync`) are wide open. Needs:
- Authentication for HTTP API and sync endpoints
- Authorization model (who can push/pull)
- Encryption of bundles in transit (TLS) and at rest
- Requires research to get right — don't rush this.

### Chat integration ("bolt a brain on top")

The thought store is a storage/retrieval layer. A separate "brain" service would sit between chat platforms and the thought store, acting as an intermediary:
- **Inbound channels**: Slack, WhatsApp, Discord, browser plugin, Android app
- **Architecture**: Chat adapter → Brain service (LLM-powered) → Thought Store API
- The brain interprets natural language commands, decides when to capture vs search vs respond
- The thought store instance that faces chat services should have this brain layer; other instances just sync
- The MCP server already provides the right interface — the brain could be an MCP client

### Mobile (Android)

Need to figure out how to get thoughts in/out from an Android phone:
- Browser-based capture via the web UI (already works if server is reachable)
- Tasker/Automate integration hitting the HTTP API
- Share intent → HTTP POST to `/api/thoughts`
- Dedicated Android app (PWA or native) — heavyweight option
- Tailscale for reaching the home server from mobile

### Rich media (future)

Supplemental files, images, data sets, code snippets, audio, video, calendar data — stored as content-addressed blobs alongside the SQLite DB. The sync bundle tarball already reserves `assets/` for this.

### Thought refinement and forgetting (future — CRDT-safe)

**Core requirement:** thoughts must be revisable.

**Refinement:** Many small thoughts roll up into a summary thought. The summary is itself a regular thought (same table, same UUID). This nesting is arbitrarily deep — a DAG, not a tree.

New table `refined_thoughts`:
- `source_thought_id`, `source_thought_unique_id` (the contributing thought)
- `target_thought_id`, `target_thought_unique_id` (the summary thought)
- Both unique_id columns needed for sync

**Forgetting:** Soft-delete for "what was I thinking?" moments. New table `forgotten_thoughts` — a thought exists but is hidden from search and display.

**CRDT property is preserved:** No thought is ever mutated or deleted. Refinement adds new thoughts + new edge rows. Forgetting adds rows. All append-only, all sync naturally via UUID dedup. Two instances that independently refine the same sources produce different summaries (different UUIDs) — after sync, both exist, both valid.

**Search implications:** An "active" thought is one that is (a) not a source in `refined_thoughts` (has been rolled up) and (b) not in `forgotten_thoughts`. Search should prefer active thoughts. The VecIndex would need to hold only active thoughts, meaning refinement/forgetting must update the index (remove rolled-up/forgotten, add new summary).

**Conflict model:** Independent refinement on two instances is not a conflict — it's two valid perspectives. After sync you might have two different summaries of the same source thoughts. A "brain" layer could reconcile these, but the store layer doesn't need to.

**Open questions:** efficient SQL for "most refined" retrieval, VecIndex filtering, UI for exploring the DAG.

### Memory optimization (future)

At >1M thoughts (~4GB index), explore mmap, float16 quantization, or disk-backed ANN index.

## Config

`thought-store.toml` with sections: `[server]`, `[database]` (`path = "thought-store.db"`), `[ollama]`, `[mcp]`, `[mdns]`.

## Dependencies

- `github.com/go-chi/chi/v5` — router
- `github.com/a-h/templ` — type-safe HTML templates
- `github.com/jessevdk/go-flags` — CLI parsing
- `github.com/BurntSushi/toml` — config
- `modernc.org/sqlite` — pure Go SQLite driver
- `github.com/viterin/vek` — SIMD-accelerated vector operations
- `github.com/google/uuid` — UUID generation
- `github.com/mark3labs/mcp-go` — MCP server (stdio transport)
- `github.com/klauspost/compress/zstd` — fast compression for sync bundles
- `github.com/hashicorp/mdns` — mDNS service advertisement and discovery
- `github.com/charmbracelet/huh` — interactive terminal forms
- `github.com/charmbracelet/lipgloss` — styled terminal output
