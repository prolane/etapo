package config

import (
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// PlugConfig holds configuration for an individual plug.
type PlugConfig struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Model       string `yaml:"model"`        // e.g. "P115"
	Type        string `yaml:"type"`         // "mock" or "tapo"
	IP          string `yaml:"ip"`           // Local LAN IP, e.g. 192.168.1.50
	MockProfile string `yaml:"mock_profile"` // "fridge", "tv", "workstation", "kettle"
	Username    string `yaml:"username"`     // Overrides global Tapo credentials if set
	Password    string `yaml:"password"`
}

// Config represents the application configuration.
type Config struct {
	Server struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"server"`

	Storage struct {
		DBPath           string        `yaml:"db_path"`
		FlushIntervalSec int           `yaml:"flush_interval_sec"`
		MaxBufferSize    int           `yaml:"max_buffer_size"`
		RetentionDays    int           `yaml:"retention_days"`
	} `yaml:"storage"`

	Collector struct {
		PollIntervalSec int `yaml:"poll_interval_sec"`
	} `yaml:"collector"`

	Billing struct {
		Currency   string  `yaml:"currency"`     // e.g. "EUR", "GBP", "USD"
		CostPerKWh float64 `yaml:"cost_per_kwh"` // Electricity rate, e.g. 0.30
	} `yaml:"billing"`

	TapoAuth struct {
		Username string `yaml:"username"` // TP-Link / Tapo account email
		Password string `yaml:"password"` // TP-Link / Tapo account password
	} `yaml:"tapo_auth"`

	Plugs []PlugConfig `yaml:"plugs"`
}

// DefaultConfig returns a sane default configuration.
func DefaultConfig() *Config {
	cfg := &Config{}
	cfg.Server.Host = "0.0.0.0"
	cfg.Server.Port = 8080

	cfg.Storage.DBPath = "./data/etapo.db"
	cfg.Storage.FlushIntervalSec = 30
	cfg.Storage.MaxBufferSize = 100
	cfg.Storage.RetentionDays = 90

	cfg.Collector.PollIntervalSec = 10

	cfg.Billing.Currency = "EUR"
	cfg.Billing.CostPerKWh = 0.32

	return cfg
}

// Load reads and parses a YAML configuration file.
func Load(filePath string) (*Config, error) {
	cfg := DefaultConfig()

	if filePath == "" {
		return cfg, nil
	}

	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file %q does not exist", filePath)
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse yaml config: %w", err)
	}

	// Environment variable overrides
	if portStr := os.Getenv("ETAPO_PORT"); portStr != "" {
		var port int
		if _, err := fmt.Sscanf(portStr, "%d", &port); err == nil && port > 0 {
			cfg.Server.Port = port
		}
	}
	if dbPath := os.Getenv("ETAPO_DB_PATH"); dbPath != "" {
		cfg.Storage.DBPath = dbPath
	}
	if user := os.Getenv("TAPO_USERNAME"); user != "" {
		cfg.TapoAuth.Username = user
	}
	if pass := os.Getenv("TAPO_PASSWORD"); pass != "" {
		cfg.TapoAuth.Password = pass
	}

	return cfg, nil
}

// FlushDuration returns the flush interval as time.Duration.
func (c *Config) FlushDuration() time.Duration {
	if c.Storage.FlushIntervalSec <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.Storage.FlushIntervalSec) * time.Second
}

// PollDuration returns the poll interval as time.Duration.
func (c *Config) PollDuration() time.Duration {
	if c.Collector.PollIntervalSec <= 0 {
		return 10 * time.Second
	}
	return time.Duration(c.Collector.PollIntervalSec) * time.Second
}
