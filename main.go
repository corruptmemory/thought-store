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
