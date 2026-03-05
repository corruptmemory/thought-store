# Thought Store

A personal thought capture and semantic search system. Record thoughts from anywhere — web UI, CLI, or API — and find them later by meaning, not keywords.

Inspired by [Nate Jones's PromptKit / Open Brain](https://promptkit.natebjones.com) concept.

## What it does

- **Capture** thoughts as plain text from a chat-like web UI, CLI, or JSON API
- **Embed** each thought into a 1024-dimensional vector via local Ollama (mxbai-embed-large)
- **Store** text + vector in Postgres with pgvector
- **Search** by meaning using cosine similarity — "why Go?" finds thoughts about performance philosophy

## Stack

Single Go binary. No npm, no node, no containers required.

- **Go** with chi router, templ templates, htmx
- **Postgres** with pgvector extension
- **Ollama** running locally with mxbai-embed-large
- **CLI** via go-flags with `serve`, `capture`, `search`, `mcp` subcommands
- **MCP** server for native Claude Code integration (stdio transport)

## Prerequisites

- Go 1.22+
- Postgres with pgvector extension enabled
- Ollama with `mxbai-embed-large` model pulled
- templ CLI (`go install github.com/a-h/templ/cmd/templ@latest`)

## Setup

```bash
# Clone and build
git clone https://github.com/corruptmemory/thought-store.git
cd thought-store
./build.sh --refresh-htmx
./build.sh build

# Edit config
$EDITOR thought-store.toml

# Start server (runs migrations automatically)
./thought-store serve --config thought-store.toml
```

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

# Run tests (requires Postgres and Ollama running)
./build.sh test
```

## Config

`thought-store.toml`:

```toml
[server]
addr = ":3000"

[database]
url = "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable"

[ollama]
url = "http://localhost:11434"
model = "mxbai-embed-large"

[mcp]
urls = [
    "http://localhost:3000",
    "http://100.122.102.93:3000",
]
```

## MCP Integration (Claude Code)

The thought store includes an MCP server for native Claude Code integration.

### Local mode (same machine as Postgres/Ollama)

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
- **Honest retrieval**: The store is only as useful as you are truthful with it.
