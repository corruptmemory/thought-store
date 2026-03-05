package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Database DatabaseConfig `toml:"database"`
	Ollama   OllamaConfig   `toml:"ollama"`
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
