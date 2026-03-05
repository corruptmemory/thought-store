# MCP Server Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add an MCP (Model Context Protocol) server to thought-store so Claude Code can natively capture, search, list, and query stats from the thought store.

**Architecture:** Extract a `ThoughtStore` interface from the existing `Store`+`OllamaClient` pair. Two implementations: `LocalBrain` (direct Postgres+Ollama, used on the same machine) and `RemoteBrain` (HTTP client with URL fallback chain, used from other machines). A new `mcp` subcommand speaks stdio MCP using the `mark3labs/mcp-go` library. The existing HTTP server handlers are refactored to use the same interface.

**Tech Stack:** Go 1.26, mark3labs/mcp-go, existing chi/pgx/pgvector/ollama stack

**Build rule:** Always use `./build.sh` — never invoke `go build`, `go test`, `templ generate` directly.

---

### Task 1: Add `thought_stats` API endpoint

The MCP server needs a stats tool, but the HTTP API doesn't have a stats endpoint yet. Add it first so both surfaces can use it.

**Files:**
- Modify: `store.go`
- Modify: `server.go`
- Modify: `e2e_test.go`

**Step 1: Add StoreStats type and Stats method to store.go**

Add after the `SearchResult` type:

```go
type StoreStats struct {
	Total       int               `json:"total"`
	OldestAt    *time.Time        `json:"oldest_at,omitempty"`
	NewestAt    *time.Time        `json:"newest_at,omitempty"`
	BySource    map[string]int    `json:"by_source"`
}
```

Add the Stats method to Store:

