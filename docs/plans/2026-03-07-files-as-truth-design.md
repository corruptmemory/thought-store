# Files as Truth: Architectural Inversion Design

> **Status:** Exploratory. High confidence in the direction, decisions not yet finalized.

## The Core Idea

Invert the architecture: thoughts are plain text files (markdown, Org, whatever), not database rows. The SQLite database becomes a derived cache — embeddings, parsed metadata indexes, full-text search acceleration. Delete the DB, rebuild from files, lose nothing.

## Why

### The tarball problem (forcing function)

The current sync model (`ExportBundle` / `ImportBundle`) ships a zstd tarball of the **entire** `brain.db` + `assets/` directory every time. This is O(everything) per sync. At 100 thoughts it's instant. At 10,000 thoughts with binary assets, it's shipping hundreds of megabytes to transfer one new thought. The math is merciless — moving to delta-based sync is inevitable.

### What files-as-truth enables

- **Human-readable, human-editable, zero-tooling-required.** `vim thoughts/some-thought.md` — done.
- **Git gives you versioning, branching, diffing, blame for free.** The entire history of how a thought evolved is just `git log -p`.
- **Editor-native workflow.** Your editor IS the primary interface. The thought-store binary becomes a background service that watches the directory, maintains the index, and exposes semantic search via a socket/pipe. Emacs, vim, whatever — keybind to semantic search, jump to file, edit, save. The watcher re-indexes.
- **Backup is `cp -r` or `git push`.** No export/import dance.
- **Durability without the DB.** If the DB is lost, rebuild from files. Slow (re-embedding), but no data loss.

## Prior Art

### Org-roam

- Files are the source of truth — plain `.org` files in a directory
- SQLite is a cache — crawls the directory, extracts nodes/links/tags/refs
- 7 tables: `files`, `nodes`, `links`, `aliases`, `citations`, `refs`, `tags`
- If you delete the DB, `M-x org-roam-db-sync` rebuilds it from files
- Uses Zettelkasten-style linked notes with backlinks
- No semantic/vector search

### Denote (Protesilaos Stavrou)

- **No database at all.** Everything derived from file names and front matter.
- File naming convention: `20240322T131856--some-title__topic1_topic2.org`
  - Timestamp identifier (no colons — Windows-safe)
  - `--` separator before title
  - `__` separator before keywords
  - Keywords separated by `_`
- Front matter mirrors the filename (title, date, filetags, identifier)
- Standard Unix tools (`ls`, `grep`, `find`) can search/filter notes without any special software
- Methodology agnostic — doesn't impose Zettelkasten or any system
- Links use the identifier — robust across renames

### Where thought-store sits

```
Denote          Org-roam              thought-store (proposed)
--------------------------------------------------------------------
Files only      Files + SQLite cache  Files + SQLite cache + vectors
No DB           DB is disposable      DB is disposable
grep/find       Fast graph queries    Fast graph + semantic search
No linking DB   Backlinks via DB      Backlinks + "thoughts like this"
```

Files as truth (like both), with a SQLite cache (like Org-roam), but also with vector embeddings for semantic search (like neither).

## Sync: Git + LFS for Everything

### Key discovery: git-lfs over pure SSH (2026-03-08)

The original design split sync into "git for text, rsync for binaries" — but rsync on Windows was a known problem. Research revealed that **git-lfs v3.0+ supports a pure SSH transfer protocol**, eliminating the need for an HTTP LFS server *and* for rsync:

- **`charmbracelet/git-lfs-transfer`** — a Go binary that implements the server side of the pure SSH LFS protocol. Install on each node: `go install github.com/charmbracelet/git-lfs-transfer@latest`
- When the git remote is an SSH URL, git-lfs automatically uses SSH transport
- No HTTP server required for P2P. Forges (GitHub/GitLab/Gitea) use their own HTTP LFS storage as usual.

This unifies the sync story: **git handles text, git-lfs handles binaries, SSH is the transport for both in P2P mode.**

### Branch-per-machine model

Each machine has an identity and a corresponding Git branch:

- Machine A works on branch `jim-desktop`
- Machine B works on branch `jim-laptop`

When Machine B wants to merge with Machine A:
1. `git fetch ssh://jim-desktop/~/thoughts` (text + LFS objects over SSH)
2. `git merge jim-desktop` into `jim-laptop`
3. Success: done. Failure: human or AI resolves conflicts, or abort.

This is genuinely peer-to-peer — no "main" is required. Git used as a DVCS the way Linus intended, not the GitHub "main is canonical" model.

```
jim-desktop ──*──*──*──*──*──────*── (merge from laptop)
                                /
jim-laptop  ──*──*──*──*──*────
```

### Merge semantics are good for thoughts

