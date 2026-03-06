package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	flags "github.com/jessevdk/go-flags"
	mcpserver "github.com/mark3labs/mcp-go/server"
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

type MCPCmd struct {
	Config string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	Remote bool   `long:"remote" description:"Use HTTP client to connect to a remote thought-store server"`
}

type ReEmbedCmd struct {
	Config    string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	Apply     bool   `long:"apply" description:"Actually update embeddings (default is dry-run)"`
	BatchSize int    `long:"batch-size" default:"50" description:"Number of thoughts per Ollama batch call"`
}

type ScanCmd struct {
	Config  string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	Service string `long:"service" default:"" description:"Service type to scan for (default: _thought-store._tcp)"`
	Timeout int    `long:"timeout" default:"3" description:"Discovery timeout in seconds"`
}

type ServiceCmd struct {
	Install   ServiceInstallCmd   `command:"install" description:"Install as a system service"`
	Uninstall ServiceUninstallCmd `command:"uninstall" description:"Remove the system service"`
	Enable    ServiceEnableCmd    `command:"enable" description:"Enable service to start on boot"`
	Disable   ServiceDisableCmd   `command:"disable" description:"Disable service from starting on boot"`
	Status    ServiceStatusCmd    `command:"status" description:"Show service status"`
	Logs      ServiceLogsCmd      `command:"logs" description:"Show service logs"`
}

type ServiceInstallCmd struct {
	Config     string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	CopyBinary bool   `long:"copy-binary" description:"Copy binary to install directory"`
	InstallDir string `long:"install-dir" default:"/usr/local/bin" description:"Directory to copy binary to"`
	CopyConfig bool   `long:"install-config" description:"Copy config to standard location"`
	System     bool   `long:"system" description:"Install as system service (requires sudo)"`
	Force      bool   `long:"force" description:"Overwrite existing service file"`
}

type ServiceUninstallCmd struct {
	System bool `long:"system" description:"Uninstall system service (requires sudo)"`
}

type ServiceEnableCmd struct {
	System bool `long:"system" description:"Enable system service"`
}

type ServiceDisableCmd struct {
	System bool `long:"system" description:"Disable system service"`
}

type ServiceStatusCmd struct {
	System bool `long:"system" description:"Check system service status"`
}

type ServiceLogsCmd struct {
	Follow bool `long:"follow" short:"f" description:"Follow log output"`
	Lines  int  `long:"lines" short:"n" default:"50" description:"Number of lines to show"`
	System bool `long:"system" description:"Show system service logs"`
}

type SyncCmd struct {
	Pull SyncPullCmd `command:"pull" description:"Pull bundle from a peer and merge locally"`
	Push SyncPushCmd `command:"push" description:"Push local bundle to a peer"`
}

type SyncPullCmd struct {
	Config string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	From   string `long:"from" required:"true" description:"URL of the peer to pull from"`
}

type SyncPushCmd struct {
	Config string `long:"config" default:"thought-store.toml" description:"Path to config file"`
	To     string `long:"to" required:"true" description:"URL of the peer to push to"`
}

