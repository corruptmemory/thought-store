# Diagnostics Report Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `Diagnostics(ctx) (*DiagnosticsReport, error)` method to the `ThoughtStore` interface so that local and remote brains can report their health, versions, and configuration for debugging.

**Architecture:** New `diagnostics.go` file defines the report types (with a `BrainMode` fake enum). `LocalBrain` queries Postgres (version, pgvector, pool stats, HNSW index, thought count) and Ollama (version, model details, embedding dimensions). `RemoteBrain` fetches `GET /api/diagnostics` from the server and annotates with network type. The report is exposed as an MCP tool (`thought_diagnostics`) and HTTP endpoint (`GET /api/diagnostics`).

**Tech Stack:** Go 1.26, pgx pool stats, Ollama `/api/version` + `/api/show` endpoints, `net` package for IP classification.

**Build rule:** Always use `./build.sh` — never invoke `go build`, `go test`, `templ generate` directly.

---

### Task 1: Define diagnostic types in `diagnostics.go`

**Files:**
- Create: `diagnostics.go`

**Step 1: Create diagnostics.go with all types**

```go
package main

import (
	"fmt"
	"net"
	"net/url"
	"time"
)

// BrainMode indicates whether the brain connects locally or remotely.
type BrainMode int

const (
	BrainModeLocal  BrainMode = iota
	BrainModeRemote
)

func (m BrainMode) String() string {
	switch m {
	case BrainModeLocal:
		return "local"
	case BrainModeRemote:
		return "remote"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

func (m BrainMode) MarshalText() ([]byte, error) {
	return []byte(m.String()), nil
}

// DiagnosticsReport is the top-level diagnostics structure.
type DiagnosticsReport struct {
	Mode      BrainMode     `json:"mode"`
	StartedAt time.Time     `json:"started_at"`
	Uptime    string        `json:"uptime"`
	GoVersion string        `json:"go_version"`
	Postgres  *PostgresDiag `json:"postgres,omitempty"`
	Ollama    *OllamaDiag   `json:"ollama,omitempty"`
	Remote    *RemoteDiag   `json:"remote,omitempty"`
}

type PostgresDiag struct {
	Version         string    `json:"version"`
	PgvectorVersion string    `json:"pgvector_version"`
	Host            string    `json:"host"`
	Port            uint16    `json:"port"`
	Database        string    `json:"database"`
	Status          string    `json:"status"`
	SchemaVersion   int       `json:"schema_version"`
	ThoughtCount    int       `json:"thought_count"`
	HNSWIndex       bool      `json:"hnsw_index"`
	PoolTotal       int32     `json:"pool_total"`
	PoolIdle        int32     `json:"pool_idle"`
	PoolInUse       int32     `json:"pool_in_use"`
}

type OllamaDiag struct {
	URL             string `json:"url"`
	Version         string `json:"version"`
	Status          string `json:"status"`
	Model           string `json:"model"`
	Family          string `json:"family,omitempty"`
	ParameterSize   string `json:"parameter_size,omitempty"`
	Quantization    string `json:"quantization,omitempty"`
	EmbeddingDim    int    `json:"embedding_dim,omitempty"`
}

type RemoteDiag struct {
	URLs        []string           `json:"urls"`
	ActiveURL   string             `json:"active_url"`
	NetworkType string             `json:"network_type"`
	Latency     string             `json:"latency"`
	ServerDiag  *DiagnosticsReport `json:"server_diagnostics,omitempty"`
}

// NetworkType classifies a URL's host as loopback, lan, tailscale, or public.
func NetworkType(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "unknown"
	}
	host := u.Hostname()

	ip := net.ParseIP(host)
	if ip == nil {
		// hostname like "localhost"
		if host == "localhost" {
			return "loopback"
		}
		return "dns"
	}

	if ip.IsLoopback() {
		return "loopback"
	}

	// Tailscale CGNAT range: 100.64.0.0/10
	_, tailscaleNet, _ := net.ParseCIDR("100.64.0.0/10")
	if tailscaleNet.Contains(ip) {
		return "tailscale"
	}

	if ip.IsPrivate() {
		return "lan"
	}

	return "public"
}
```

**Step 2: Build**

Run: `./build.sh build`
Expected: Build succeeds (types are defined but not yet used)

**Step 3: Commit**

```bash
git add diagnostics.go
git commit -m "Add diagnostics report types with BrainMode enum and network classifier"
```

