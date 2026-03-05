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

// NewServiceManager returns a systemd-based ServiceManager for Linux.
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

	fmt.Fprintln(os.Stderr, "Service installed. Use 'service enable' to start on boot.")
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
	for line := range strings.SplitSeq(output, "\n") {
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