var opts struct {
	Serve   ServeCmd   `command:"serve" description:"Start the HTTP server"`
	Capture CaptureCmd `command:"capture" description:"Capture a thought"`
	Search  SearchCmd  `command:"search" description:"Search thoughts"`
	MCP     MCPCmd     `command:"mcp" description:"Start MCP server (stdio) for Claude Code"`
	ReEmbed ReEmbedCmd `command:"re-embed" description:"Re-vectorize all thoughts with current model"`
	Scan    ScanCmd    `command:"scan" description:"Scan for thought-store instances on the local network"`
	Service ServiceCmd `command:"service" description:"Manage thought-store as a system service"`
	Setup   SetupCmd   `command:"setup" description:"Interactive guided setup for new installations"`
	Sync    SyncCmd    `command:"sync" description:"Sync thoughts with a peer"`
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

	ctx := context.Background()

	store, err := NewSQLiteStore(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	hostname, _ := os.Hostname()
	instanceID, err := store.EnsureInstance(ctx, hostname, "")
	if err != nil {
		return fmt.Errorf("ensure instance: %w", err)
	}

	modelID, err := store.EnsureModel(ctx, cfg.Ollama.Model, "", 1024)
	if err != nil {
		return fmt.Errorf("ensure model: %w", err)
	}
	if err := store.SetActiveModel(ctx, modelID); err != nil {
		return fmt.Errorf("set active model: %w", err)
	}

	ollama := NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)

	vecIndex := NewVecIndex()
	ids, vectors, err := store.LoadEmbeddings(ctx, modelID)
	if err != nil {
		return fmt.Errorf("load embeddings: %w", err)
	}
	vecIndex.Load(ids, vectors)
	fmt.Fprintf(os.Stderr, "Loaded %d embeddings into memory\n", vecIndex.Len())

	brain := NewLocalBrain(store, ollama, vecIndex, instanceID, modelID)
	srv := NewServer(brain)

	instanceUID, err := store.InstanceUID(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("instance uid: %w", err)
	}
	srv.SetSyncState(store, cfg.Database.Path, instanceUID, modelID, vecIndex)

	// mDNS advertising
	if cfg.MDNS.Name != "" {
		port, err := ParsePort(cfg.Server.Addr)
		if err != nil {
			return fmt.Errorf("parse listen address for mdns: %w", err)
		}
		mdnsServer, err := AdvertiseService(cfg.MDNS.Name, cfg.MDNS.ServiceType(), port)
		if err != nil {
			return fmt.Errorf("start mdns: %w", err)
		}
		defer mdnsServer.Shutdown()
		brain.SetMDNS(cfg.MDNS.Name, cfg.MDNS.ServiceType())
		fmt.Printf("Advertising as %q via mDNS (%s)\n", cfg.MDNS.Name, cfg.MDNS.ServiceType())
	}

	fmt.Printf("Starting server on %s\n", cfg.Server.Addr)
	return http.ListenAndServe(cfg.Server.Addr, srv)
}

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
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error: %s", b)
	}

	var t Thought
	json.NewDecoder(resp.Body).Decode(&t)
	fmt.Printf("Captured: %s (%s)\n", t.Content, t.ID[:8])
	return nil
}

func (cmd *SearchCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	u := fmt.Sprintf("http://localhost%s/api/search?q=%s&limit=%d",
		cfg.Server.Addr, url.QueryEscape(cmd.Args.Query), cmd.Limit)
	resp, err := http.Get(u)
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

func (cmd *MCPCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	var brain ThoughtStore

	if cmd.Remote {
		var discovered []string

		// mDNS discovery
		if cfg.MDNS.Name != "" {
			fmt.Fprintf(os.Stderr, "Discovering %q via mDNS...\n", cfg.MDNS.Name)
			found, err := DiscoverByName(cfg.MDNS.Name, cfg.MDNS.ServiceType(), 2*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "mDNS discovery failed: %v\n", err)
			} else if len(found) > 0 {
				fmt.Fprintf(os.Stderr, "Discovered: %v\n", found)
				discovered = found
			}
		}

		if len(discovered) == 0 && len(cfg.MCP.URLs) == 0 {
			return fmt.Errorf("no URLs: configure mcp.urls or mdns.name in %s", cmd.Config)
		}

		brain = NewRemoteBrain(cfg.MCP.URLs, discovered, 30*time.Second)
	} else {
		ctx := context.Background()

		store, err := NewSQLiteStore(cfg.Database.Path)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer store.Close()

		if err := store.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		hostname, _ := os.Hostname()
		instanceID, err := store.EnsureInstance(ctx, hostname, "")
		if err != nil {
			return fmt.Errorf("ensure instance: %w", err)
		}

		modelID, err := store.EnsureModel(ctx, cfg.Ollama.Model, "", 1024)
		if err != nil {
			return fmt.Errorf("ensure model: %w", err)
		}
		if err := store.SetActiveModel(ctx, modelID); err != nil {
			return fmt.Errorf("set active model: %w", err)
		}

		ollama := NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)

		vecIndex := NewVecIndex()
		ids, vectors, err := store.LoadEmbeddings(ctx, modelID)
		if err != nil {
			return fmt.Errorf("load embeddings: %w", err)
		}
		vecIndex.Load(ids, vectors)
		fmt.Fprintf(os.Stderr, "Loaded %d embeddings into memory\n", vecIndex.Len())

		brain = NewLocalBrain(store, ollama, vecIndex, instanceID, modelID)
	}

	srv := newMCPServer(brain)
	return mcpserver.ServeStdio(srv)
}

