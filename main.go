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

var opts struct {
	Serve   ServeCmd   `command:"serve" description:"Start the HTTP server"`
	Capture CaptureCmd `command:"capture" description:"Capture a thought"`
	Search  SearchCmd  `command:"search" description:"Search thoughts"`
	MCP     MCPCmd     `command:"mcp" description:"Start MCP server (stdio) for Claude Code"`
	ReEmbed ReEmbedCmd `command:"re-embed" description:"Re-vectorize all thoughts with current model"`
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

	store, err := NewStore(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	ollama := NewOllamaClient(cfg.Ollama.URL, cfg.Ollama.Model)
	brain := NewLocalBrain(store, ollama)
	srv := NewServer(brain)

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
		var urls []string

		// mDNS discovery
		if cfg.MDNS.Name != "" {
			fmt.Fprintf(os.Stderr, "Discovering %q via mDNS...\n", cfg.MDNS.Name)
			discovered, err := DiscoverByName(cfg.MDNS.Name, cfg.MDNS.ServiceType(), 2*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "mDNS discovery failed: %v\n", err)
			} else if len(discovered) > 0 {
				fmt.Fprintf(os.Stderr, "Discovered: %v\n", discovered)
				urls = append(urls, discovered...)
			}
		}

		// Configured URLs as fallback
		urls = append(urls, cfg.MCP.URLs...)

		if len(urls) == 0 {
			return fmt.Errorf("no URLs: configure mcp.urls or mdns.name in %s", cmd.Config)
		}

		brain = NewRemoteBrain(urls, 30*time.Second)
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
	return mcpserver.ServeStdio(srv)
}

func (cmd *ReEmbedCmd) Execute(args []string) error {
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
		if err := store.UpdateEmbeddings(ctx, ids, embeddings); err != nil {
			return fmt.Errorf("update batch %d-%d: %w", i+1, end, err)
		}

		fmt.Fprintf(os.Stderr, "  re-embedded %d/%d\n", end, total)
	}

	fmt.Fprintln(os.Stderr, "Done.")
	return nil
}
