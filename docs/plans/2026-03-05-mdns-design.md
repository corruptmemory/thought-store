# mDNS Service Discovery Design

**Goal:** Advertise the thought-store HTTP server on the local network via mDNS/DNS-SD, and allow remote clients to auto-discover it by name instead of hardcoding URLs.

**Library:** `github.com/hashicorp/mdns` — pure Go, no CGo, cross-platform (Linux, macOS, Windows).

## Config

```toml
[mdns]
name = "jim-brain"                    # instance name (advertise + discover)
service = "_thought-store._tcp"       # service type (default if omitted)
```

- `name` is the single knob. Empty/absent = mDNS disabled entirely.
- `service` defaults to `"_thought-store._tcp"` if omitted.
- Three discovery strategies fall out naturally from config:
  1. `name` set, no `urls` → discover only
  2. `name` set + `urls` → discover first, URLs as fallback
  3. no `name`, `urls` set → URLs only (current behavior, unchanged)

```go
type MDNSConfig struct {
    Name    string `toml:"name"`
    Service string `toml:"service"`  // default: "_thought-store._tcp"
}
```

## Advertise (serve side)

When `serve` starts and `[mdns].name` is non-empty:

1. Parse listen address to extract port (`:3000` → 3000)
2. Create `mdns.MDNSService` with name, service type, port, TXT record `path=/api`
3. Start `mdns.Server` in background — responds to multicast queries
4. Shut down when server exits

Advertised as `jim-brain._thought-store._tcp.local.`. Machine IPs resolved automatically by hashicorp/mdns. No new subcommand — advertising is a side effect of `serve` when configured.

## Discover (remote client side)

When `mcp --remote` starts and `[mdns].name` is configured:

1. Browse for configured service type, filter by instance name
2. Timeout after ~2 seconds (LAN — if not there in 2s, it's not there)
3. Build URL from discovered host:port + TXT record path
4. Prepend discovered URLs to configured fallback URLs
5. Pass combined list to `NewRemoteBrain` — existing fallback chain handles the rest

Standalone function: `DiscoverThoughtStores(ctx, name, service, timeout) ([]string, error)`. RemoteBrain stays unchanged — still just takes `[]string` of URLs.

Multiple IPs for same instance (IPv4 + IPv6, multiple interfaces) all included — fallback chain tries each.

## `scan` subcommand

`./thought-store scan` — interactive discovery and config writer.

1. Browse for all instances of service type (no name filter), timeout ~3 seconds
2. Print numbered list to stderr: `1) jim-brain (192.168.1.50:3000)`
3. Prompt user to pick a number (stdin)
4. Write selection to TOML config: set `[mdns].name` to chosen instance name

TOML writing: read, unmarshal, update `MDNS.Name`, marshal back. Comments will be lost (file is short and machine-managed). Print warning before writing.

```go
type ScanCmd struct {
    Config  string `long:"config" default:"thought-store.toml" description:"Path to config file"`
    Service string `long:"service" default:"_thought-store._tcp" description:"Service type to scan for"`
    Timeout int    `long:"timeout" default:"3" description:"Discovery timeout in seconds"`
}
```

## Diagnostics integration

- **LocalBrain**: If advertising, include `mdns_name` and `mdns_service` in report
- **RemoteBrain**: Include `discovered_urls` alongside configured `urls`

Lightweight — extra fields on existing diagnostic structs, no new types.

## Testing

- **Discovery round-trip**: Advertise test service on random port, discover it. Real mDNS on loopback (~1s). Skip if multicast unavailable.
- **Config writing**: Table-driven marshal/unmarshal round-trip with `[mdns]` variations.
- **Scan subcommand**: Manual verification (interactive stdin).
