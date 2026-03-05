# Setup Subcommand Design

## Goal

`thought-store setup` guides a new user from zero to a working config file, checking and provisioning all dependencies along the way.

## Decisions

- **Top-level subcommand**: `thought-store setup`, not nested under `serve`
- **Interactive library**: `charmbracelet/huh` for forms, `charmbracelet/lipgloss` for styled output. No bubbletea.
- **Install strategy**: Hybrid — print system package install commands and ask "want me to run this?", do Postgres provisioning (CREATE USER/DB/EXTENSION) directly
- **Postgres credentials**: Ask for app credentials (user/pass/db) with defaults, then ask for admin user. Ask whether the app user requires a password.
- **Config file**: Check for existing file upfront. Warn and let user bail. If they continue, it gets replaced at the end.
- **Config path**: Always `./thought-store.toml` (the default)
- **Embedding model**: Locked to `mxbai-embed-large`. Not configurable in setup. The schema hardcodes `vector(1024)`. Prominent comments flag the coupling for future work.
- **Scope**: Server addr, database, ollama, optional mDNS. No MCP/remote config (future `setup-remote` subcommand).
- **Windows**: `runtime.GOOS == "windows"` early exit with friendly "not yet supported" message.

## Architecture

Sequential step functions called from `SetupCmd.Execute()`. Each step receives the in-progress config and returns it updated.

### Files

- `setup.go` — SetupCmd, Execute(), step functions, package manager detection
- `setup_prompts.go` — huh form wrappers for each prompt

### Flow

1. **Config file check** — Exists? Warn, ask continue or bail.
2. **Detect package manager** — `paru` > `yay` > `pacman` > `brew` via `exec.LookPath`. Store for install offers.
3. **Check Postgres**
   - `psql` on PATH? If not, offer install.
   - Server running? Try `psql -U postgres -c "SELECT 1"`. If not, tell them to start it.
   - Ask: local or remote Postgres?
     - Remote: ask for full connection URL, validate, skip provisioning.
     - Local: gather app credentials (user, password y/n, password if yes, database, admin user), then CREATE USER, CREATE DATABASE, CREATE EXTENSION vector.
   - Handle "already exists" gracefully for user/db.
4. **Check Ollama**
   - HTTP GET `http://localhost:11434/api/version`. If not reachable, check `ollama` on PATH, offer install.
   - If on PATH but not responding, tell them to start it.
   - Check if `mxbai-embed-large` is pulled via `/api/show`. If not, offer to pull (subprocess with piped output).
5. **Server config** — Ask listen address (default `:3000`).
6. **mDNS (optional)** — "Advertise on local network?" If yes, ask instance name.
7. **Write config** — Write `thought-store.toml`, print summary, print next steps.

### Package Manager Mapping

| Package | pacman | paru/yay | brew |
|---------|--------|----------|------|
| postgresql | `postgresql` | `postgresql` | `postgresql@17` |
| ollama | `ollama` | `ollama` | `ollama` |

paru/yay don't need sudo. pacman needs `sudo`. brew doesn't need sudo.

### Postgres Provisioning (local only)

Connect as admin user, then:

```sql
-- Check/create user
CREATE USER thoughtstore WITH PASSWORD 'thoughtstore';
-- or without password:
CREATE USER thoughtstore;

-- Check/create database
CREATE DATABASE thoughtstore OWNER thoughtstore;

-- Enable pgvector (connect to the new database)
CREATE EXTENSION IF NOT EXISTS vector;
```

Check `pg_roles` and `pg_database` before creating to avoid errors and report "already exists" gracefully.

### Future: Embedding Dimension Decoupling

The schema currently hardcodes `vector(1024)` tied to `mxbai-embed-large`. To support multiple models, embeddings should move to a separate table with a 1:1 FK to thoughts. This would let `re-embed` create new embeddings with a different dimension and swap atomically. Out of scope for this design — prominent comments in `store.go` and `setup.go` flag the coupling.

## Dependencies

- `github.com/charmbracelet/huh` — interactive forms
- `github.com/charmbracelet/lipgloss` — styled terminal output
