# Thought Store v1 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a personal thought capture and semantic search system with a chat-like web UI.

**Architecture:** Single Go binary using chi router, templ templates, and htmx. Synchronous pipeline: receive text, embed via local Ollama, store in Postgres with pgvector, return rendered HTML fragment. CLI subcommands for headless capture/search via the running server's API.

**Tech Stack:** Go 1.26, chi v5, templ, htmx 2.0.8, go-flags, BurntSushi/toml, pgx v5, pgvector-go, Ollama (mxbai-embed-large 1024D)

**Prerequisites already done:**
- Postgres 18.3 running, `thoughtstore` DB created, pgvector extension enabled
- Ollama running with mxbai-embed-large model pulled
- templ, air, Go all installed

**Build rule:** Always use `./build.sh` — never invoke `go build`, `go test`, `templ generate` directly.

**Test approach:** Table-driven tests using `t.TempDir()` for isolation where needed. Integration tests hit real Postgres (test DB) and real Ollama. No mocks for v1 — this is a personal tool, not a library.

---

### Task 1: Project scaffolding and build script

**Files:**
- Create: `go.mod`
- Create: `build.sh`
- Create: `thought-store.toml`
- Create: `.gitignore`
- Create: `.air.toml`

**Step 1: Initialize Go module**

Run: `go mod init github.com/jim/thought-store`

**Step 2: Create build.sh**

```bash
#!/usr/bin/env bash
set -euo pipefail

HTMX_VERSION="2.0.8"

refresh_htmx() {
    mkdir -p static/vendor
    curl -sL "https://unpkg.com/htmx.org@${HTMX_VERSION}/dist/htmx.min.js" \
        -o static/vendor/htmx.min.js
    echo "Downloaded htmx ${HTMX_VERSION}"
}

case "${1:-build}" in
    build)
        templ generate
        go build -o thought-store .
        ;;
    test)
        templ generate
        go test ./... -v
        ;;
    --refresh-htmx)
        refresh_htmx
        ;;
    *)
        echo "Usage: ./build.sh [build|test|--refresh-htmx]"
        exit 1
        ;;
esac
```

Run: `chmod +x build.sh`

**Step 3: Create default config file**

```toml
[server]
addr = ":3000"

[database]
url = "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable"

[ollama]
url = "http://localhost:11434"
model = "mxbai-embed-large"
```

**Step 4: Create .gitignore**

```
thought-store
*_templ.go
tmp/
```

**Step 5: Create .air.toml**

```toml
root = "."
tmp_dir = "tmp"

[build]
cmd = "./build.sh build"
bin = "./thought-store serve --config thought-store.toml"
include_ext = ["go", "templ", "toml", "css", "js"]
exclude_dir = ["tmp", "static/vendor"]

[screen]
clear_on_rebuild = true
```

**Step 6: Download htmx**

Run: `./build.sh --refresh-htmx`

**Step 7: Commit**

```bash
git add go.mod build.sh thought-store.toml .gitignore .air.toml static/vendor/htmx.min.js
git commit -m "Scaffold project with build script, config, and htmx vendor"
```

---

### Task 2: Config and main entrypoint

**Files:**
- Create: `config.go`
- Create: `main.go`

**Step 1: Create config.go**

TOML config struct with sensible defaults. Includes a helper to load from file path.

```go
package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Database DatabaseConfig `toml:"database"`
	Ollama   OllamaConfig   `toml:"ollama"`
}

type ServerConfig struct {
	Addr string `toml:"addr"`
}

type DatabaseConfig struct {
	URL string `toml:"url"`
}

type OllamaConfig struct {
	URL   string `toml:"url"`
	Model string `toml:"model"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}
```

**Step 2: Create main.go**

go-flags with three subcommands: serve, capture, search. Only `serve` prints a startup message for now; capture and search are stubs.

```go
package main

import (
	"fmt"
	"os"

	flags "github.com/jessevdk/go-flags"
)

type ServeCmd struct {
	Config string `long:"config" default:"thought-store.toml" description:"Path to config file"`
}

type CaptureCmd struct {
	Config string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	Args   struct {
		Content string `positional-arg-name:"content" required:"true"`
	} `positional-args:"yes"`
}

type SearchCmd struct {
	Config string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	Limit  int    `long:"limit" default:"5" description:"Max results"`
	Args   struct {
		Query string `positional-arg-name:"query" required:"true"`
	} `positional-args:"yes"`
}

