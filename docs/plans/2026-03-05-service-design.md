# Service Management Design

**Goal:** Add a `service` subcommand that installs, manages, and monitors thought-store as a system service, with a platform-agnostic interface and build-tagged implementations.

## Interface

```go
type ServiceManager interface {
    Install(opts ServiceOpts) error
    Uninstall() error
    Enable() error
    Disable() error
    Status() (*ServiceStatus, error)
    Logs(follow bool, lines int) error
}

type ServiceOpts struct {
    BinaryPath  string // absolute path to the binary to run
    ConfigPath  string // absolute path to the config file
    CopyBinary  bool   // copy binary to install dir
    InstallDir  string // where to copy binary (default: /usr/local/bin)
    CopyConfig  bool   // copy config to standard location
    System      bool   // system-level vs user-level service
}

type ServiceStatus struct {
    Installed bool
    Running   bool
    Enabled   bool   // starts on boot/login
    PID       int    // 0 if not running
    Uptime    string // empty if not running
}
```

## File Layout

```
service.go              # Interface, types, ServiceCmd (platform-agnostic)
service_linux.go        # //go:build linux — systemd implementation
service_unsupported.go  # //go:build !linux — "not supported on this platform"
```

Future:
```
service_windows.go      # //go:build windows — Windows Services
service_darwin.go       # //go:build darwin — launchd
```

Each platform file exports `func NewServiceManager(name string) ServiceManager`. The unsupported build tag grows as platforms are added (`//go:build !linux && !windows`).

## Subcommand Structure

```
./thought-store service install [--config] [--copy-binary] [--install-dir] [--install-config] [--system]
./thought-store service uninstall [--system]
./thought-store service enable [--system]
./thought-store service disable [--system]
./thought-store service status [--system]
./thought-store service logs [--follow] [--lines] [--system]
```

go-flags nested subcommands: `ServiceCmd` contains `ServiceInstallCmd`, `ServiceUninstallCmd`, etc.

## Install Behavior

- Default: create service file pointing to current binary + config paths (absolutized)
- `--copy-binary`: copy binary to `--install-dir` (default `/usr/local/bin`), service file points there
- `--install-config`: copy config to standard location (`/etc/thought-store/` for system, `~/.config/thought-store/` for user)
- Default is user service (`~/.config/systemd/user/`); `--system` for system service (`/etc/systemd/system/`)

## Systemd Implementation (Linux)

Unit file template:

```ini
[Unit]
Description=Thought Store
After=network.target postgresql.service

[Service]
Type=simple
ExecStart={{.BinaryPath}} serve --config {{.ConfigPath}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy={{.WantedBy}}
```

- `WantedBy`: `default.target` (user) or `multi-user.target` (system)
- Unit file locations: `~/.config/systemd/user/` (user) or `/etc/systemd/system/` (system)
- All operations via `exec.Command("systemctl", ...)` and `exec.Command("journalctl", ...)`
- Status via `systemctl show --property=ActiveState,SubState,MainPID,ActiveEnterTimestamp`
- Logs: `journalctl --user-unit` (user) or `--unit` (system), `--no-pager`, `-n`, optional `-f`

## Safety Guardrails

- **Install**: check if unit file exists, require `--force` to overwrite
- **Uninstall**: stop and disable before removing unit file
- **`--system`**: check `os.Geteuid() == 0`, fail early with clear message
- **`--copy-binary`**: verify install dir exists and is writable
- **Daemon reload**: automatic `systemctl daemon-reload` after install/uninstall

## Testing

- Unit file template rendering (pure string, no systemd)
- ServiceOpts path resolution (relative → absolute)
- Unsupported platform error message
- No live systemctl tests (manual verification)
