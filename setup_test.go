package main

import (
	"path/filepath"
	"testing"
)

func TestDetectPackageManager(t *testing.T) {
	pm := detectPackageManager()
	// pm == nil is valid (e.g. in CI with no package manager on PATH)
	if pm != nil {
		if pm.name == "" {
			t.Error("package manager name is empty")
		}
		if pm.path == "" {
			t.Error("package manager path is empty")
		}
	}
}

func TestInstallCommand(t *testing.T) {
	tests := []struct {
		name     string
		pm       packageManager
		pkg      string
		wantBin  string
		wantLast string
	}{
		{
			name:     "paru ollama",
			pm:       packageManager{name: "paru", path: "/usr/bin/paru"},
			pkg:      "ollama",
			wantBin:  "/usr/bin/paru",
			wantLast: "ollama",
		},
		{
			name:     "pacman ollama",
			pm:       packageManager{name: "pacman", path: "/usr/bin/pacman"},
			pkg:      "ollama",
			wantBin:  "sudo",
			wantLast: "ollama",
		},
		{
			name:     "brew ollama",
			pm:       packageManager{name: "brew", path: "/opt/homebrew/bin/brew"},
			pkg:      "ollama",
			wantBin:  "/opt/homebrew/bin/brew",
			wantLast: "ollama",
		},
		{
			name:     "yay ollama",
			pm:       packageManager{name: "yay", path: "/usr/bin/yay"},
			pkg:      "ollama",
			wantBin:  "/usr/bin/yay",
			wantLast: "ollama",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, args := tt.pm.installCommand(tt.pkg)
			if bin != tt.wantBin {
				t.Errorf("bin = %q, want %q", bin, tt.wantBin)
			}
			if len(args) == 0 {
				t.Fatal("args is empty")
			}
			last := args[len(args)-1]
			if last != tt.wantLast {
				t.Errorf("last arg = %q, want %q", last, tt.wantLast)
			}
		})
	}
}

func TestCheckConfigFileNonexistent(t *testing.T) {
	cmd := SetupCmd{Config: filepath.Join(t.TempDir(), "nonexistent.toml")}
	if err := cmd.checkConfigFile(); err != nil {
		t.Errorf("checkConfigFile() for nonexistent file returned error: %v", err)
	}
}

func TestIsOllamaRunning(t *testing.T) {
	// Just verify the function returns without panicking.
	// The result depends on whether Ollama is running on the host.
	_ = isOllamaRunning("http://localhost:11434")
}