---

### Task 2: Add Postgres diagnostic queries to `store.go`

**Files:**
- Modify: `store.go`

**Step 1: Add the Diagnostics method to Store**

Add after the `Stats` method:

```go
func (s *Store) Diagnostics(ctx context.Context) *PostgresDiag {
	diag := &PostgresDiag{
		SchemaVersion: 1,
		Status:        "connected",
	}

	// Connection info from pool config
	cfg := s.pool.Config().ConnConfig
	diag.Host = cfg.Host
	diag.Port = cfg.Port
	diag.Database = cfg.Database

	// Pool stats
	stat := s.pool.Stat()
	diag.PoolTotal = stat.TotalConns()
	diag.PoolIdle = stat.IdleConns()
	diag.PoolInUse = stat.AcquiredConns()

	// Postgres version
	_ = s.pool.QueryRow(ctx, `SELECT version()`).Scan(&diag.Version)

	// pgvector version
	_ = s.pool.QueryRow(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`,
	).Scan(&diag.PgvectorVersion)

	// Thought count
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM thoughts`).Scan(&diag.ThoughtCount)

	// HNSW index
	var indexName string
	err := s.pool.QueryRow(ctx,
		`SELECT indexname FROM pg_indexes WHERE tablename = 'thoughts' AND indexname = 'thoughts_embedding_idx'`,
	).Scan(&indexName)
	diag.HNSWIndex = err == nil

	return diag
}
```

Note: This method returns `*PostgresDiag` directly (not error) because it's best-effort — individual queries failing just leave fields at zero values. The `Status` field is "connected" if we get this far (the pool already validated the connection).

**Step 2: Build**

Run: `./build.sh build`
Expected: Build succeeds

**Step 3: Commit**

```bash
git add store.go
git commit -m "Add Postgres diagnostics: version, pgvector, pool stats, HNSW index check"
```

---

### Task 3: Add Ollama diagnostic queries to `ollama.go`

**Files:**
- Modify: `ollama.go`

**Step 1: Add version and model info methods**

Add after the `Embed` method:

```go
// Version fetches the Ollama server version.
func (c *OllamaClient) Version(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var v struct{ Version string }
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// ModelInfo holds details from Ollama's /api/show endpoint.
type ModelInfo struct {
	Family        string `json:"family"`
	ParameterSize string `json:"parameter_size"`
	Quantization  string `json:"quantization_level"`
	EmbeddingDim  int    `json:"embedding_dim"`
}

// ShowModel fetches model details from Ollama.
func (c *OllamaClient) ShowModel(ctx context.Context) (*ModelInfo, error) {
	body, _ := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: c.model})

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw struct {
		Details struct {
			Family        string `json:"family"`
			ParameterSize string `json:"parameter_size"`
			Quantization  string `json:"quantization_level"`
		} `json:"details"`
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	info := &ModelInfo{
		Family:        raw.Details.Family,
		ParameterSize: raw.Details.ParameterSize,
		Quantization:  raw.Details.Quantization,
	}

	// Extract embedding dimension from model_info
	if dim, ok := raw.ModelInfo["bert.embedding_length"]; ok {
		switch v := dim.(type) {
		case float64:
			info.EmbeddingDim = int(v)
		}
	}

	return info, nil
}

// Diagnostics returns an OllamaDiag with version and model info (best-effort).
func (c *OllamaClient) Diagnostics(ctx context.Context) *OllamaDiag {
	diag := &OllamaDiag{
		URL:    c.baseURL,
		Model:  c.model,
		Status: "ok",
	}

	ver, err := c.Version(ctx)
	if err != nil {
		diag.Status = fmt.Sprintf("error: %v", err)
		return diag
	}
	diag.Version = ver

	info, err := c.ShowModel(ctx)
	if err != nil {
		// Version worked but show didn't — partial success
		return diag
	}
	diag.Family = info.Family
	diag.ParameterSize = info.ParameterSize
	diag.Quantization = info.Quantization
	diag.EmbeddingDim = info.EmbeddingDim

	return diag
}
```

**Step 2: Build**

Run: `./build.sh build`
Expected: Build succeeds

**Step 3: Commit**

```bash
git add ollama.go
git commit -m "Add Ollama diagnostics: version, model family, parameters, embedding dimensions"
```

---

### Task 4: Add `Diagnostics` to the `ThoughtStore` interface and implement for `LocalBrain`