var opts struct {
	Serve   ServeCmd   `command:"serve" description:"Start the HTTP server"`
	Capture CaptureCmd `command:"capture" description:"Capture a thought"`
	Search  SearchCmd  `command:"search" description:"Search thoughts"`
}

func main() {
	parser := flags.NewParser(&opts, flags.Default)
	_, err := parser.Parse()
	if err != nil {
		if flags.WroteHelp(err) {
			os.Exit(0)
		}
		os.Exit(1)
	}
}

func (cmd *ServeCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}
	fmt.Printf("Starting server on %s\n", cfg.Server.Addr)
	return nil
}

func (cmd *CaptureCmd) Execute(args []string) error {
	fmt.Println("capture: not yet implemented")
	return nil
}

func (cmd *SearchCmd) Execute(args []string) error {
	fmt.Println("search: not yet implemented")
	return nil
}
```

**Step 3: Fetch dependencies and verify build**

Run: `go mod tidy && ./build.sh build`
Run: `./thought-store serve --config thought-store.toml`
Expected: `Starting server on :3000`

**Step 4: Commit**

```bash
git add config.go main.go go.mod go.sum
git commit -m "Add config loading and CLI entrypoint with serve/capture/search subcommands"
```

---

### Task 3: Ollama embedding client

**Files:**
- Create: `ollama.go`
- Create: `ollama_test.go`

**Step 1: Write the test**

This is an integration test — it calls real Ollama. Skip if Ollama isn't running.

```go
package main

import (
	"context"
	"net/http"
	"testing"
)

func ollamaAvailable(t *testing.T) {
	t.Helper()
	resp, err := http.Get("http://localhost:11434/api/version")
	if err != nil {
		t.Skip("Ollama not running, skipping integration test")
	}
	resp.Body.Close()
}

