package main

import (
	"net"
	"net/url"
	"time"
)

// BrainMode indicates whether the server is running with a local or remote brain.
type BrainMode int

const (
	BrainModeLocal BrainMode = iota
	BrainModeRemote
)

func (m BrainMode) String() string {
	switch m {
	case BrainModeLocal:
		return "local"
	case BrainModeRemote:
		return "remote"
	default:
		return "unknown"
	}
}

// MarshalText implements encoding.TextMarshaler so JSON output is "local"/"remote".
func (m BrainMode) MarshalText() ([]byte, error) {
	return []byte(m.String()), nil
}

// DiagnosticsReport is the top-level diagnostics response.
type DiagnosticsReport struct {
	Mode      BrainMode     `json:"mode"`
	StartedAt time.Time     `json:"started_at"`
	Uptime    string        `json:"uptime"`
	GoVersion string        `json:"go_version"`
	Postgres  *PostgresDiag `json:"postgres,omitempty"`
	Ollama    *OllamaDiag   `json:"ollama,omitempty"`
	Remote    *RemoteDiag   `json:"remote,omitempty"`
}

// PostgresDiag contains diagnostics for the Postgres connection.
type PostgresDiag struct {
	Version         string `json:"version"`
	PgvectorVersion string `json:"pgvector_version"`
	Host            string `json:"host"`
	Port            uint16 `json:"port"`
	Database        string `json:"database"`
	Status          string `json:"status"`
	SchemaVersion   int    `json:"schema_version"`
	ThoughtCount    int    `json:"thought_count"`
	HNSWIndex       bool   `json:"hnsw_index"`
	PoolTotal       int32  `json:"pool_total"`
	PoolIdle        int32  `json:"pool_idle"`
	PoolInUse       int32    `json:"pool_in_use"`
	Errors          []string `json:"errors,omitempty"`
}

// OllamaDiag contains diagnostics for the Ollama embedding service.
type OllamaDiag struct {
	URL           string `json:"url"`
	Version       string `json:"version"`
	Status        string `json:"status"`
	Model         string `json:"model"`
	Family        string `json:"family,omitempty"`
	ParameterSize string `json:"parameter_size,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
	EmbeddingDim  int      `json:"embedding_dim,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

// RemoteDiag contains diagnostics for a remote brain connection.
type RemoteDiag struct {
	URLs        []string           `json:"urls"`
	ActiveURL   string             `json:"active_url"`
	NetworkType string             `json:"network_type"`
	Latency     string             `json:"latency"`
	ServerDiag  *DiagnosticsReport `json:"server_diagnostics,omitempty"`
	Errors      []string           `json:"errors,omitempty"`
}

// tailscaleCIDR is the CGNAT range used by Tailscale (100.64.0.0/10).
var tailscaleCIDR *net.IPNet

func init() {
	_, tailscaleCIDR, _ = net.ParseCIDR("100.64.0.0/10")
}

// NetworkType classifies a URL's host into a network category.
func NetworkType(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "unknown"
	}

	host := u.Hostname()
	if host == "" {
		return "unknown"
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Not an IP address — check for localhost hostname.
		if host == "localhost" {
			return "loopback"
		}
		return "dns"
	}

	if ip.IsLoopback() {
		return "loopback"
	}

	if tailscaleCIDR.Contains(ip) {
		return "tailscale"
	}

	if ip.IsPrivate() {
		return "lan"
	}

	return "public"
}