**Files:**
- Modify: `brain.go`
- Modify: `main.go` (pass start time)

**Step 1: Add Diagnostics to the interface and LocalBrain**

Add to the `ThoughtStore` interface in `brain.go`:

```go
Diagnostics(ctx context.Context) (*DiagnosticsReport, error)
```

Add `startedAt` field to `LocalBrain`:

```go
type LocalBrain struct {
	store     *Store
	ollama    *OllamaClient
	startedAt time.Time
}

func NewLocalBrain(store *Store, ollama *OllamaClient) *LocalBrain {
	return &LocalBrain{store: store, ollama: ollama, startedAt: time.Now()}
}
```

Add the implementation:

```go
func (b *LocalBrain) Diagnostics(ctx context.Context) (*DiagnosticsReport, error) {
	return &DiagnosticsReport{
		Mode:      BrainModeLocal,
		StartedAt: b.startedAt,
		Uptime:    time.Since(b.startedAt).Truncate(time.Second).String(),
		GoVersion: runtime.Version(),
		Postgres:  b.store.Diagnostics(ctx),
		Ollama:    b.ollama.Diagnostics(ctx),
	}, nil
}
```

Add `"runtime"` and `"time"` to the imports in `brain.go`.

**Step 2: Build**

Run: `./build.sh build`
Expected: FAIL — `RemoteBrain` does not implement `Diagnostics` yet. That's expected; we fix it in Task 5.

**Step 3: Commit**

Do NOT commit yet — the build is broken. Proceed to Task 5.

---

### Task 5: Implement `Diagnostics` for `RemoteBrain` and add HTTP endpoint

**Files:**
- Modify: `remote.go`
- Modify: `server.go`

**Step 1: Add HTTP endpoint to server.go**

Add route in `NewServer`:

```go
s.router.Get("/api/diagnostics", s.handleDiagnostics)
```

Add handler:

```go
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	report, err := s.brain.Diagnostics(r.Context())
	if err != nil {
		http.Error(w, "diagnostics failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}
```

**Step 2: Add startedAt and Diagnostics to RemoteBrain in remote.go**

Add `startedAt` field to `RemoteBrain`:

```go
type RemoteBrain struct {
	urls      []string
	client    *http.Client
	startedAt time.Time
}

func NewRemoteBrain(urls []string, timeout time.Duration) *RemoteBrain {
	return &RemoteBrain{
		urls:      urls,
		client:    &http.Client{Timeout: timeout},
		startedAt: time.Now(),
	}
}
```

Add the `Diagnostics` implementation. This method probes each URL in the fallback chain to find which one responds, measures latency, then fetches the server's own diagnostics:

```go
func (b *RemoteBrain) Diagnostics(ctx context.Context) (*DiagnosticsReport, error) {
	report := &DiagnosticsReport{
		Mode:      BrainModeRemote,
		StartedAt: b.startedAt,
		Uptime:    time.Since(b.startedAt).Truncate(time.Second).String(),
		GoVersion: runtime.Version(),
	}

	remoteDiag := &RemoteDiag{
		URLs: b.urls,
	}

	// Probe: find which URL responds and measure latency
	start := time.Now()
	resp, err := b.doRequest(ctx, "GET", "/api/diagnostics", nil, nil)
	latency := time.Since(start)

	if err != nil {
		remoteDiag.ActiveURL = "none"
		remoteDiag.NetworkType = "unreachable"
		remoteDiag.Latency = "n/a"
		report.Remote = remoteDiag
		return report, nil
	}
	defer resp.Body.Close()

	// Determine which URL responded by trying each one
	// (doRequest already did the fallback, we need to figure out which won)
	for _, u := range b.urls {
		testResp, testErr := b.client.Get(u + "/api/stats")
		if testErr == nil {
			testResp.Body.Close()
			remoteDiag.ActiveURL = u
			remoteDiag.NetworkType = NetworkType(u)
			break
		}
	}

	remoteDiag.Latency = latency.Truncate(time.Millisecond).String()

	// Decode the server's diagnostics report
	var serverDiag DiagnosticsReport
	if err := json.NewDecoder(resp.Body).Decode(&serverDiag); err == nil {
		remoteDiag.ServerDiag = &serverDiag
	}

	report.Remote = remoteDiag
	return report, nil
}
```

Add `"runtime"` to the imports in `remote.go`.

