package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type SetupCmd struct {
	Config string `long:"config" default:"thought-store.toml" description:"Path to config file"`
}

type packageManager struct {
	name string
	path string
}

var packageNames = map[string]map[string]string{
	"ollama": {
		"pacman": "ollama",
		"paru":   "ollama",
		"yay":    "ollama",
		"brew":   "ollama",
	},
}

func detectPackageManager() *packageManager {
	for _, name := range []string{"paru", "yay", "pacman", "brew"} {
		if path, err := exec.LookPath(name); err == nil {
			return &packageManager{name: name, path: path}
		}
	}
	return nil
}

func (pm *packageManager) installCommand(pkg string) (string, []string) {
	pkgName := pkg
	if names, ok := packageNames[pkg]; ok {
		if mapped, ok := names[pm.name]; ok {
			pkgName = mapped
		}
	}

	switch pm.name {
	case "paru", "yay":
		return pm.path, []string{"-S", "--noconfirm", pkgName}
	case "pacman":
		return "sudo", []string{pm.path, "-S", "--noconfirm", pkgName}
	case "brew":
		return pm.path, []string{"install", pkgName}
	default:
		return pm.path, []string{"install", pkgName}
	}
}

func (cmd *SetupCmd) Execute(args []string) error {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "Windows setup is not yet supported.")
		return nil
	}

	printHeader("Thought Store Setup")

	if err := cmd.checkConfigFile(); err != nil {
		return err
	}

	pm := detectPackageManager()
	if pm != nil {
		printSuccess(fmt.Sprintf("Found package manager: %s (%s)", pm.name, pm.path))
	} else {
		printWarn("No supported package manager found (paru, yay, pacman, brew)")
	}

	cfg := &Config{
		Server: ServerConfig{Addr: ":3000"},
		Ollama: OllamaConfig{
			URL:   "http://localhost:11434",
			Model: "mxbai-embed-large",
		},
	}

	if err := cmd.setupDatabase(cfg); err != nil {
		return err
	}
	if err := cmd.setupOllama(cfg, pm); err != nil {
		return err
	}
	if err := cmd.setupServer(cfg); err != nil {
		return err
	}
	if err := cmd.setupMDNS(cfg); err != nil {
		return err
	}
	if err := cmd.writeConfig(cfg); err != nil {
		return err
	}

	return nil
}

func (cmd *SetupCmd) checkConfigFile() error {
	if _, err := os.Stat(cmd.Config); err == nil {
		printWarn(fmt.Sprintf("Config file %s already exists.", cmd.Config))
		cont, err := confirmPrompt("Continue and overwrite?", false)
		if err != nil {
			return err
		}
		if !cont {
			fmt.Fprintln(os.Stderr, "Setup cancelled.")
			return fmt.Errorf("cancelled")
		}
	}
	return nil
}

func (cmd *SetupCmd) setupDatabase(cfg *Config) error {
	printHeader("Database")
	fmt.Fprintln(os.Stderr, "  Thought Store uses SQLite — no server required.")

	path, err := inputPrompt("Database file path", "thought-store.db")
	if err != nil {
		return err
	}
	cfg.Database.Path = path
	printSuccess(fmt.Sprintf("Database will be stored at %s", path))
	return nil
}

