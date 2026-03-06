# Thought Store

A personal thought capture and semantic search system. Record thoughts from anywhere — web UI, CLI, or API — and find them later by meaning, not keywords.

Inspired by Nate Jones's [Open Brain build guide](https://promptkit.natebjones.com/20260224_uq1_guide_main) and [PromptKit Prompt 1](https://promptkit.natebjones.com/20260224_uq1_promptkit_1) (memory seed migration).

Nate's Open Brain runs on cloud free tiers (Supabase + OpenRouter + Slack). This project takes the same core idea — SQLite storage with in-memory vector search, cosine similarity search, MCP integration, and the same 4-tool interface — but runs entirely self-hosted: local SQLite, local Ollama for embeddings, single Go binary, no internet required. Tailscale provides remote access when needed.

## What it does

- **Capture** thoughts as plain text from a chat-like web UI, CLI, or JSON API
- **Embed** each thought into a 1024-dimensional vector via local Ollama (mxbai-embed-large)
- **Store** text + vector in SQLite
- **Search** by meaning using cosine similarity — "why Go?" finds thoughts about performance philosophy

## Stack

Single Go binary. No npm, no node, no containers required.

- **Go** with chi router, templ templates, htmx
- **SQLite** (via modernc.org/sqlite, pure Go, no CGO)
- **In-memory SIMD vector search** (viterin/vek)
- **Ollama** running locally with mxbai-embed-large
- **CLI** via go-flags with `serve`, `capture`, `search`, `mcp`, `re-embed`, `scan`, `service`, `setup` subcommands
- **MCP** server for native Claude Code integration (stdio transport)

## Prerequisites

- Go 1.22+
- Ollama with `mxbai-embed-large` model pulled
- templ CLI (`go install github.com/a-h/templ/cmd/templ@latest`)

## Setup

```bash
# Clone and build
git clone https://github.com/corruptmemory/thought-store.git
cd thought-store
./build.sh --refresh-htmx
./build.sh build

# Interactive guided setup (checks Ollama, writes config)
./thought-store setup

# Or manually edit config
$EDITOR thought-store.toml

# Start server (runs migrations automatically)
./thought-store serve --config thought-store.toml
```

The `setup` wizard detects your package manager (pacman/yay/paru/brew), checks for Ollama, offers to install missing dependencies, and generates `thought-store.toml`. SQLite requires no provisioning — the database file is created automatically on first run.

Open http://localhost:3000 for the web UI.

## Usage

### Web UI

Open the browser. Type a thought, hit Send. Toggle to Search mode to find thoughts by meaning.

### CLI

```bash
# Capture (server must be running)
./thought-store capture "the auth system needs refactoring"

# Search
./thought-store search "authentication"
```

### Re-embed

Re-vectorize all thoughts with the current Ollama model (e.g. after a model upgrade):

```bash
# Dry run (default) — shows what would happen
./thought-store re-embed

# Actually update embeddings
./thought-store re-embed --apply

# Tune batch size (default 50)
./thought-store re-embed --apply --batch-size 100
```

### Network Discovery

Thought-store can advertise itself on the local network via mDNS, so remote clients find it automatically.

**Server side** — add to `thought-store.toml`:

```toml
[mdns]
name = "jim-brain"
```

The server advertises itself when `serve` starts. Remote clients discover it by name.

**Find instances on the network:**

```bash
# Scan for all thought-store instances
./thought-store scan

# Select one to auto-configure your config
```

**Remote mode auto-discovery:**

With `[mdns].name` configured, `mcp --remote` discovers the server automatically — no hardcoded URLs needed (though configured URLs still work as fallback).

### Service Management

Install thought-store as a system service (Linux/systemd):

```bash
# Install as user service (default)
./thought-store service install --config thought-store.toml

# Install with binary and config copied to standard locations
./thought-store service install --config thought-store.toml \
  --copy-binary --install-config

# Install as system service (requires sudo)
sudo ./thought-store service install --config thought-store.toml --system

# Enable, check status, view logs
./thought-store service enable
./thought-store service status
./thought-store service logs -f

# Uninstall
./thought-store service uninstall
```

### API

```bash
# Capture
curl -X POST http://localhost:3000/api/thoughts \
  -H 'Content-Type: application/json' \
  -d '{"content":"some thought"}'

# Search
curl 'http://localhost:3000/api/search?q=some+query'

# List recent
curl 'http://localhost:3000/api/thoughts?limit=20'
```

## Development

```bash
# Live reload
air

# Run tests (requires Ollama running for integration tests)
./build.sh test
```

## Config

`thought-store.toml`:

```toml
[server]
addr = ":3000"

[database]
path = "thought-store.db"

[ollama]
url = "http://localhost:11434"
model = "mxbai-embed-large"

[mcp]
urls = [
    "http://localhost:3000",
    "http://100.122.102.93:3000",
]

[mdns]
name = "jim-brain"
```

## MCP Integration (Claude Code)

The thought store includes an MCP server for native Claude Code integration.

### Local mode (same machine as Ollama)

Register with Claude Code by adding to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "thought-store": {
      "command": "./thought-store",
      "args": ["mcp", "--config", "thought-store.toml"]
    }
  }
}
```

### Remote mode (laptop, other machines via Tailscale)

Configure URLs in `thought-store.toml`:

```toml
[mcp]
urls = [
    "http://localhost:3000",
    "http://100.122.102.93:3000",
]
```

Then register with the `--remote` flag:

```json
{
  "mcpServers": {
    "thought-store": {
      "command": "./thought-store",
      "args": ["mcp", "--config", "thought-store.toml", "--remote"]
    }
  }
}
```

The remote client tries each URL in order. Put localhost first for zero-latency when on the same machine, Tailscale IP last as the universal fallback.

## Design principles

- **Self-hosted**: Your thoughts stay on your machine. No cloud, no rent.
- **Single binary**: Build once, deploy anywhere. All assets embedded.
- **Simple**: No abstractions beyond what's needed. Anti-clever by design.
- **Portable**: SQLite file = your entire brain. Copy it, sync it, back it up.
- **Honest retrieval**: The store is only as useful as you are truthful with it.