- Two peers both **add new files** (new thoughts): no conflict, both appear
- Two peers **edit the same thought**: real conflict, Git shows exactly which lines differ
- Merge failure modes map to the three-tier brain:
  - MCP mode: AI agent resolves conflicts (it IS the brain)
  - Brain mode: embedded LLM attempts auto-resolution
  - Fallback: human sees conflict markers, decides

### The forge: optional, not required

A "forge" (GitHub, GitLab, Gitea, bare repo on a NAS) is an optional always-on peer:

- `thought-store publish --forge gitlab` pushes the machine branch and opens a PR/MR to `main`
- The PR is a curated reconciliation checkpoint — review what's new before it hits `main`
- `main` on the forge serves as a seed for new machines: clone, checkout new branch, go
- Backup by construction — durable off-site copy without thinking about it
- Sharing, if you ever want it — give someone access, they see `main`, never your working branches

### Binary assets via git-lfs

Git handles text thoughts natively. Binaries (images, video, audio, web clippings) are tracked via git-lfs:

| Context | Text (thoughts) | Binaries (assets) |
|---|---|---|
| **Peer-to-peer** | `git fetch` over SSH | `git-lfs` over pure SSH (`git-lfs-transfer`) |
| **Forge publish** | `git push` + PR | `git-lfs` over HTTP (forge stores blobs) |

**One tool for both modes.** The only difference is the transport: SSH for P2P, HTTP for forge.

**Selective sync via LFS controls:**
- `GIT_LFS_SKIP_SMUDGE=1 git fetch` — fetch text + pointer files, skip downloading blobs
- `git lfs pull --include="*.png"` — download only specific asset types
- `git lfs fetch --recent` — download only recently referenced LFS objects
- `.lfsconfig` per-repo can set `lfs.fetchexclude` patterns

**LFS objects are whole-file transfers** (by content hash, not rsync-style deltas). This is fine for the expected asset types — images, web clippings, audio — which are write-once. rsync's rolling checksum advantage only matters for large files with small edits (video editing, database files), which isn't our use case.

```
Machine A                          Machine B
    |                                  |
    |<--- git fetch over SSH ----------|  (text: git objects)
    |                                  |  (binaries: git-lfs over SSH)
    |                                  |
    |  (git merge jim-laptop)          |
    |                                  |
```

### Prerequisites per node

For P2P sync to work, each node needs:
1. **Git** (with LFS extension) — standard on all platforms
2. **`git-lfs-transfer`** — Charmbracelet's Go binary, server side of pure SSH LFS protocol
3. **SSH access between nodes** — see "SSH between nodes" in Open Questions

## The DB as Cache

### What the SQLite cache stores

- Embeddings (vector index for semantic search)
- Parsed metadata from front matter (people, tags, dates, action items)
- Asset manifest (path, SHA256, size, mtime)
- Computed relationships (backlinks, refinement DAG)
- Full-text search index (FTS5)

### Cache lifecycle

- **On startup / on file change (fsnotify):** scan thought directory, parse front matter, generate embeddings for new/changed files, update SQLite
- **If DB is lost:** rebuild from files — re-parse all front matter, re-embed all content via Ollama. Slow but lossless.
- **The `re-embed` command becomes:** "rebuild index from scratch"

## Cross-Platform Concerns

| Component | Linux | macOS | Windows | Notes |
|---|---|---|---|---|
| Git | native | native | native | No issues |
| git-lfs | native | native | native | Bundled with Git for Windows |
| git-lfs-transfer | `go install` | `go install` | `go install` | Pure Go binary, cross-platform |
| SSH | native | native | OpenSSH bundled since Win10 | See "SSH between nodes" below |
| File naming | no issues | case-insensitive FS | no colons, 260-char limit, case-insensitive | Use Denote-style timestamp (no colons) |
| fsnotify | inotify | kqueue | ReadDirectoryChangesW | Go library handles all three |
| SQLite | pure Go | pure Go | pure Go | No issues |
| Ollama | native | native | native | No issues |

**rsync eliminated.** The move to git-lfs over pure SSH resolves the Windows rsync problem entirely. All components are now cross-platform without caveats.

**File naming safety:** avoid colons in identifiers (use `20260307T143000` not `14:30:00`), keep paths reasonably short, use consistent casing.

## Open Questions

### File format
Markdown + YAML front matter? Org mode? Something custom? Determines parsing, metadata extraction, editor support.

### Identity
Denote uses timestamps, Org-roam uses UUIDs. Current thought-store uses UUIDs. What's the canonical identifier in the files-as-truth world? Needs to be filename-safe across platforms.

### Atomic operations
Current `RefineThoughts` is a single DB transaction. In files-as-truth: write multiple files + update front matter + commit Git. More surface area for partial failures. Needs exploration.

