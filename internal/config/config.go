package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type RouteConfig struct {
	Path    string        `yaml:"path"`
	Backend string        `yaml:"backend"`
	Limit   int           `yaml:"limit"`
	Window  time.Duration `yaml:"window"`
}

type ClientOverride struct {
	ClientID string `yaml:"client_id"`
	Limit    int    `yaml:"limit"`
	Window   time.Duration `yaml:"window"`
}

type Config struct {
	ListenAddr     string           `yaml:"listen_addr"`
	RedisAddr      string           `yaml:"redis_addr"`
	DefaultLimit   int              `yaml:"default_limit"`
	DefaultWindow  time.Duration    `yaml:"default_window"`
	Routes         []RouteConfig    `yaml:"routes"`
	ClientOverrides []ClientOverride `yaml:"client_overrides"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.RedisAddr == "" {
		cfg.RedisAddr = "localhost:6379"
	}
	if cfg.DefaultLimit == 0 {
		cfg.DefaultLimit = 100
	}
	if cfg.DefaultWindow == 0 {
		cfg.DefaultWindow = time.Minute
	}
	return &cfg, nil
}

func (c *Config) RouteFor(path string) *RouteConfig {
	for i := range c.Routes {
		if matchPath(c.Routes[i].Path, path) {
			return &c.Routes[i]
		}
	}
	return nil
}

func (c *Config) ClientLimit(clientID string) (int, time.Duration) {
	for _, o := range c.ClientOverrides {
		if o.ClientID == clientID {
			return o.Limit, o.Window
		}
	}
	return c.DefaultLimit, c.DefaultWindow
}

func matchPath(pattern, path string) bool {
	if len(pattern) == 0 {
		return false
	}
	if pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(path) >= len(prefix) && path[:len(prefix)] == prefix
	}
	return pattern == path
}