**Step 3: Build**

Run: `./build.sh build`
Expected: Build succeeds — both implementations satisfy the interface.

**Step 4: Commit**

```bash
git add brain.go remote.go server.go
git commit -m "Add Diagnostics to ThoughtStore interface with local and remote implementations"
```

---

### Task 6: Add `thought_diagnostics` MCP tool

**Files:**
- Modify: `mcp.go`
- Modify: `.claude/settings.local.json`

**Step 1: Add the tool to mcp.go**

In `newMCPServer`, add after the `thought_stats` tool:

```go
srv.AddTool(
	mcp.NewTool("thought_diagnostics",
		mcp.WithDescription("Get diagnostic information about the thought store: database status, Ollama status, connection mode, versions, and uptime."),
	),
	diagnosticsHandler(brain),
)
```

Add the handler function:

```go
func diagnosticsHandler(brain ThoughtStore) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := brain.Diagnostics(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("diagnostics failed: %v", err)), nil
		}

		data, _ := json.Marshal(report)
		return mcp.NewToolResultText(string(data)), nil
	}
}
```

**Step 2: Add tool permission to .claude/settings.local.json**

Add to the `allow` list:

```json
"mcp__thought-store__thought_diagnostics"
```

**Step 3: Build**

Run: `./build.sh build`
Expected: Build succeeds

**Step 4: Run tests**

Run: `./build.sh test`
Expected: All existing tests pass (diagnostics is additive, no behavior changed)

**Step 5: Commit**

```bash
git add mcp.go .claude/settings.local.json
git commit -m "Add thought_diagnostics MCP tool and HTTP endpoint"
```

---

### Task 7: Add diagnostics test

**Files:**
- Modify: `e2e_test.go`

**Step 1: Add diagnostics test to e2e_test.go**

Add after the existing stats assertions in `TestE2E`:

```go
// Diagnostics
resp, err = client.Get(base + "/api/diagnostics")
if err != nil {
	t.Fatalf("diagnostics: %v", err)
}
defer resp.Body.Close()

var diag DiagnosticsReport
json.NewDecoder(resp.Body).Decode(&diag)
if diag.Mode != BrainModeLocal {
	t.Fatalf("expected local mode, got %s", diag.Mode)
}
if diag.Postgres == nil {
	t.Fatal("expected postgres diagnostics")
}
if diag.Postgres.ThoughtCount == 0 {
	t.Fatal("expected non-zero thought count in diagnostics")
}
if !diag.Postgres.HNSWIndex {
	t.Fatal("expected HNSW index to exist")
}
if diag.Ollama == nil {
	t.Fatal("expected ollama diagnostics")
}
if diag.Ollama.EmbeddingDim != 1024 {
	t.Fatalf("expected embedding dim 1024, got %d", diag.Ollama.EmbeddingDim)
}
if diag.GoVersion == "" {
	t.Fatal("expected non-empty Go version")
}
```

**Step 2: Run tests**

Run: `./build.sh test`
Expected: All tests pass

**Step 3: Commit**

```bash
git add e2e_test.go
git commit -m "Add diagnostics endpoint test: mode, postgres, ollama, embedding dims"
```

---

### Task 8: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

**Step 1: Update CLAUDE.md**

Add to the Key patterns section:

```
- **Diagnostics**: `Diagnostics(ctx)` on the interface returns a structured report. LocalBrain queries Postgres and Ollama directly; RemoteBrain fetches `/api/diagnostics` and nests the server's own report. `BrainMode` is a fake enum with `String()` and `MarshalText()`.
```

Add to the Tests section:

```
- `e2e_test.go` — now also tests `/api/diagnostics` endpoint (mode, postgres status, HNSW index, embedding dimensions)
```

**Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "Document diagnostics pattern in CLAUDE.md"
```

---

## Summary

| Task | What | Key files |
|------|------|-----------|
| 1 | Diagnostic types + BrainMode enum + NetworkType | diagnostics.go |
| 2 | Postgres diagnostic queries | store.go |
| 3 | Ollama diagnostic queries | ollama.go |
| 4 | Add to ThoughtStore interface + LocalBrain impl | brain.go |
| 5 | RemoteBrain impl + HTTP endpoint | remote.go, server.go |
| 6 | MCP tool + permissions | mcp.go, settings.local.json |
| 7 | E2E test | e2e_test.go |
| 8 | Documentation | CLAUDE.md |
