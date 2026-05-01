package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Homeserver    string `yaml:"homeserver"`
	AccessToken   string `yaml:"access_token"`
	RoomID        string `yaml:"room_id"`
	ListenAddress string `yaml:"listen_address"`
	ListenPort    int    `yaml:"listen_port"`
	CacheDir      string `yaml:"cache_dir"`
}

// ListenAddr returns the combined host:port string for http.ListenAndServe.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.ListenAddress, c.ListenPort)
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	cfg := &Config{
		ListenAddress: "0.0.0.0",
		ListenPort:    8008,
		CacheDir:      "./cache",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Homeserver == "" {
		return nil, fmt.Errorf("homeserver is required")
	}
	if cfg.AccessToken == "" {
		return nil, fmt.Errorf("access_token is required")
	}
	if cfg.RoomID == "" {
		return nil, fmt.Errorf("room_id is required")
	}
	return cfg, nil
}