### Cache rebuild contract
If the DB is truly disposable, rebuilding requires Ollama for re-embedding. Is that acceptable? Does the embedding cache need its own durability story (e.g., a sidecar file per thought with its embedding)?

### Refinement DAG representation
Currently a join table in SQLite. In files-as-truth, options include:
- Wiki-style `[[references]]` in the summary file
- Front matter arrays listing source thought identifiers
- The DB reconstructs the DAG from these file-level references

### SSH between nodes

Git-lfs over pure SSH eliminates rsync and the HTTP LFS server, but introduces a new dependency: SSH connectivity between nodes. This is the transport layer for P2P sync.

**What's needed:** Each node must accept SSH connections from peers and have `git-lfs-transfer` on PATH.

**Options for the network layer:**
- **Embedded SSH server** — `golang.org/x/crypto/ssh` is a full SSH client+server in pure Go (no CGO). thought-store already embeds an HTTP server; adding an SSH listener on another port keeps the "single binary, all self-contained" philosophy. Peers connect to each other's embedded SSH, same as they currently connect to each other's HTTP for tarball sync. This also means `git-lfs-transfer` could be embedded rather than a separate binary on PATH.
- **Tailscale SSH** — already in the stack for remote access. `tailscale ssh` provides authenticated, encrypted connections without managing keys or opening ports. Nodes are addressable by Tailscale hostname.
- **Plain SSH** — works on LAN. Requires key exchange or password auth. Firewall/port management. Standard but more manual setup.
- **Tailscale Funnel** — exposes a node to the public internet via Tailscale's edge. Probably overkill for P2P thought sync, but could enable sync with untrusted networks.

**The embedded SSH angle:** The most self-contained option is to embed the SSH server in the thought-store binary itself. `golang.org/x/crypto/ssh` provides everything needed: server listener, pubkey/password auth, channel handling, subsystem dispatch. Charmbracelet's ecosystem (`soft-serve`, `wish`) already uses this exact pattern — Go binaries that are their own SSH servers. This means:
- No dependency on system `sshd` configuration
- thought-store controls its own auth (could use its own key exchange, or piggyback on Tailscale identity)
- Git operations target `ssh://jim-desktop:2222/thoughts` (thought-store's embedded SSH port)
- `git-lfs-transfer` protocol handler could be embedded directly (it's a Go library under the hood)
- Works identically on Linux, macOS, Windows — no platform-specific SSH setup

**Prior art in the Charmbracelet ecosystem:**
- `soft-serve` — pure Go git server with SSH, includes LFS support
- `git-lfs-transfer` — the SSH LFS protocol handler, already written in Go, potentially embeddable as a library
- `wish` — framework for building SSH apps in Go

**The Tailscale angle:** If both nodes are on the same Tailnet, Tailscale handles DNS, auth, and encryption at the network layer. The embedded SSH server still provides the git/LFS protocol, but Tailscale provides the secure transport underneath. This is the happy path for remote (non-LAN) sync.

**Setup implications:** `thought-store setup` may need to:
1. Generate a host key for the embedded SSH server (or derive from instance identity)
2. Exchange/trust peer public keys (or rely on Tailscale for auth)
3. Configure git remotes with the right SSH URLs pointing at thought-store's SSH port

**Discovery:** mDNS currently discovers HTTP endpoints for the tarball sync. In the git+SSH world, mDNS TXT records should also advertise the SSH port and repo path. `thought-store scan` would show both HTTP and SSH endpoints.

### Security and trust model

Parked for separate deep exploration. The motivating idea is a **chain of trust with transitive closure**, inspired by Brave browser's trust model:

- Node A trusts Node B (explicit key exchange or Tailscale identity)
- Node B trusts Node C
- Therefore Node A can transitively trust Node C (to some configurable depth/policy)

This maps naturally to the gossip-style sync already in the design — thoughts propagate transitively through pairwise syncs, so trust should too. The trust graph and the sync graph are the same topology.

**Open sub-questions:**
- Trust granularity: all-or-nothing per node, or per-capability (read vs write vs sync)?
- Trust attenuation: does trust weaken over transitive hops? (depth limit, capability narrowing)
- Revocation: how does a node revoke trust, and how does that propagate?
- Identity: what IS a node's identity? SSH host key? Tailscale identity? Self-signed cert? Instance UUID + signature?
- Key management UX: `thought-store trust add <node>`, `thought-store trust list`, etc.?

### Encryption at rest

Parked for separate exploration. Options differ in files-as-truth model:
- `git-crypt` for selective file encryption in the repo
- Filesystem-level encryption (LUKS/dm-crypt) for the whole directory
- The DB is a cache — if it leaks, it has embeddings and metadata but not thought content (if thoughts are encrypted at the file level)
- CGO-free constraint rules out SQLCipher for the DB itself