func TestOllamaEmbed(t *testing.T) {
	ollamaAvailable(t)

	client := NewOllamaClient("http://localhost:11434", "mxbai-embed-large")

	embedding, err := client.Embed(context.Background(), "test thought about fixing bugs")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(embedding) != 1024 {
		t.Fatalf("expected 1024 dimensions, got %d", len(embedding))
	}

	// Verify values are non-zero (not a blank embedding)
	allZero := true
	for _, v := range embedding {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("embedding is all zeros")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `NewOllamaClient` not defined

**Step 3: Write the implementation**

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type OllamaClient struct {
	baseURL string
	model   string
	http    *http.Client
}

func NewOllamaClient(baseURL, model string) *OllamaClient {
	return &OllamaClient{
		baseURL: baseURL,
		model:   model,
		http:    &http.Client{},
	}
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

func (c *OllamaClient) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Embedding, nil
}
```

**Step 4: Run test to verify it passes**

Run: `./build.sh test`
Expected: PASS

**Step 5: Commit**

```bash
git add ollama.go ollama_test.go
git commit -m "Add Ollama embedding client with integration test"
```

---

### Task 4: Postgres store

**Files:**
- Create: `store.go`
- Create: `store_test.go`

**Step 1: Write the test**

Integration test against real Postgres. Uses a test-specific schema or truncates between tests.

```go
package main

import (
	"context"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, "postgres://thoughtstore:thoughtstore@localhost/thoughtstore?sslmode=disable")
	if err != nil {
		t.Skip("Postgres not available, skipping integration test")
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Create a fake 1024-dim embedding
	embedding := make([]float32, 1024)
	embedding[0] = 0.5
	embedding[1] = -0.3

	thought, err := store.Insert(ctx, "test thought about fixing auth", embedding, "test")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if thought.Content != "test thought about fixing auth" {
		t.Fatalf("content mismatch: %q", thought.Content)
	}
	if thought.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// List
	thoughts, err := store.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(thoughts) == 0 {
		t.Fatal("expected at least one thought")
	}

	// Search with same embedding should find it
	results, err := store.Search(ctx, embedding, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}
	if results[0].Content != "test thought about fixing auth" {
		t.Fatalf("search result mismatch: %q", results[0].Content)
	}

	// Cleanup
	_, err = store.pool.Exec(ctx, "DELETE FROM thoughts WHERE source = 'test'")
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `./build.sh test`
Expected: FAIL — `NewStore` not defined

**Step 3: Write the implementation**

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	pgxvector "github.com/pgvector/pgvector-go/pgx"
)

type Thought struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

type SearchResult struct {
	Thought
	Similarity float64 `json:"similarity"`
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.AfterConnect = pgxvector.RegisterTypes
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS thoughts (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			content     TEXT NOT NULL,
			embedding   vector(1024) NOT NULL,
			source      TEXT NOT NULL DEFAULT 'web',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS thoughts_embedding_idx
		ON thoughts USING hnsw (embedding vector_cosine_ops)`)
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	return nil
}

func (s *Store) Insert(ctx context.Context, content string, embedding []float32, source string) (*Thought, error) {
	var t Thought
	err := s.pool.QueryRow(ctx,
		`INSERT INTO thoughts (content, embedding, source)
		 VALUES ($1, $2, $3)
		 RETURNING id, content, source, created_at`,
		content, pgxvector.NewVector(embedding), source,
	).Scan(&t.ID, &t.Content, &t.Source, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert thought: %w", err)
	}
	return &t, nil
}

func (s *Store) List(ctx context.Context, limit, offset int) ([]Thought, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, content, source, created_at
		 FROM thoughts ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list thoughts: %w", err)
	}
	defer rows.Close()

	var thoughts []Thought
	for rows.Next() {
		var t Thought
		if err := rows.Scan(&t.ID, &t.Content, &t.Source, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan thought: %w", err)
		}
		thoughts = append(thoughts, t)
	}
	return thoughts, rows.Err()
}

func (s *Store) Search(ctx context.Context, embedding []float32, limit int) ([]SearchResult, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, content, source, created_at,
		        1 - (embedding <=> $1) AS similarity
		 FROM thoughts
		 ORDER BY embedding <=> $1
		 LIMIT $2`,
		pgxvector.NewVector(embedding), limit)
	if err != nil {
		return nil, fmt.Errorf("search thoughts: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Content, &r.Source, &r.CreatedAt, &r.Similarity); err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
```

Note: `pgxvector.NewVector` is from `github.com/pgvector/pgvector-go/pgx`. Check the actual API — it may be `pgvector.NewVector` from the parent package with a separate `pgx` registration package. Verify with `go doc` after fetching.

**Step 4: Fetch deps and run test**

Run: `go mod tidy && ./build.sh test`
Expected: PASS

**Step 5: Commit**

```bash
git add store.go store_test.go go.mod go.sum
git commit -m "Add Postgres store with insert, list, search and integration test"
```

---

### Task 5: HTTP server and API handlers

**Files:**
- Create: `server.go`

**Step 1: Write server.go**

Chi router with three API endpoints. Handlers wire the Ollama client and Store together.

```go
package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Server struct {
	router *chi.Mux
	store  *Store
	ollama *OllamaClient
}

func NewServer(store *Store, ollama *OllamaClient) *Server {
	s := &Server{
		router: chi.NewRouter(),
		store:  store,
		ollama: ollama,
	}
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)

	// API routes
	s.router.Post("/api/thoughts", s.handleCapture)
	s.router.Get("/api/thoughts", s.handleList)
	s.router.Get("/api/search", s.handleSearch)

	// Static files
	fs := http.FileServer(http.Dir("static"))
	s.router.Handle("/static/*", http.StripPrefix("/static/", fs))

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

type captureRequest struct {
	Content string `json:"content"`
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var req captureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	embedding, err := s.ollama.Embed(r.Context(), req.Content)
	if err != nil {
		http.Error(w, "embedding failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	source := "web"
	if r.Header.Get("X-Source") != "" {
		source = r.Header.Get("X-Source")
	}

	thought, err := s.store.Insert(r.Context(), req.Content, embedding, source)
	if err != nil {
		http.Error(w, "store failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(thought)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	thoughts, err := s.store.List(r.Context(), limit, offset)
	if err != nil {
		http.Error(w, "list failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(thoughts)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q parameter is required", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 5
	}

	embedding, err := s.ollama.Embed(r.Context(), q)
	if err != nil {
		http.Error(w, "embedding failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	results, err := s.store.Search(r.Context(), embedding, limit)
	if err != nil {
		http.Error(w, "search failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}
```

**Step 2: Wire up the serve command in main.go**

Update `ServeCmd.Execute` to create the store, ollama client, run migrations, and start the HTTP server:

```go
func (cmd *ServeCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

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
	srv := NewServer(store, ollama)

	fmt.Printf("Starting server on %s\n", cfg.Server.Addr)
	return http.ListenAndServe(cfg.Server.Addr, srv)
}
```

Add `"context"` and `"net/http"` to main.go imports.

**Step 3: Build and manually test**

Run: `./build.sh build`
Run: `./thought-store serve --config thought-store.toml &`
Run: `curl -s -X POST http://localhost:3000/api/thoughts -H 'Content-Type: application/json' -d '{"content":"test thought about fixing the login bug"}' | python3 -m json.tool`
Expected: JSON with id, content, source, created_at
Run: `curl -s 'http://localhost:3000/api/search?q=authentication' | python3 -m json.tool`
Expected: JSON array with the thought and a similarity score
Run: kill the background server

**Step 4: Commit**

```bash
git add server.go main.go
git commit -m "Add HTTP server with capture, list, and search API endpoints"
```

---

### Task 6: Templ templates and web UI

**Files:**
- Create: `templates/layout.templ`
- Create: `templates/home.templ`
- Create: `templates/thought.templ`
- Create: `static/tokens.css`
- Create: `static/app.css`

**Step 1: Create tokens.css**

Minimal design tokens for a clean, dark-friendly UI.

```css
:root {
    --font-sans: system-ui, -apple-system, sans-serif;
    --font-mono: ui-monospace, monospace;
    --color-bg: #0f0f0f;
    --color-surface: #1a1a1a;
    --color-surface-hover: #252525;
    --color-border: #333;
    --color-text: #e0e0e0;
    --color-text-muted: #888;
    --color-accent: #6c9eff;
    --color-accent-hover: #8bb4ff;
    --radius: 8px;
    --space-xs: 4px;
    --space-sm: 8px;
    --space-md: 16px;
    --space-lg: 24px;
    --space-xl: 32px;
}
```

**Step 2: Create app.css**

Layout styles for the chat-like UI. Body is a flex column, thought stream scrolls, input bar pinned to bottom.

```css
* { margin: 0; padding: 0; box-sizing: border-box; }

body {
    font-family: var(--font-sans);
    background: var(--color-bg);
    color: var(--color-text);
    height: 100vh;
    display: flex;
    flex-direction: column;
}

.header {
    padding: var(--space-md);
    border-bottom: 1px solid var(--color-border);
    display: flex;
    align-items: center;
    justify-content: space-between;
}

.header h1 {
    font-size: 1.1rem;
    font-weight: 600;
}

.mode-toggle {
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    color: var(--color-text);
    padding: var(--space-xs) var(--space-md);
    border-radius: var(--radius);
    cursor: pointer;
    font-size: 0.85rem;
}

.mode-toggle:hover {
    background: var(--color-surface-hover);
}

.stream {
    flex: 1;
    overflow-y: auto;
    padding: var(--space-md);
    display: flex;
    flex-direction: column;
    gap: var(--space-sm);
}

.thought {
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: var(--radius);
    padding: var(--space-sm) var(--space-md);
}

.thought-content {
    margin-bottom: var(--space-xs);
}

.thought-meta {
    font-size: 0.75rem;
    color: var(--color-text-muted);
}

.thought-similarity {
    color: var(--color-accent);
    font-family: var(--font-mono);
    font-size: 0.75rem;
}

.input-bar {
    padding: var(--space-md);
    border-top: 1px solid var(--color-border);
}

.input-bar form {
    display: flex;
    gap: var(--space-sm);
}

.input-bar input[type="text"] {
    flex: 1;
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    color: var(--color-text);
    padding: var(--space-sm) var(--space-md);
    border-radius: var(--radius);
    font-size: 1rem;
    outline: none;
}

.input-bar input[type="text"]:focus {
    border-color: var(--color-accent);
}

.input-bar button {
    background: var(--color-accent);
    color: #000;
    border: none;
    padding: var(--space-sm) var(--space-lg);
    border-radius: var(--radius);
    cursor: pointer;
    font-weight: 600;
}

.input-bar button:hover {
    background: var(--color-accent-hover);
}

.empty-state {
    color: var(--color-text-muted);
    text-align: center;
    margin-top: var(--space-xl);
}
```

**Step 3: Create templates/layout.templ**

```templ
package templates

templ Layout(title string) {
    <!DOCTYPE html>
    <html lang="en">
    <head>
        <meta charset="UTF-8"/>
        <meta name="viewport" content="width=device-width, initial-scale=1.0"/>
        <title>{ title }</title>
        <link rel="stylesheet" href="/static/tokens.css"/>
        <link rel="stylesheet" href="/static/app.css"/>
        <script src="/static/vendor/htmx.min.js"></script>
    </head>
    <body>
        { children... }
    </body>
    </html>
}
```

**Step 4: Create templates/thought.templ**

Fragment used both for full-page rendering and htmx swaps.

```templ
package templates

import "time"

type ThoughtData struct {
    ID         string
    Content    string
    Source     string
    CreatedAt  time.Time
    Similarity *float64
}

templ ThoughtCard(t ThoughtData) {
    <div class="thought">
        <div class="thought-content">{ t.Content }</div>
        <div class="thought-meta">
            <span>{ t.Source }</span>
            <span>{ t.CreatedAt.Local().Format("Jan 2 3:04pm") }</span>
            if t.Similarity != nil {
                <span class="thought-similarity">
                    { fmt.Sprintf("%.1f%%", *t.Similarity * 100) }
                </span>
            }
        </div>
    </div>
}

templ ThoughtList(thoughts []ThoughtData) {
    for _, t := range thoughts {
        @ThoughtCard(t)
    }
}
```

**Step 5: Create templates/home.templ**

```templ
package templates

templ Home(thoughts []ThoughtData) {
    @Layout("Thought Store") {
        <div class="header">
            <h1>Thought Store</h1>
            <button class="mode-toggle" onclick="toggleMode()">Search</button>
        </div>
        <div class="stream" id="stream">
            if len(thoughts) == 0 {
                <div class="empty-state">No thoughts yet. Start typing below.</div>
            }
            @ThoughtList(thoughts)
        </div>
        <div class="input-bar">
            <form id="capture-form"
                  hx-post="/api/thoughts"
                  hx-target="#stream"
                  hx-swap="beforeend"
                  hx-on::after-request="this.reset(); document.getElementById('stream').scrollTo(0, document.getElementById('stream').scrollHeight)">
                <input type="text" name="content" placeholder="What's on your mind?" autocomplete="off"/>
                <button type="submit">Send</button>
            </form>
        </div>
        <script>
            // Scroll to bottom on load
            document.getElementById('stream').scrollTo(0, document.getElementById('stream').scrollHeight);

            function toggleMode() {
                const btn = document.querySelector('.mode-toggle');
                const form = document.getElementById('capture-form');
                const input = form.querySelector('input');
                const isSearch = btn.textContent === 'Capture';

                if (isSearch) {
                    btn.textContent = 'Search';
                    form.setAttribute('hx-post', '/api/thoughts');
                    form.setAttribute('hx-swap', 'beforeend');
                    input.placeholder = "What's on your mind?";
                    htmx.process(form);
                } else {
                    btn.textContent = 'Capture';
                    form.setAttribute('hx-get', '/api/search');
                    form.setAttribute('hx-swap', 'innerHTML');
                    form.removeAttribute('hx-post');
                    input.name = 'q';
                    input.placeholder = 'Search your thoughts...';
                    htmx.process(form);
                }
            }
        </script>
    }
}
```

Note: The toggle mode JS will need refinement — the htmx attribute swapping between `hx-post` (JSON body) and `hx-get` (query param) requires the handlers to detect htmx requests and return HTML fragments instead of JSON. This will be addressed in Task 7.

**Step 6: Commit**

```bash
git add templates/ static/tokens.css static/app.css
git commit -m "Add templ templates and CSS for chat-like web UI"
```

---

### Task 7: Connect web UI to server (htmx-aware handlers)

**Files:**
- Modify: `server.go` — add HTML routes and make API handlers return HTML for htmx requests

**Step 1: Add content negotiation**

Handlers check for `HX-Request` header. If present, return templ HTML fragments. Otherwise, return JSON (for CLI/API clients).

Add a `GET /` route that renders the home page with recent thoughts.

Update `handleCapture` to return a `ThoughtCard` fragment when called via htmx. The form needs to send JSON — use a small htmx extension or change the form to use `hx-vals` / `hx-include`.

Simpler approach: have the capture form POST with form encoding, and have the handler accept both `application/json` and `application/x-www-form-urlencoded`.

**Step 2: Add to server.go**

- `GET /` → render `Home` template with recent thoughts
- `POST /api/thoughts` with `HX-Request` → return `ThoughtCard` HTML fragment
- `GET /api/search` with `HX-Request` → return `ThoughtList` HTML fragment

Helper function:

```go
func isHTMX(r *http.Request) bool {
    return r.Header.Get("HX-Request") == "true"
}
```

Update `handleCapture` to read form values when content-type is form-urlencoded.

Update `handleSearch` to return HTML fragment for htmx.

**Step 3: Build and test in browser**

Run: `./build.sh build && ./thought-store serve --config thought-store.toml`
Open: `http://localhost:3000`
Expected: Chat-like UI, can type thoughts, they appear in the stream. Search mode toggle works.

**Step 4: Commit**

```bash
git add server.go
git commit -m "Wire web UI to API handlers with htmx content negotiation"
```

---

### Task 8: CLI capture and search subcommands

**Files:**
- Modify: `main.go`

**Step 1: Implement capture subcommand**

POSTs to the running server's API endpoint.

```go
func (cmd *CaptureCmd) Execute(args []string) error {
    cfg, err := LoadConfig(cmd.Config)
    if err != nil {
        return err
    }

    body, _ := json.Marshal(captureRequest{Content: cmd.Args.Content})
    resp, err := http.Post(
        "http://localhost"+cfg.Server.Addr+"/api/thoughts",
        "application/json",
        bytes.NewReader(body),
    )
    if err != nil {
        return fmt.Errorf("server request failed (is the server running?): %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusCreated {
        body, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("server error: %s", body)
    }

    var t Thought
    json.NewDecoder(resp.Body).Decode(&t)
    fmt.Printf("Captured: %s (%s)\n", t.Content, t.ID[:8])
    return nil
}
```

**Step 2: Implement search subcommand**

```go
func (cmd *SearchCmd) Execute(args []string) error {
    cfg, err := LoadConfig(cmd.Config)
    if err != nil {
        return err
    }

    url := fmt.Sprintf("http://localhost%s/api/search?q=%s&limit=%d",
        cfg.Server.Addr, url.QueryEscape(cmd.Args.Query), cmd.Limit)
    resp, err := http.Get(url)
    if err != nil {
        return fmt.Errorf("server request failed (is the server running?): %w", err)
    }
    defer resp.Body.Close()

    var results []SearchResult
    json.NewDecoder(resp.Body).Decode(&results)

    for _, r := range results {
        fmt.Printf("[%.1f%%] %s\n  %s\n\n", r.Similarity*100, r.CreatedAt.Local().Format("Jan 2 3:04pm"), r.Content)
    }
    return nil
}
```

**Step 3: Test manually**

Run server in background, then:
Run: `./thought-store capture "remember to review the PR tomorrow"`
Expected: `Captured: remember to review the PR tomorrow (a1b2c3d4)`
Run: `./thought-store search "code review"`
Expected: Shows the captured thought with a similarity score

**Step 4: Commit**

```bash
git add main.go
git commit -m "Implement CLI capture and search subcommands"
```

---

### Task 9: End-to-end smoke test

**Files:**
- Create: `e2e_test.go`

**Step 1: Write an integration test that exercises the full pipeline**

Starts the server on a random port, captures via API, searches via API, verifies results.

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestE2E(t *testing.T) {
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
	srv := NewServer(store, ollama)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go http.Serve(ln, srv)

	base := fmt.Sprintf("http://%s", ln.Addr().String())
	client := &http.Client{Timeout: 10 * time.Second}

	// Capture
	body, _ := json.Marshal(captureRequest{Content: "e2e test: the authentication system needs refactoring"})
	resp, err := client.Post(base+"/api/thoughts", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("capture status: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Search
	resp, err = client.Get(base + "/api/search?q=auth+refactor")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	defer resp.Body.Close()

	var results []SearchResult
	json.NewDecoder(resp.Body).Decode(&results)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].Similarity < 0.5 {
		t.Fatalf("expected high similarity, got %.2f", results[0].Similarity)
	}

	// Cleanup
	store.pool.Exec(ctx, "DELETE FROM thoughts WHERE source = 'web' AND content LIKE 'e2e test:%'")
}
```

**Step 2: Run it**

Run: `./build.sh test`
Expected: All tests pass

**Step 3: Commit**

```bash
git add e2e_test.go
git commit -m "Add end-to-end smoke test for full capture-search pipeline"
```

---

## Summary

| Task | What | Est. |
|------|------|------|
| 1 | Project scaffolding, build.sh, config, htmx | Setup |
| 2 | Config struct + main.go with go-flags subcommands | Config |
| 3 | Ollama embedding client + integration test | Core |
| 4 | Postgres store (insert/list/search) + integration test | Core |
| 5 | Chi HTTP server with JSON API handlers | Core |
| 6 | Templ templates + CSS for chat UI | UI |
| 7 | Connect UI to server with htmx content negotiation | UI |
| 8 | CLI capture and search subcommands | CLI |
| 9 | End-to-end smoke test | Test |
