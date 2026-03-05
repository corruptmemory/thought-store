package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	"postgresql": {
		"pacman": "postgresql",
		"paru":   "postgresql",
		"yay":    "postgresql",
		"brew":   "postgresql@17",
	},
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

	if err := cmd.setupPostgres(cfg, pm); err != nil {
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

func (cmd *SetupCmd) setupPostgres(cfg *Config, pm *packageManager) error {
	printHeader("PostgreSQL")

	choice, err := selectPrompt("Where is your PostgreSQL server?", []string{"Local (this machine)", "Remote (external server)"})
	if err != nil {
		return err
	}

	if choice == "Remote (external server)" {
		return setupRemotePostgres(cfg)
	}
	return setupLocalPostgres(cfg, pm)
}

func setupRemotePostgres(cfg *Config) error {
	fmt.Fprintln(os.Stderr, "  Example: postgres://user:password@host:5432/dbname?sslmode=require")
	dbURL, err := inputPrompt("Database URL", "")
	if err != nil {
		return err
	}
	if dbURL == "" {
		return fmt.Errorf("database URL is required")
	}

	if err := checkPsqlConnection(dbURL); err != nil {
		return err
	}
	printSuccess("Connected to remote PostgreSQL")

	if err := checkPgvectorInstalled(dbURL); err != nil {
		return err
	}
	printSuccess("pgvector extension is available")

	cfg.Database.URL = dbURL
	return nil
}

func setupLocalPostgres(cfg *Config, pm *packageManager) error {
	if _, err := exec.LookPath("psql"); err != nil {
		printWarn("psql not found in PATH")
		if pm == nil {
			return fmt.Errorf("psql is required but no package manager found to install it")
		}
		install, err := confirmPrompt("Install PostgreSQL?", true)
		if err != nil {
			return err
		}
		if !install {
			return fmt.Errorf("PostgreSQL is required")
		}
		if err := runPackageInstall(pm, "postgresql"); err != nil {
			return fmt.Errorf("install postgresql: %w", err)
		}
		printSuccess("PostgreSQL installed")
	} else {
		printSuccess("psql found")
	}

	if !isPostgresRunning() {
		printWarn("PostgreSQL server does not appear to be running.")
		fmt.Fprintln(os.Stderr, "  Please start it (e.g. systemctl start postgresql) and press Enter.")
		ready, err := confirmPrompt("Is PostgreSQL running now?", false)
		if err != nil {
			return err
		}
		if !ready || !isPostgresRunning() {
			return fmt.Errorf("PostgreSQL server is not running")
		}
		printSuccess("PostgreSQL server is running")
	} else {
		printSuccess("PostgreSQL server is running")
	}

	appUser, err := inputPrompt("Application database user", "thoughtstore")
	if err != nil {
		return err
	}

	needsPassword, err := confirmPrompt("Does this user require a password?", false)
	if err != nil {
		return err
	}

	var appPassword string
	if needsPassword {
		appPassword, err = inputPrompt("Password", "thoughtstore")
		if err != nil {
			return err
		}
	}

	appDB, err := inputPrompt("Database name", "thoughtstore")
	if err != nil {
		return err
	}

	adminUser, err := inputPrompt("Admin user for provisioning", "postgres")
	if err != nil {
		return err
	}

	// Provision user
	if !userExists(adminUser, appUser) {
		if err := createDBUser(adminUser, appUser, appPassword); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		printSuccess(fmt.Sprintf("Created user %s", appUser))
	} else {
		printSuccess(fmt.Sprintf("User %s already exists", appUser))
	}

	// Provision database
	if !dbExists(adminUser, appDB) {
		if err := createDatabase(adminUser, appDB, appUser); err != nil {
			return fmt.Errorf("create database: %w", err)
		}
		printSuccess(fmt.Sprintf("Created database %s", appDB))
	} else {
		printSuccess(fmt.Sprintf("Database %s already exists", appDB))
	}

	// Enable pgvector
	if err := enablePgvector(adminUser, appDB); err != nil {
		return fmt.Errorf("enable pgvector: %w", err)
	}
	printSuccess("pgvector extension enabled")

	// Build connection URL
	if needsPassword {
		cfg.Database.URL = fmt.Sprintf("postgres://%s:%s@localhost/%s?sslmode=disable", appUser, appPassword, appDB)
	} else {
		cfg.Database.URL = fmt.Sprintf("postgres://%s@localhost/%s?sslmode=disable", appUser, appDB)
	}

	return nil
}

func isPostgresRunning() bool {
	cmd := exec.Command("psql", "-U", "postgres", "-c", "SELECT 1")
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=3")
	return cmd.Run() == nil
}

func checkPsqlConnection(dbURL string) error {
	cmd := exec.Command("psql", dbURL, "-c", "SELECT 1")
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=5")
	out, err := cmd.CombinedOutput()
	if err != nil {
		printError(fmt.Sprintf("Connection failed: %s", strings.TrimSpace(string(out))))
		return fmt.Errorf("cannot connect to database: %w", err)
	}
	return nil
}

func checkPgvectorInstalled(dbURL string) error {
	cmd := exec.Command("psql", dbURL, "-tAc", "SELECT 1 FROM pg_available_extensions WHERE name = 'vector'")
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=5")
	out, err := cmd.CombinedOutput()
	if err != nil {
		printError(fmt.Sprintf("pgvector check failed: %s", strings.TrimSpace(string(out))))
		return fmt.Errorf("pgvector check failed: %w", err)
	}
	if strings.TrimSpace(string(out)) != "1" {
		return fmt.Errorf("pgvector extension is not available — install it on the server first")
	}
	return nil
}

func userExists(adminUser, user string) bool {
	cmd := exec.Command("psql", "-U", adminUser, "-tAc", fmt.Sprintf("SELECT 1 FROM pg_roles WHERE rolname = '%s'", user))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

func dbExists(adminUser, dbName string) bool {
	cmd := exec.Command("psql", "-U", adminUser, "-tAc", fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s'", dbName))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

func createDBUser(adminUser, user, password string) error {
	var sql string
	if password != "" {
		sql = fmt.Sprintf("CREATE USER %s WITH PASSWORD '%s'", user, password)
	} else {
		sql = fmt.Sprintf("CREATE USER %s", user)
	}
	cmd := exec.Command("psql", "-U", adminUser, "-c", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		printError(fmt.Sprintf("Create user failed: %s", strings.TrimSpace(string(out))))
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func createDatabase(adminUser, dbName, owner string) error {
	sql := fmt.Sprintf("CREATE DATABASE %s OWNER %s", dbName, owner)
	cmd := exec.Command("psql", "-U", adminUser, "-c", sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		printError(fmt.Sprintf("Create database failed: %s", strings.TrimSpace(string(out))))
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

func enablePgvector(adminUser, dbName string) error {
	cmd := exec.Command("psql", "-U", adminUser, "-d", dbName, "-c", "CREATE EXTENSION IF NOT EXISTS vector")
	out, err := cmd.CombinedOutput()
	if err != nil {
		printError(fmt.Sprintf("Enable pgvector failed: %s", strings.TrimSpace(string(out))))
		return fmt.Errorf("enable pgvector: %w", err)
	}
	return nil
}

func (cmd *SetupCmd) setupOllama(cfg *Config, pm *packageManager) error {
	printHeader("Ollama")

	// NOTE: The embedding model is locked to mxbai-embed-large because the
	// schema hardcodes vector(1024) in store.go:Migrate(). Changing the model
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
	fmt.Fprintf(os.Stderr, "  Database: %s\n", cfg.Database.URL)
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
