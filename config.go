package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type MCPConfig struct {
	URLs []string `toml:"urls"`
}

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Database DatabaseConfig `toml:"database"`
	Ollama   OllamaConfig   `toml:"ollama"`
	MCP      MCPConfig      `toml:"mcp"`
	MDNS     MDNSConfig     `toml:"mdns"`
}

type ServerConfig struct {
	Addr string `toml:"addr"`
}

type DatabaseConfig struct {
	URL string `toml:"url"`
}

type OllamaConfig struct {
	URL   string `toml:"url"`
	Model string `toml:"model"`
}

type MDNSConfig struct {
	Name    string `toml:"name"`
	Service string `toml:"service"`
}

// ServiceType returns the configured service type or the default.
func (c MDNSConfig) ServiceType() string {
	if c.Service != "" {
		return c.Service
	}
	return "_thought-store._tcp"
}

// WriteConfig marshals the config to TOML and writes it to the given path.
func WriteConfig(path string, cfg *Config) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create config file: %w", err)
	}
	defer f.Close()

	encoder := toml.NewEncoder(f)
	if err := encoder.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}
