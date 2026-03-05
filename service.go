package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// ServiceManager abstracts platform-specific service management.
type ServiceManager interface {
	Install(opts ServiceOpts) error
	Uninstall() error
	Enable() error
	Disable() error
	Status() (*ServiceStatus, error)
	Logs(follow bool, lines int) error
}

// ServiceOpts captures install-time configuration.
type ServiceOpts struct {
	BinaryPath string // absolute path to the binary to run
	ConfigPath string // absolute path to the config file
	CopyBinary bool   // copy binary to install dir
	InstallDir string // where to copy binary (default: /usr/local/bin)
	CopyConfig bool   // copy config to standard location
	System     bool   // system-level vs user-level service
	Force      bool   // overwrite existing service file
}

// ServiceStatus reports the current state of the service.
type ServiceStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Enabled   bool   `json:"enabled"`
	PID       int    `json:"pid,omitempty"`
	Uptime    string `json:"uptime,omitempty"`
}

// serviceName is the name used for the service across all platforms.
const serviceName = "thought-store"

// ResolvePaths absolutizes binary and config paths relative to the current directory.
func ResolvePaths(binaryPath, configPath string) (string, string, error) {
	absBinary, err := filepath.Abs(binaryPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve binary path: %w", err)
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve config path: %w", err)
	}

	// Verify files exist
	if _, err := os.Stat(absBinary); err != nil {
		return "", "", fmt.Errorf("binary not found: %w", err)
	}
	if _, err := os.Stat(absConfig); err != nil {
		return "", "", fmt.Errorf("config not found: %w", err)
	}

	return absBinary, absConfig, nil
}
