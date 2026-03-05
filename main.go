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