func (cmd *ReEmbedCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	ctx := context.Background()

	store, err := NewSQLiteStore(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	hostname, _ := os.Hostname()
	_, err = store.EnsureInstance(ctx, hostname, "")
	if err != nil {
		return fmt.Errorf("ensure instance: %w", err)
	}

	modelID, err := store.EnsureModel(ctx, cfg.Ollama.Model, "", 1024)
	if err != nil {
		return fmt.Errorf("ensure model: %w", err)
	}

	ollama := NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)

	// Pre-flight: verify Ollama is reachable and get model info
	info, err := ollama.ShowModel(ctx)
	if err != nil {
		return fmt.Errorf("cannot reach Ollama: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Model: %s (%s, %s, %dd embeddings)\n",
		cfg.Ollama.Model, info.Family, info.ParameterSize, info.EmbeddingDim)

	// Load all thoughts
	thoughts, err := store.ListAllContent(ctx)
	if err != nil {
		return fmt.Errorf("load thoughts: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Thoughts to re-embed: %d\n", len(thoughts))

	if len(thoughts) == 0 {
		fmt.Fprintln(os.Stderr, "Nothing to do.")
		return nil
	}

	if !cmd.Apply {
		fmt.Fprintln(os.Stderr, "\nDry run — no changes made. Use --apply to update embeddings.")
		return nil
	}

	// Process in batches
	batchSize := cmd.BatchSize
	if batchSize <= 0 {
		batchSize = 50
	}

	total := len(thoughts)
	for i := 0; i < total; i += batchSize {
		end := min(i+batchSize, total)
		batch := thoughts[i:end]

		// Collect texts for this batch
		texts := make([]string, len(batch))
		ids := make([]string, len(batch))
		for j, t := range batch {
			texts[j] = t.Content
			ids[j] = t.ID
		}

		// Embed batch
		embeddings, err := ollama.EmbedBatch(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed batch %d-%d: %w", i+1, end, err)
		}

		// Update database
		if err := store.UpdateEmbeddings(ctx, ids, embeddings, modelID); err != nil {
			return fmt.Errorf("update batch %d-%d: %w", i+1, end, err)
		}

		fmt.Fprintf(os.Stderr, "  re-embedded %d/%d\n", end, total)
	}

	fmt.Fprintln(os.Stderr, "Done.")
	return nil
}

func (cmd *ScanCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	serviceType := cmd.Service
	if serviceType == "" {
		serviceType = cfg.MDNS.ServiceType()
	}

	timeout := time.Duration(cmd.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	fmt.Fprintf(os.Stderr, "Scanning for %s services (%s)...\n", serviceType, timeout)

	services, err := ScanAll(serviceType, timeout)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	if len(services) == 0 {
		fmt.Fprintln(os.Stderr, "No services found.")
		return nil
	}

	fmt.Fprintf(os.Stderr, "\nFound %d service(s):\n\n", len(services))
	for i, svc := range services {
		fmt.Fprintf(os.Stderr, "  %d) %s (%s:%d)\n", i+1, svc.Name, svc.Addr, svc.Port)
	}

	fmt.Fprintf(os.Stderr, "\nSelect a service (1-%d) or 0 to cancel: ", len(services))

	var choice int
	if _, err := fmt.Scanln(&choice); err != nil {
		return fmt.Errorf("read selection: %w", err)
	}

	if choice == 0 {
		fmt.Fprintln(os.Stderr, "Cancelled.")
		return nil
	}
	if choice < 1 || choice > len(services) {
		return fmt.Errorf("invalid selection: %d", choice)
	}

	selected := services[choice-1]
	fmt.Fprintf(os.Stderr, "\nSelected: %s (%s:%d)\n", selected.Name, selected.Addr, selected.Port)

	// Update config
	cfg.MDNS.Name = selected.Name
	if cmd.Service != "" {
		cfg.MDNS.Service = cmd.Service
	}

	fmt.Fprintf(os.Stderr, "Writing to %s (existing comments will be lost)...\n", cmd.Config)

	if err := WriteConfig(cmd.Config, cfg); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Done. Set [mdns] name = %q\n", selected.Name)
	return nil
}

func (cmd *ServiceInstallCmd) Execute(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	binaryPath, configPath, err := ResolvePaths(exe, cmd.Config)
	if err != nil {
		return err
	}

	mgr := NewServiceManager(serviceName, cmd.System)
	return mgr.Install(ServiceOpts{
		BinaryPath: binaryPath,
		ConfigPath: configPath,
		CopyBinary: cmd.CopyBinary,
		InstallDir: cmd.InstallDir,
		CopyConfig: cmd.CopyConfig,
		System:     cmd.System,
		Force:      cmd.Force,
	})
}

func (cmd *ServiceUninstallCmd) Execute(args []string) error {
	mgr := NewServiceManager(serviceName, cmd.System)
	return mgr.Uninstall()
}

func (cmd *ServiceEnableCmd) Execute(args []string) error {
	mgr := NewServiceManager(serviceName, cmd.System)
	return mgr.Enable()
}

func (cmd *ServiceDisableCmd) Execute(args []string) error {
	mgr := NewServiceManager(serviceName, cmd.System)
	return mgr.Disable()
}

func (cmd *ServiceStatusCmd) Execute(args []string) error {
	mgr := NewServiceManager(serviceName, cmd.System)
	status, err := mgr.Status()
	if err != nil {
		return err
	}

	if !status.Installed {
		fmt.Println("Not installed")
		return nil
	}

	state := "stopped"
	if status.Running {
		state = "running"
	}
	enabled := "disabled"
	if status.Enabled {
		enabled = "enabled"
	}

	fmt.Printf("Status:  %s\n", state)
	fmt.Printf("Enabled: %s\n", enabled)
	if status.PID > 0 {
		fmt.Printf("PID:     %d\n", status.PID)
	}
	if status.Uptime != "" {
		fmt.Printf("Since:   %s\n", status.Uptime)
	}
	return nil
}

func (cmd *ServiceLogsCmd) Execute(args []string) error {
	mgr := NewServiceManager(serviceName, cmd.System)
	return mgr.Logs(cmd.Follow, cmd.Lines)
}

func (cmd *SyncPullCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	ctx := context.Background()

	store, err := NewSQLiteStore(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	hostname, _ := os.Hostname()
	instanceID, err := store.EnsureInstance(ctx, hostname, "")
	if err != nil {
		return fmt.Errorf("ensure instance: %w", err)
	}

	modelID, err := store.EnsureModel(ctx, cfg.Ollama.Model, "", 1024)
	if err != nil {
		return fmt.Errorf("ensure model: %w", err)
	}

	myUID, err := store.InstanceUID(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("instance uid: %w", err)
	}

	peerURL := strings.TrimRight(cmd.From, "/") + "/api/sync"
	fmt.Fprintf(os.Stderr, "Pulling from %s...\n", peerURL)

	resp, err := http.Get(peerURL)
	if err != nil {
		return fmt.Errorf("fetch bundle: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("peer returned %d: %s", resp.StatusCode, body)
	}

	result, err := ImportBundle(ctx, store, myUID, modelID, resp.Body, nil)
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Imported: %d new instances, %d new thoughts, %d skipped, %d queued for re-embed\n",
		result.NewInstances, result.NewThoughts, result.Skipped, result.ReembedQueued)

	// If the local server is running, tell it to reload embeddings
	if result.NewThoughts > 0 {
		reloadURL := "http://localhost" + cfg.Server.Addr + "/api/reload"
		resp, err := http.Post(reloadURL, "", nil)
		if err == nil {
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "Notified local server to reload\n")
		}
	}

	return nil
}

func (cmd *SyncPushCmd) Execute(args []string) error {
	cfg, err := LoadConfig(cmd.Config)
	if err != nil {
		return err
	}

	ctx := context.Background()

	store, err := NewSQLiteStore(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Create bundle in temp file
	tmpFile, err := os.CreateTemp("", "sync-push-*.tar.zst")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	fmt.Fprintf(os.Stderr, "Creating bundle...\n")
	if err := ExportBundle(ctx, store, cfg.Database.Path, tmpFile); err != nil {
		return fmt.Errorf("export: %w", err)
	}
	tmpFile.Close()

	// Upload to peer
	peerURL := strings.TrimRight(cmd.To, "/") + "/api/sync"
	fmt.Fprintf(os.Stderr, "Pushing to %s...\n", peerURL)

	bundleFile, err := os.Open(tmpFile.Name())
	if err != nil {
		return fmt.Errorf("open bundle: %w", err)
	}
	defer bundleFile.Close()

	resp, err := http.Post(peerURL, "application/octet-stream", bundleFile)
	if err != nil {
		return fmt.Errorf("push bundle: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("peer returned %d: %s", resp.StatusCode, body)
	}

	var result ImportResult
	json.NewDecoder(resp.Body).Decode(&result)

	fmt.Fprintf(os.Stderr, "Peer imported: %d new instances, %d new thoughts, %d skipped, %d queued for re-embed\n",
		result.NewInstances, result.NewThoughts, result.Skipped, result.ReembedQueued)
	return nil
}