```go
func (s *Store) Stats(ctx context.Context) (*StoreStats, error) {
	var stats StoreStats
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM thoughts`,
	).Scan(&stats.Total, &stats.OldestAt, &stats.NewestAt)
	if err != nil {
		return nil, fmt.Errorf("stats query: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT source, COUNT(*) FROM thoughts GROUP BY source`)
	if err != nil {
		return nil, fmt.Errorf("stats by source: %w", err)
	}
	defer rows.Close()

	stats.BySource = make(map[string]int)
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			return nil, fmt.Errorf("scan source count: %w", err)
		}
		stats.BySource[source] = count
	}
	return &stats, rows.Err()
}
```

**Step 2: Add the stats endpoint to server.go**

Add route in `NewServer`:

```go
s.router.Get("/api/stats", s.handleStats)
```

Add handler:

```go
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context())
	if err != nil {
		http.Error(w, "stats failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
```

**Step 3: Add a stats test to e2e_test.go**

Add after the search assertions in TestE2E:

```go
// Stats
resp, err = client.Get(base + "/api/stats")
if err != nil {
	t.Fatalf("stats: %v", err)
}
defer resp.Body.Close()

var stats StoreStats
json.NewDecoder(resp.Body).Decode(&stats)
if stats.Total == 0 {
	t.Fatal("expected non-zero total in stats")
}
```

**Step 4: Run tests**

Run: `./build.sh test`
Expected: All tests pass

**Step 5: Commit**

```bash
git add store.go server.go e2e_test.go
git commit -m "Add thought stats API endpoint with count, date range, and source breakdown"
```

---

### Task 2: Extract ThoughtStore interface

**Files:**
- Create: `brain.go`
- Modify: `server.go`

**Step 1: Create brain.go with the interface and LocalBrain**

```go
package main

import "context"

// ThoughtStore is the core interface for interacting with the thought store.
// Both LocalBrain (direct Postgres+Ollama) and RemoteBrain (HTTP client) implement it.
type ThoughtStore interface {
	Capture(ctx context.Context, content string, source string) (*Thought, error)
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	List(ctx context.Context, limit, offset int) ([]Thought, error)
	Stats(ctx context.Context) (*StoreStats, error)
}

// LocalBrain implements ThoughtStore using direct Postgres and Ollama access.
type LocalBrain struct {
	store  *Store
	ollama *OllamaClient
}

func NewLocalBrain(store *Store, ollama *OllamaClient) *LocalBrain {
	return &LocalBrain{store: store, ollama: ollama}
}

func (b *LocalBrain) Capture(ctx context.Context, content string, source string) (*Thought, error) {
	embedding, err := b.ollama.Embed(ctx, content)
	if err != nil {
		return nil, err
	}
	return b.store.Insert(ctx, content, embedding, source)
}

func (b *LocalBrain) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	embedding, err := b.ollama.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	return b.store.Search(ctx, embedding, limit)
}

func (b *LocalBrain) List(ctx context.Context, limit, offset int) ([]Thought, error) {
	return b.store.List(ctx, limit, offset)
}

func (b *LocalBrain) Stats(ctx context.Context) (*StoreStats, error) {
	return b.store.Stats(ctx)
}
```

**Step 2: Refactor server.go to use ThoughtStore interface**

Change `Server` struct to hold a `ThoughtStore` instead of separate `*Store` and `*OllamaClient`:

```go
type Server struct {
	router *chi.Mux
	brain  ThoughtStore
	store  *Store // retained for handleHome which needs List directly
}
```

Wait — actually, `handleHome`, `handleCapture`, `handleList`, `handleSearch`, and `handleStats` all map cleanly to the interface methods. The only wrinkle is `handleCapture` which currently calls `ollama.Embed` then `store.Insert` separately — that's exactly what `LocalBrain.Capture` does. So the refactor is clean:

Replace the `Server` struct and `NewServer`:

```go
type Server struct {
	router *chi.Mux
	brain  ThoughtStore
}

func NewServer(brain ThoughtStore) *Server {
	s := &Server{
		router: chi.NewRouter(),
		brain:  brain,
	}
	// ... routes unchanged ...
	return s
}
```

Update `handleCapture` to use `s.brain.Capture()` instead of separate embed+insert.

Update `handleList` to use `s.brain.List()`.

Update `handleSearch` to use `s.brain.Search()` (passing the query string, not embedding).

Update `handleStats` to use `s.brain.Stats()`.

Update `handleHome` to use `s.brain.List()`.

**Step 3: Update main.go serve command**

Change from:

```go
ollama := NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)
srv := NewServer(store, ollama)
```

To:

```go
ollama := NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)
brain := NewLocalBrain(store, ollama)
srv := NewServer(brain)
```

**Step 4: Run tests**

Run: `./build.sh test`
Expected: All tests pass (behavior unchanged, just restructured)

**Step 5: Commit**

```bash
git add brain.go server.go main.go
git commit -m "Extract ThoughtStore interface with LocalBrain implementation"
```

---

### Task 3: Implement RemoteBrain (HTTP client with fallback chain)

**Files:**
- Create: `remote.go`
- Create: `remote_test.go`

**Step 1: Write the test**

The test starts a real HTTP server (like e2e_test.go does) and verifies RemoteBrain can talk to it.

```go
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRemoteBrain(t *testing.T) {
	ollamaAvailable(t)

	ctx := context.Background()
	store, err := NewStore(ctx, "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable")
	if err != nil {
		t.Skip("Postgres not available")
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ollama := NewOllamaClient("http://localhost:11434", "mxbai-embed-large")
	brain := NewLocalBrain(store, ollama)
	srv := NewServer(brain)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go http.Serve(ln, srv)

	base := fmt.Sprintf("http://%s", ln.Addr().String())
	remote := NewRemoteBrain([]string{base}, 5*time.Second)

	// Capture
	thought, err := remote.Capture(ctx, "remote test: distributed systems are hard", "test")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if thought.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// Search
	results, err := remote.Search(ctx, "distributed computing", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results")
	}

	// List
	thoughts, err := remote.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(thoughts) == 0 {
		t.Fatal("expected thoughts")
	}

	// Stats
	stats, err := remote.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total == 0 {
		t.Fatal("expected non-zero total")
	}

	// Cleanup
	store.pool.Exec(ctx, "DELETE FROM thoughts WHERE source = 'test' AND content LIKE 'remote test:%'")
}

func TestRemoteBrainFallback(t *testing.T) {
	ollamaAvailable(t)

	ctx := context.Background()
	store, err := NewStore(ctx, "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable")
	if err != nil {
		t.Skip("Postgres not available")
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ollama := NewOllamaClient("http://localhost:11434", "mxbai-embed-large")
	brain := NewLocalBrain(store, ollama)
	srv := NewServer(brain)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go http.Serve(ln, srv)

	base := fmt.Sprintf("http://%s", ln.Addr().String())

	// First URL is unreachable, second is the real server
	remote := NewRemoteBrain([]string{"http://127.0.0.1:1", base}, 2*time.Second)

	stats, err := remote.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats with fallback: %v", err)
	}
	if stats.Total == 0 {
		t.Fatal("expected non-zero total via fallback")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `NewRemoteBrain` not defined

**Step 3: Implement remote.go**

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// RemoteBrain implements ThoughtStore by talking to a thought-store HTTP server.
// It tries URLs in order, using the first one that responds.
type RemoteBrain struct {
	urls    []string
	client  *http.Client
}

func NewRemoteBrain(urls []string, timeout time.Duration) *RemoteBrain {
	return &RemoteBrain{
		urls:   urls,
		client: &http.Client{Timeout: timeout},
	}
}

func (b *RemoteBrain) Capture(ctx context.Context, content string, source string) (*Thought, error) {
	body, _ := json.Marshal(captureRequest{Content: content})
	resp, err := b.doRequest(ctx, "POST", "/api/thoughts", bytes.NewReader(body), http.Header{
		"Content-Type": {"application/json"},
		"X-Source":     {source},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("capture failed (%d): %s", resp.StatusCode, msg)
	}

	var t Thought
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode thought: %w", err)
	}
	return &t, nil
}

func (b *RemoteBrain) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	path := fmt.Sprintf("/api/search?q=%s&limit=%d", url.QueryEscape(query), limit)
	resp, err := b.doRequest(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var results []SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}
	return results, nil
}

func (b *RemoteBrain) List(ctx context.Context, limit, offset int) ([]Thought, error) {
	path := fmt.Sprintf("/api/thoughts?limit=%d&offset=%d", limit, offset)
	resp, err := b.doRequest(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var thoughts []Thought
	if err := json.NewDecoder(resp.Body).Decode(&thoughts); err != nil {
		return nil, fmt.Errorf("decode thoughts: %w", err)
	}
	return thoughts, nil
}

func (b *RemoteBrain) Stats(ctx context.Context) (*StoreStats, error) {
	resp, err := b.doRequest(ctx, "GET", "/api/stats", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var stats StoreStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}
	return &stats, nil
}

// doRequest tries each URL in the fallback chain until one responds.
func (b *RemoteBrain) doRequest(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	var lastErr error
	for _, base := range b.urls {
		req, err := http.NewRequestWithContext(ctx, method, base+path, body)
		if err != nil {
			lastErr = err
			continue
		}
		for k, v := range headers {
			req.Header[k] = v
		}

		resp, err := b.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("all URLs unreachable: %w", lastErr)
}
```

Note: The `body` parameter in `doRequest` is an `io.Reader`, which can only be read once. For the fallback chain to retry with a body (POST requests), we need to handle re-reading. Since our only POST body is small JSON, we can use `bytes.NewReader` which supports `Seek`. Update `doRequest` to reset the reader on retry:

Replace the body handling in `doRequest`:

```go
func (b *RemoteBrain) doRequest(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	var lastErr error
	for _, base := range b.urls {
		// Reset body reader for retry if it supports seeking
		if seeker, ok := body.(io.Seeker); ok {
			seeker.Seek(0, io.SeekStart)
		}

		req, err := http.NewRequestWithContext(ctx, method, base+path, body)
		if err != nil {
			lastErr = err
			continue
		}
		for k, v := range headers {
			req.Header[k] = v
		}

		resp, err := b.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("all URLs unreachable: %w", lastErr)
}
```

**Step 4: Run tests**

Run: `./build.sh test`
Expected: All tests pass, including the fallback test

**Step 5: Commit**

```bash
git add remote.go remote_test.go
git commit -m "Add RemoteBrain with HTTP client and URL fallback chain"
```

---

### Task 4: Add MCP server subcommand

**Files:**
- Create: `mcp.go`
- Modify: `main.go`
- Modify: `config.go`

**Step 1: Add MCP config to config.go**

Add to Config struct:

```go
type MCPConfig struct {
	URLs []string `toml:"urls"`
}
```

Add field to Config:

```go
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Database DatabaseConfig `toml:"database"`
	Ollama   OllamaConfig   `toml:"ollama"`
	MCP      MCPConfig      `toml:"mcp"`
}
```

**Step 2: Create mcp.go with MCP server setup**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func newMCPServer(brain ThoughtStore) *server.MCPServer {
	s := server.NewMCPServer(
		"thought-store",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	s.AddTool(
		mcp.NewTool("capture_thought",
			mcp.WithDescription("Capture a new thought into the thought store. Use this to save insights, decisions, preferences, facts, or anything worth remembering."),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("The thought to capture"),
			),
			mcp.WithString("source",
				mcp.Description("Source tag for provenance (e.g. conversation, cli, web)"),
				mcp.DefaultString("conversation"),
			),
		),
		captureHandler(brain),
	)

	s.AddTool(
		mcp.NewTool("search_thoughts",
			mcp.WithDescription("Semantically search the thought store by meaning. Returns thoughts ranked by cosine similarity to the query."),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("Natural language search query"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of results (default 5)"),
			),
		),
		searchHandler(brain),
	)

	s.AddTool(
		mcp.NewTool("list_thoughts",
			mcp.WithDescription("List recent thoughts from the thought store, ordered by most recent first."),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of results (default 20)"),
			),
			mcp.WithNumber("offset",
				mcp.Description("Offset for pagination (default 0)"),
			),
		),
		listHandler(brain),
	)

	s.AddTool(
		mcp.NewTool("thought_stats",
			mcp.WithDescription("Get statistics about the thought store: total count, date range, and breakdown by source."),
		),
		statsHandler(brain),
	)

	return s
}

func captureHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, err := req.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError("content is required"), nil
		}
		source := req.GetString("source", "conversation")

		thought, err := brain.Capture(ctx, content, source)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("capture failed: %v", err)), nil
		}

		result, _ := json.Marshal(thought)
		return mcp.NewToolResultText(string(result)), nil
	}
}

func searchHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query is required"), nil
		}
		limit := int(req.GetFloat64("limit", 5))

		results, err := brain.Search(ctx, query, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		out, _ := json.Marshal(results)
		return mcp.NewToolResultText(string(out)), nil
	}
}

func listHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		limit := int(req.GetFloat64("limit", 20))
		offset := int(req.GetFloat64("offset", 0))

		thoughts, err := brain.List(ctx, limit, offset)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list failed: %v", err)), nil
		}

		out, _ := json.Marshal(thoughts)
		return mcp.NewToolResultText(string(out)), nil
	}
}

func statsHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stats, err := brain.Stats(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("stats failed: %v", err)), nil
		}

		out, _ := json.Marshal(stats)
		return mcp.NewToolResultText(string(out)), nil
	}
}
```

**Step 3: Add MCP subcommand to main.go**

Add the struct:

```go
type MCPCmd struct {
	Config string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	Remote bool   `long:"remote" description:"Use HTTP client to connect to a remote thought-store server"`
}
```

Add to opts:

```go
var opts struct {
	Serve   ServeCmd   `command:"serve" description:"Start the HTTP server"`
	Capture CaptureCmd `command:"capture" description:"Capture a thought"`
	Search  SearchCmd  `command:"search" description:"Search thoughts"`
	MCP     MCPCmd     `command:"mcp" description:"Start MCP server (stdio) for Claude Code"`
}
```

Add Execute method:

```go
func (cmd *MCPCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	var brain ThoughtStore

	if cmd.Remote {
		if len(cfg.MCP.URLs) == 0 {
			return fmt.Errorf("no [mcp] urls configured in %s", cmd.Config)
		}
		brain = NewRemoteBrain(cfg.MCP.URLs, 30*time.Second)
	} else {
		ctx := context.Background()
		store, err := NewStore(ctx, cfg.Database.URL)
		if err != nil {
			return fmt.Errorf("connect to database: %w", err)
		}
		defer store.Close()

		if err := store.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		ollama := NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)
		brain = NewLocalBrain(store, ollama)
	}

	srv := newMCPServer(brain)
	return server.ServeStdio(srv)
}
```

Add `"time"` to main.go imports, and the mcp-go server import:

```go
import (
	// ... existing imports ...
	mcpserver "github.com/mark3labs/mcp-go/server"
)
```

Note: since both `net/http` and `mcp-go/server` export `server`, use an alias for the MCP one. The Execute method should reference `mcpserver.ServeStdio(srv)`.

**Step 4: Update thought-store.toml with MCP config**

Add to the bottom of `thought-store.toml`:

```toml
[mcp]
urls = [
    "http://localhost:3000",
    "http://100.122.102.93:3000",
]
```

**Step 5: Fetch deps and build**

Run: `go mod tidy && ./build.sh build`
Expected: Build succeeds

**Step 6: Run tests**

Run: `./build.sh test`
Expected: All tests pass

**Step 7: Commit**

```bash
git add mcp.go main.go config.go thought-store.toml go.mod go.sum
git commit -m "Add MCP server subcommand with local and remote brain modes"
```

---

### Task 5: Register MCP server with Claude Code

**Files:**
- Modify: `.claude/settings.local.json` (or use `claude mcp add`)

**Step 1: Register the MCP server**

Run: `claude mcp add thought-store -- ./thought-store mcp --config thought-store.toml`

This tells Claude Code to start the thought-store MCP server as a stdio subprocess.

**Step 2: Add MCP tool permissions to settings**

Add these to the `allow` list in `.claude/settings.local.json`:

```json
"mcp__thought-store__capture_thought",
"mcp__thought-store__search_thoughts",
"mcp__thought-store__list_thoughts",
"mcp__thought-store__thought_stats"
```

**Step 3: Verify in Claude Code**

Start a new Claude Code session and verify the tools are available. Test with:
- "Search my thought store for Go philosophy"
- "Capture this thought: testing the MCP integration"
- "Show me thought store stats"

**Step 4: Commit**

```bash
git add .claude/
git commit -m "Register thought-store MCP server with Claude Code"
```

---

### Task 6: Update documentation

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

**Step 1: Update README.md**

Add MCP section after the API usage section:

```markdown
## MCP Integration (Claude Code)

The thought store includes an MCP server for native Claude Code integration.

### Local mode (same machine as Postgres/Ollama)

```bash
# Register with Claude Code
claude mcp add thought-store -- ./thought-store mcp --config thought-store.toml
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

```bash
# Register with --remote flag
claude mcp add thought-store -- ./thought-store mcp --config thought-store.toml --remote
```

The remote client tries each URL in order. Put localhost first for zero-latency when on the same machine, Tailscale IP last as the universal fallback.
```

**Step 2: Update CLAUDE.md**

Add to Architecture section:

```
brain.go         ThoughtStore interface, LocalBrain (direct Postgres+Ollama)
remote.go        RemoteBrain (HTTP client with URL fallback chain)
mcp.go           MCP server setup (4 tools: capture, search, list, stats)
```

Add to Key patterns:

```
- **ThoughtStore interface**: Both HTTP server and MCP server consume the same interface. LocalBrain talks directly to Postgres+Ollama; RemoteBrain talks HTTP with a URL fallback chain.
- **MCP subcommand**: `./thought-store mcp` speaks stdio MCP for Claude Code. `--remote` flag switches from LocalBrain to RemoteBrain.
```

**Step 3: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "Document MCP server setup for local and remote modes"
```

---

## Summary

| Task | What | Key files |
|------|------|-----------|
| 1 | Stats API endpoint | store.go, server.go |
| 2 | Extract ThoughtStore interface + LocalBrain | brain.go, server.go, main.go |
| 3 | RemoteBrain with URL fallback chain | remote.go, remote_test.go |
| 4 | MCP server subcommand | mcp.go, main.go, config.go |
| 5 | Register with Claude Code | .claude/settings.local.json |
| 6 | Update docs | README.md, CLAUDE.md |
