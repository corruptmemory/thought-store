# Service Management Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `service` subcommand with platform-agnostic interface and systemd implementation for installing, managing, and monitoring thought-store as a system service.

**Architecture:** A `ServiceManager` interface in `service.go` defines Install/Uninstall/Enable/Disable/Status/Logs. Build-tagged files provide platform implementations: `service_linux.go` (systemd via exec'd systemctl/journalctl) and `service_unsupported.go` (stub). The `service` subcommand uses go-flags nested commands to expose each operation.

**Tech Stack:** Go 1.26, go-flags nested subcommands, `text/template` for unit file generation, `os/exec` for systemctl/journalctl, Go build tags for platform selection.

**Build rule:** Always use `./build.sh` — never invoke `go build`, `go test`, `templ generate` directly.

---

### Task 1: Add ServiceManager interface and types

**Files:**
- Create: `service.go`

**Step 1: Create service.go with interface, types, and NewServiceManager placeholder**

```go
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
```

**Step 2: Build**

Run: `./build.sh build`
Expected: Build succeeds

**Step 3: Commit**

```bash
git add service.go
git commit -m "Add ServiceManager interface and types"
```

---

### Task 2: Add systemd implementation

**Files:**
- Create: `service_linux.go`

**Step 1: Create service_linux.go**

```go
//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

const unitTemplate = `[Unit]
Description=Thought Store
After=network.target postgresql.service

[Service]
Type=simple
ExecStart={{.BinaryPath}} serve --config {{.ConfigPath}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy={{.WantedBy}}
`

type unitData struct {
	BinaryPath string
	ConfigPath string
	WantedBy   string
}

type systemdManager struct {
	name   string
	system bool
}

func NewServiceManager(name string, system bool) ServiceManager {
	return &systemdManager{name: name, system: system}
}

func (m *systemdManager) unitFilePath() string {
	if m.system {
		return filepath.Join("/etc/systemd/system", m.name+".service")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config/systemd/user", m.name+".service")
}

func (m *systemdManager) systemctl(args ...string) *exec.Cmd {
	if !m.system {
		args = append([]string{"--user"}, args...)
	}
	return exec.Command("systemctl", args...)
}

func (m *systemdManager) Install(opts ServiceOpts) error {
	if m.system && os.Geteuid() != 0 {
		return fmt.Errorf("system service requires sudo")
	}

	unitPath := m.unitFilePath()

	// Check if already installed
	if _, err := os.Stat(unitPath); err == nil && !opts.Force {
		return fmt.Errorf("service file already exists at %s (use --force to overwrite)", unitPath)
	}

	binaryPath := opts.BinaryPath
	configPath := opts.ConfigPath

	// Copy binary if requested
	if opts.CopyBinary {
		dest := filepath.Join(opts.InstallDir, serviceName)
		if err := copyFile(binaryPath, dest); err != nil {
			return fmt.Errorf("copy binary to %s: %w", dest, err)
		}
		if err := os.Chmod(dest, 0755); err != nil {
			return fmt.Errorf("chmod binary: %w", err)
		}
		binaryPath = dest
		fmt.Fprintf(os.Stderr, "Copied binary to %s\n", dest)
	}

	// Copy config if requested
	if opts.CopyConfig {
		var configDir string
		if m.system {
			configDir = "/etc/" + serviceName
		} else {
			home, _ := os.UserHomeDir()
			configDir = filepath.Join(home, ".config", serviceName)
		}
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
		dest := filepath.Join(configDir, filepath.Base(configPath))
		if err := copyFile(configPath, dest); err != nil {
			return fmt.Errorf("copy config to %s: %w", dest, err)
		}
		configPath = dest
		fmt.Fprintf(os.Stderr, "Copied config to %s\n", dest)
	}

	// Render unit file
	wantedBy := "default.target"
	if m.system {
		wantedBy = "multi-user.target"
	}

	tmpl, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return fmt.Errorf("parse unit template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, unitData{
		BinaryPath: binaryPath,
		ConfigPath: configPath,
		WantedBy:   wantedBy,
	}); err != nil {
		return fmt.Errorf("render unit file: %w", err)
	}

	// Ensure parent directory exists (for user units)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}

	if err := os.WriteFile(unitPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s\n", unitPath)

	// Reload systemd
	if out, err := m.systemctl("daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("daemon-reload: %s: %w", out, err)
	}

	fmt.Fprintln(os.Stderr, "Service installed. Use 'service enable' to start on boot, 'service start' to run now.")
	return nil
}

func (m *systemdManager) Uninstall() error {
	if m.system && os.Geteuid() != 0 {
		return fmt.Errorf("system service requires sudo")
	}

	// Stop and disable if running
	status, _ := m.Status()
	if status != nil && status.Running {
		fmt.Fprintln(os.Stderr, "Stopping service...")
		m.systemctl("stop", m.name).Run()
	}
	if status != nil && status.Enabled {
		fmt.Fprintln(os.Stderr, "Disabling service...")
		m.systemctl("disable", m.name).Run()
	}

	unitPath := m.unitFilePath()
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	if out, err := m.systemctl("daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("daemon-reload: %s: %w", out, err)
	}

	fmt.Fprintf(os.Stderr, "Service uninstalled (removed %s)\n", unitPath)
	return nil
}

func (m *systemdManager) Enable() error {
	if out, err := m.systemctl("enable", m.name).CombinedOutput(); err != nil {
		return fmt.Errorf("enable: %s: %w", strings.TrimSpace(string(out)), err)
	}
	fmt.Fprintln(os.Stderr, "Service enabled (will start on boot).")
	return nil
}

func (m *systemdManager) Disable() error {
	if out, err := m.systemctl("disable", m.name).CombinedOutput(); err != nil {
		return fmt.Errorf("disable: %s: %w", strings.TrimSpace(string(out)), err)
	}
	fmt.Fprintln(os.Stderr, "Service disabled (will not start on boot).")
	return nil
}

func (m *systemdManager) Status() (*ServiceStatus, error) {
	out, _ := m.systemctl("show", m.name,
		"--property=ActiveState,SubState,MainPID,ActiveEnterTimestamp").CombinedOutput()

	props := parseSystemctlShow(string(out))

	status := &ServiceStatus{}

	// Check if installed by looking for the unit file
	if _, err := os.Stat(m.unitFilePath()); err == nil {
		status.Installed = true
	}

	status.Running = props["ActiveState"] == "active"

	// Check enabled state
	enableOut, _ := m.systemctl("is-enabled", m.name).CombinedOutput()
	status.Enabled = strings.TrimSpace(string(enableOut)) == "enabled"

	if pid, err := strconv.Atoi(props["MainPID"]); err == nil && pid > 0 {
		status.PID = pid
	}

	if ts := props["ActiveEnterTimestamp"]; ts != "" && status.Running {
		status.Uptime = ts
	}

	return status, nil
}

func (m *systemdManager) Logs(follow bool, lines int) error {
	args := []string{
		"--no-pager",
		"-n", strconv.Itoa(lines),
	}

	if m.system {
		args = append(args, "--unit", m.name)
	} else {
		args = append(args, "--user-unit", m.name)
	}

	if follow {
		args = append(args, "-f")
	}

	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// parseSystemctlShow parses "Key=Value\n" output from systemctl show.
func parseSystemctlShow(output string) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	return props
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
```

**Step 2: Build**

Run: `./build.sh build`
Expected: Build succeeds

**Step 3: Commit**

```bash
git add service_linux.go
git commit -m "Add systemd ServiceManager implementation for Linux"
```

---

### Task 3: Add unsupported platform stub

**Files:**
- Create: `service_unsupported.go`

**Step 1: Create service_unsupported.go**

```go
//go:build !linux

package main

import "fmt"

type unsupportedManager struct{}

func NewServiceManager(name string, system bool) ServiceManager {
	return &unsupportedManager{}
}

func (m *unsupportedManager) Install(opts ServiceOpts) error {
	return fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Uninstall() error {
	return fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Enable() error {
	return fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Disable() error {
	return fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Status() (*ServiceStatus, error) {
	return nil, fmt.Errorf("service management is not supported on this platform")
}

func (m *unsupportedManager) Logs(follow bool, lines int) error {
	return fmt.Errorf("service management is not supported on this platform")
}
```

**Step 2: Build**

Run: `./build.sh build`
Expected: Build succeeds (on Linux, this file is excluded by build tag)

**Step 3: Commit**

```bash
git add service_unsupported.go
git commit -m "Add unsupported platform stub for ServiceManager"
```

---

### Task 4: Add service subcommand with sub-subcommands

**Files:**
- Modify: `main.go`

**Step 1: Add the ServiceCmd and its sub-subcommand structs**

Add after `ScanCmd`:

```go
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
```

**Step 2: Register in opts**

Add to the opts struct:

```go
Service ServiceCmd `command:"service" description:"Manage thought-store as a system service"`
```

**Step 3: Add Execute methods**

```go
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
```

**Step 4: Build**

Run: `./build.sh build`
Expected: Build succeeds

**Step 5: Commit**

```bash
git add main.go
git commit -m "Add service subcommand with install/uninstall/enable/disable/status/logs"
```

---

### Task 5: Add unit file template test

**Files:**
- Create: `service_test.go`

**Step 1: Write tests**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePaths(t *testing.T) {
	// Create temp files to resolve
	dir := t.TempDir()
	binPath := filepath.Join(dir, "thought-store")
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(binPath, []byte("binary"), 0755)
	os.WriteFile(cfgPath, []byte("config"), 0644)

	absBin, absCfg, err := ResolvePaths(binPath, cfgPath)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	if !filepath.IsAbs(absBin) {
		t.Errorf("expected absolute binary path, got %s", absBin)
	}
	if !filepath.IsAbs(absCfg) {
		t.Errorf("expected absolute config path, got %s", absCfg)
	}
}

func TestResolvePathsMissing(t *testing.T) {
	_, _, err := ResolvePaths("/nonexistent/binary", "/nonexistent/config")
	if err == nil {
		t.Fatal("expected error for missing files")
	}
}
```

**Step 2: Run tests**

Run: `./build.sh test`
Expected: All tests pass

**Step 3: Commit**

```bash
git add service_test.go
git commit -m "Add service path resolution tests"
```

---

### Task 6: Add Linux-specific unit template test

**Files:**
- Create: `service_linux_test.go`

**Step 1: Write test**

```go
//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestUnitFileRendering(t *testing.T) {
	mgr := NewServiceManager("test-service", false).(*systemdManager)

	// Verify unit file path for user service
	path := mgr.unitFilePath()
	if !strings.Contains(path, ".config/systemd/user/test-service.service") {
		t.Errorf("unexpected user unit path: %s", path)
	}

	// Verify system unit file path
	sysMgr := NewServiceManager("test-service", true).(*systemdManager)
	sysPath := sysMgr.unitFilePath()
	if sysPath != "/etc/systemd/system/test-service.service" {
		t.Errorf("unexpected system unit path: %s", sysPath)
	}
}

func TestParseSystemctlShow(t *testing.T) {
	input := "ActiveState=active\nSubState=running\nMainPID=1234\nActiveEnterTimestamp=Wed 2026-03-05 15:00:00 EST\n"
	props := parseSystemctlShow(input)

	if props["ActiveState"] != "active" {
		t.Errorf("ActiveState = %q, want %q", props["ActiveState"], "active")
	}
	if props["MainPID"] != "1234" {
		t.Errorf("MainPID = %q, want %q", props["MainPID"], "1234")
	}
	if props["ActiveEnterTimestamp"] != "Wed 2026-03-05 15:00:00 EST" {
		t.Errorf("ActiveEnterTimestamp = %q", props["ActiveEnterTimestamp"])
	}
}
```

**Step 2: Run tests**

Run: `./build.sh test`
Expected: All tests pass

**Step 3: Commit**

```bash
git add service_linux_test.go
git commit -m "Add systemd unit file path and systemctl output parsing tests"
```

---

### Task 7: Update documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`

**Step 1: Update CLAUDE.md**

Architecture section — update `main.go` line:

```
main.go                 CLI entrypoint (go-flags: serve, capture, search, mcp, re-embed, scan, service)
```

Add to architecture:

```
service.go              ServiceManager interface and types (platform-agnostic)
service_linux.go        Systemd implementation (build-tagged linux)
service_unsupported.go  Stub for unsupported platforms
```

In Key patterns, add:

```
- **Service management**: `./thought-store service install` installs as a systemd service (user by default, `--system` for system-level). Build-tagged `ServiceManager` interface with per-platform implementations. `--copy-binary` and `--install-config` flags for full deployment.
```

In Tests, add:

```
- `service_test.go` — path resolution tests
- `service_linux_test.go` — unit file paths and systemctl output parsing
```

**Step 2: Update README.md**

Update CLI subcommands line:

```
- **CLI** via go-flags with `serve`, `capture`, `search`, `mcp`, `re-embed`, `scan`, `service` subcommands
```

Add after Network Discovery section:

```markdown
### Service Management

Install thought-store as a system service:

```bash
# Install as user service (no sudo required)
./thought-store service install --config thought-store.toml

# Install as system service
sudo ./thought-store service install --config thought-store.toml --system

# Full deployment: copy binary + config to standard locations
sudo ./thought-store service install --config thought-store.toml --system --copy-binary --install-config

# Enable, check status, view logs
./thought-store service enable
./thought-store service status
./thought-store service logs -f

# Uninstall
./thought-store service uninstall
```
```

**Step 3: Commit**

```bash
git add CLAUDE.md README.md
git commit -m "Document service management subcommand"
```

---

## Summary

| Task | What | Key files |
|------|------|-----------|
| 1 | ServiceManager interface + types | service.go |
| 2 | Systemd implementation | service_linux.go |
| 3 | Unsupported platform stub | service_unsupported.go |
| 4 | Service subcommand with sub-subcommands | main.go |
| 5 | Path resolution tests | service_test.go |
| 6 | Linux-specific unit file tests | service_linux_test.go |
| 7 | Documentation | CLAUDE.md, README.md |
