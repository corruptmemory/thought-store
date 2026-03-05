package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
	return nil
}

func (cmd *SetupCmd) setupOllama(cfg *Config, pm *packageManager) error {
	return nil
}

func (cmd *SetupCmd) setupServer(cfg *Config) error {
	return nil
}

func (cmd *SetupCmd) setupMDNS(cfg *Config) error {
	return nil
}

func (cmd *SetupCmd) writeConfig(cfg *Config) error {
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
