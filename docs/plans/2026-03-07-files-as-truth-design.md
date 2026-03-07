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

## Sync: Git for Text, rsync for Binaries

### Branch-per-machine model

Each machine has an identity and a corresponding Git branch:

- Machine A works on branch `jim-desktop`
- Machine B works on branch `jim-laptop`

When Machine B wants to merge with Machine A:
1. `git fetch` Machine A's branch
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

### Binary assets

Git handles text thoughts beautifully. Binaries (images, video, audio) need a different approach:

| Context | Text (thoughts) | Binaries (assets) |
|---|---|---|
| **Peer-to-peer** | `git fetch` + merge | API manifest handshake + rsync |
| **Forge publish** | `git push` + PR | `git-lfs` (forge handles storage) |

**Peer-to-peer binary sync flow:**
1. Peers exchange asset manifests (path, SHA256, size) — cached in the DB for fast lookup
2. Local instance diffs against its own manifest, identifies truly new/changed assets
3. rsync transfers only deltas (rolling checksum algorithm handles partial changes to large files)

**Forge binary sync:**
- git-lfs: pointer file in Git, blob on the forge's LFS storage
- GitHub, GitLab, Gitea all support it
- LFS only breaks down in P2P (assumes central server) — which is exactly where we use rsync instead

```
Machine A                          Machine B
    |                                  |
    |<--- git fetch jim-laptop --------|  (text: Git handles it)
    |                                  |
    |<--- GET /api/assets/manifest ----|  (list of {path, sha256, size})
    |                                  |
    |  (diff against local manifest)   |
    |  (identify missing/changed)      |
    |                                  |
    |<--- rsync <missing assets> ------|  (binary: rsync handles it)
    |                                  |
    |  (update local asset cache/DB)   |
    |  (git merge jim-laptop)          |
    |                                  |
```

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
| rsync | native | native | **problematic** | WSL, MSYS2, or Go implementation needed |
| File naming | no issues | case-insensitive FS | no colons, 260-char limit, case-insensitive | Use Denote-style timestamp (no colons) |
| fsnotify | inotify | kqueue | ReadDirectoryChangesW | Go library handles all three |
| SQLite | pure Go | pure Go | pure Go | No issues |
| Ollama | native | native | native | No issues |
| git-lfs | native | native | native | Bundled with Git for Windows |

**The rsync problem on Windows:** Options include (a) require WSL, (b) pure Go rsync implementation, (c) different binary delta approach on Windows, or (d) Windows gets forge-only sync via git-lfs, no P2P binary sync.

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

### Encryption at rest
Parked for separate exploration. Options differ in files-as-truth model:
- `git-crypt` for selective file encryption in the repo
- Filesystem-level encryption (LUKS/dm-crypt) for the whole directory
- The DB is a cache — if it leaks, it has embeddings and metadata but not thought content (if thoughts are encrypted at the file level)
- CGO-free constraint rules out SQLCipher for the DB itself
