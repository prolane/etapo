package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() *Config {
	cfg := DefaultConfig()
	cfg.TapoAuth.Username = "user@example.com"
	cfg.TapoAuth.Password = "secret"
	cfg.Plugs = []PlugConfig{
		{ID: "office", Name: "Office", Type: "tapo", IP: "192.168.20.105"},
	}
	return cfg
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{name: "valid tapo config", mutate: func(c *Config) {}},
		{name: "valid mock config", mutate: func(c *Config) {
			c.TapoAuth.Username = ""
			c.TapoAuth.Password = ""
			c.Plugs = []PlugConfig{{ID: "tv", Type: "mock"}}
		}},
		{name: "port out of range", mutate: func(c *Config) { c.Server.Port = 70000 }, wantErr: "server.port"},
		{name: "empty db path", mutate: func(c *Config) { c.Storage.DBPath = "" }, wantErr: "storage.db_path"},
		{name: "zero poll interval", mutate: func(c *Config) { c.Collector.PollIntervalSec = 0 }, wantErr: "collector.poll_interval_sec"},
		{name: "negative cost", mutate: func(c *Config) { c.Billing.CostPerKWh = -1 }, wantErr: "billing.cost_per_kwh"},
		{name: "unknown plug type", mutate: func(c *Config) { c.Plugs[0].Type = "tapp" }, wantErr: "unknown type"},
		{name: "tapo plug without ip", mutate: func(c *Config) { c.Plugs[0].IP = "" }, wantErr: "valid IP address"},
		{name: "tapo plug with invalid ip", mutate: func(c *Config) { c.Plugs[0].IP = "plug.local" }, wantErr: "valid IP address"},
		{name: "missing password", mutate: func(c *Config) { c.TapoAuth.Password = "" }, wantErr: "no Tapo password"},
		{name: "missing username", mutate: func(c *Config) { c.TapoAuth.Username = "" }, wantErr: "no Tapo username"},
		{name: "placeholder password", mutate: func(c *Config) { c.TapoAuth.Password = "your-tplink-password" }, wantErr: "placeholder"},
		{name: "placeholder username", mutate: func(c *Config) { c.TapoAuth.Username = "your-tplink-email@example.com" }, wantErr: "placeholder"},
		{name: "replace-with password", mutate: func(c *Config) { c.TapoAuth.Password = "REPLACE_WITH_TAPO_PASSWORD" }, wantErr: "placeholder"},
		{name: "per-plug credentials satisfy global", mutate: func(c *Config) {
			c.TapoAuth.Password = ""
			c.Plugs[0].Password = "secret"
		}},
		{name: "duplicate id", mutate: func(c *Config) {
			c.Plugs = append(c.Plugs, PlugConfig{ID: "office", Type: "mock"})
		}, wantErr: "duplicate id"},
		{name: "empty id", mutate: func(c *Config) { c.Plugs[0].ID = "" }, wantErr: "id must not be empty"},
		{name: "standby window unset uses default", mutate: func(c *Config) { c.Standby.Start = ""; c.Standby.End = "" }, wantErr: "standby.start must be"},
		{name: "standby window malformed", mutate: func(c *Config) { c.Standby.End = "5am" }, wantErr: "standby.end must be"},
		{name: "standby window empty", mutate: func(c *Config) { c.Standby.Start = "03:00"; c.Standby.End = "03:00" }, wantErr: "must be different"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got none", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "plugs:\n  - id: office\n    type: tapo\n    ip: 192.168.20.105\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAPO_USERNAME", "")
	t.Setenv("TAPO_PASSWORD", "")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected Load to reject config without Tapo credentials")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("expected error to name the config file, got %v", err)
	}
}

func TestLoadAppliesEnvCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "plugs:\n  - id: office\n    type: tapo\n    ip: 192.168.20.105\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAPO_USERNAME", "user@example.com")
	t.Setenv("TAPO_PASSWORD", "secret")

	if _, err := Load(path); err != nil {
		t.Fatalf("expected env credentials to satisfy validation, got %v", err)
	}
}

func TestStandbyMinutes(t *testing.T) {
	tests := []struct {
		name       string
		start, end string
		wantStart  int
		wantEnd    int
	}{
		{name: "default window", start: "02:00", end: "05:00", wantStart: 120, wantEnd: 300},
		{name: "wraps past midnight", start: "23:30", end: "06:15", wantStart: 1410, wantEnd: 375},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Standby.Start = tt.start
			cfg.Standby.End = tt.end
			start, end, err := cfg.StandbyMinutes()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if start != tt.wantStart || end != tt.wantEnd {
				t.Fatalf("expected %d-%d, got %d-%d", tt.wantStart, tt.wantEnd, start, end)
			}
		})
	}
}