func (cmd *SetupCmd) setupOllama(cfg *Config, pm *packageManager) error {
	printHeader("Ollama")

	// NOTE: The embedding model is locked to mxbai-embed-large because the
	// schema hardcodes vector(1024) in sqlite_store.go:Migrate(). Changing the model
	// requires changing the column dimension and re-embedding all thoughts.
	// See docs/plans/2026-03-05-setup-design.md for the future migration path.
	const model = "mxbai-embed-large"

	ollamaURL := cfg.Ollama.URL

	// 1. Check if Ollama is reachable
	if !isOllamaRunning(ollamaURL) {
		// Check if binary is on PATH
		if _, lookErr := exec.LookPath("ollama"); lookErr != nil {
			printWarn("Ollama is not installed")
			if pm == nil {
				fmt.Fprintln(os.Stderr, "  Install Ollama manually: https://ollama.ai")
				return fmt.Errorf("ollama not installed and no package manager available")
			}
			install, err := confirmPrompt(fmt.Sprintf("Install Ollama via %s?", pm.name), true)
			if err != nil {
				return err
			}
			if !install {
				return fmt.Errorf("ollama is required")
			}
			if err := runPackageInstall(pm, "ollama"); err != nil {
				return fmt.Errorf("install ollama: %w", err)
			}
			printSuccess("Ollama installed")
		} else {
			printSuccess("Ollama binary found")
		}

		// Re-check after potential install
		if !isOllamaRunning(ollamaURL) {
			printWarn("Ollama service is not running")
			fmt.Fprintln(os.Stderr, "  Start it with: systemctl start ollama")
			fmt.Fprintln(os.Stderr, "  Or: ollama serve")
			ready, err := confirmPrompt("Is Ollama running now?", false)
			if err != nil {
				return err
			}
			if !ready || !isOllamaRunning(ollamaURL) {
				return fmt.Errorf("ollama is not running at %s", ollamaURL)
			}
		}
	}
	printSuccess(fmt.Sprintf("Ollama is running at %s", ollamaURL))

	// 2. Check model
	fmt.Fprintf(os.Stderr, "  Checking for model %s...\n", model)
	if isModelAvailable(ollamaURL, model) {
		printSuccess(fmt.Sprintf("Model %s is available", model))
	} else {
		printWarn(fmt.Sprintf("Model %s is not pulled", model))
		pull, err := confirmPrompt(fmt.Sprintf("Pull %s? (may take a few minutes)", model), true)
		if err != nil {
			return err
		}
		if !pull {
			fmt.Fprintf(os.Stderr, "  Run manually: ollama pull %s\n", model)
			return fmt.Errorf("model %s not available", model)
		}
		if err := pullOllamaModel(model); err != nil {
			return fmt.Errorf("pull model: %w", err)
		}
		printSuccess(fmt.Sprintf("Model %s pulled", model))
	}

	return nil
}

func isOllamaRunning(baseURL string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/api/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func isModelAvailable(baseURL, model string) bool {
	body, _ := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: model})
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(baseURL+"/api/show", "application/json", bytes.NewReader(body))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func pullOllamaModel(model string) error {
	cmd := exec.Command("ollama", "pull", model)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (cmd *SetupCmd) setupServer(cfg *Config) error {
	printHeader("Server")
	addr, err := inputPrompt("Listen address", ":3000")
	if err != nil {
		return err
	}
	cfg.Server.Addr = addr
	printSuccess(fmt.Sprintf("Server will listen on %s", addr))
	return nil
}

func (cmd *SetupCmd) setupMDNS(cfg *Config) error {
	printHeader("Network Discovery (mDNS)")
	enable, err := confirmPrompt("Advertise this instance on your local network via mDNS?", false)
	if err != nil {
		return err
	}
	if !enable {
		return nil
	}
	hostname, _ := os.Hostname()
	defaultName := hostname
	if defaultName == "" {
		defaultName = "my-brain"
	}
	name, err := inputPrompt("Instance name", defaultName)
	if err != nil {
		return err
	}
	cfg.MDNS.Name = name
	printSuccess(fmt.Sprintf("Will advertise as %q", name))
	return nil
}

func (cmd *SetupCmd) writeConfig(cfg *Config) error {
	printHeader("Writing Config")
	if err := WriteConfig(cmd.Config, cfg); err != nil {
		return err
	}
	printSuccess(fmt.Sprintf("Wrote %s", cmd.Config))

	fmt.Fprintln(os.Stderr)
	printHeader("Setup Complete")
	fmt.Fprintf(os.Stderr, "  Server:   %s\n", cfg.Server.Addr)
	fmt.Fprintf(os.Stderr, "  Database: %s\n", cfg.Database.Path)
	fmt.Fprintf(os.Stderr, "  Ollama:   %s (model: %s)\n", cfg.Ollama.URL, cfg.Ollama.Model)
	if cfg.MDNS.Name != "" {
		fmt.Fprintf(os.Stderr, "  mDNS:     %s\n", cfg.MDNS.Name)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  Next: ./thought-store serve --config %s\n", cmd.Config)
	fmt.Fprintln(os.Stderr)
	return nil
}

func runPackageInstall(pm *packageManager, pkg string) error {
	bin, args := pm.installCommand(pkg)
	c := exec.Command(bin, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}
