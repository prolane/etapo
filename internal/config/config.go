package config

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PlugConfig holds configuration for an individual plug.
type PlugConfig struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Model       string `yaml:"model"`        // e.g. "P115"
	Category    string `yaml:"category"`     // e.g. "Kitchen", "Office", "Entertainment", "Utility"
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
		DBPath           string `yaml:"db_path"`
		FlushIntervalSec int    `yaml:"flush_interval_sec"`
		MaxBufferSize    int    `yaml:"max_buffer_size"`
		RetentionDays    int    `yaml:"retention_days"`
	} `yaml:"storage"`

	Collector struct {
		PollIntervalSec int `yaml:"poll_interval_sec"`
	} `yaml:"collector"`

	Billing struct {
		Currency   string  `yaml:"currency"`     // e.g. "EUR", "GBP", "USD"
		CostPerKWh float64 `yaml:"cost_per_kwh"` // Electricity rate, e.g. 0.30
	} `yaml:"billing"`

	Standby struct {
		Start string `yaml:"start"` // Local start of the standby window, HH:MM
		End   string `yaml:"end"`   // Local end of the standby window, HH:MM (earlier than start wraps past midnight)
	} `yaml:"standby"`

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

	cfg.Standby.Start = "02:00"
	cfg.Standby.End = "05:00"

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

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", filePath, err)
	}

	return cfg, nil
}

// placeholderValues are the example values from config.example.yaml that must be replaced before use.
var placeholderValues = []string{"your-tplink-email@example.com", "your-tplink-password"}

// Validate checks the configuration for values that would make the daemon fail or misbehave at runtime.
func (c *Config) Validate() error {
	var errs []error

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("server.port must be between 1 and 65535, got %d", c.Server.Port))
	}
	if c.Storage.DBPath == "" {
		errs = append(errs, errors.New("storage.db_path must not be empty"))
	}
	if c.Storage.FlushIntervalSec <= 0 {
		errs = append(errs, fmt.Errorf("storage.flush_interval_sec must be greater than 0, got %d", c.Storage.FlushIntervalSec))
	}
	if c.Storage.MaxBufferSize <= 0 {
		errs = append(errs, fmt.Errorf("storage.max_buffer_size must be greater than 0, got %d", c.Storage.MaxBufferSize))
	}
	if c.Storage.RetentionDays <= 0 {
		errs = append(errs, fmt.Errorf("storage.retention_days must be greater than 0, got %d", c.Storage.RetentionDays))
	}
	if c.Collector.PollIntervalSec <= 0 {
		errs = append(errs, fmt.Errorf("collector.poll_interval_sec must be greater than 0, got %d", c.Collector.PollIntervalSec))
	}
	if c.Billing.CostPerKWh < 0 {
		errs = append(errs, fmt.Errorf("billing.cost_per_kwh must not be negative, got %v", c.Billing.CostPerKWh))
	}
	if _, _, err := c.StandbyMinutes(); err != nil {
		errs = append(errs, err)
	}

	seen := make(map[string]bool)
	for i, p := range c.Plugs {
		prefix := fmt.Sprintf("plugs[%d]", i)
		if p.ID == "" {
			errs = append(errs, fmt.Errorf("%s: id must not be empty", prefix))
		} else if seen[p.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id %q", prefix, p.ID))
		}
		seen[p.ID] = true
		prefix = fmt.Sprintf("plugs[%d] (%s)", i, p.ID)

		switch p.Type {
		case "mock", "":
		case "tapo":
			if net.ParseIP(p.IP) == nil {
				errs = append(errs, fmt.Errorf("%s: ip must be a valid IP address for type \"tapo\", got %q", prefix, p.IP))
			}
			username := p.Username
			if username == "" {
				username = c.TapoAuth.Username
			}
			password := p.Password
			if password == "" {
				password = c.TapoAuth.Password
			}
			if username == "" {
				errs = append(errs, fmt.Errorf("%s: no Tapo username; set tapo_auth.username, TAPO_USERNAME, or the plug's username", prefix))
			} else if isPlaceholder(username) {
				errs = append(errs, fmt.Errorf("%s: Tapo username is still the example placeholder %q", prefix, username))
			}
			if password == "" {
				errs = append(errs, fmt.Errorf("%s: no Tapo password; set tapo_auth.password, TAPO_PASSWORD, or the plug's password", prefix))
			} else if isPlaceholder(password) {
				errs = append(errs, fmt.Errorf("%s: Tapo password is still the example placeholder", prefix))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: unknown type %q (use \"tapo\" or \"mock\")", prefix, p.Type))
		}
	}

	return errors.Join(errs...)
}

// StandbyMinutes returns the standby window start and end as minutes after local midnight.
func (c *Config) StandbyMinutes() (int, int, error) {
	start, err := parseClock(c.Standby.Start)
	if err != nil {
		return 0, 0, fmt.Errorf("standby.start %w", err)
	}
	end, err := parseClock(c.Standby.End)
	if err != nil {
		return 0, 0, fmt.Errorf("standby.end %w", err)
	}
	if start == end {
		return 0, 0, errors.New("standby.start and standby.end must be different")
	}
	return start, end, nil
}

func parseClock(value string) (int, error) {
	t, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("must be a time in HH:MM format, got %q", value)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func isPlaceholder(value string) bool {
	if strings.HasPrefix(value, "REPLACE_WITH_") {
		return true
	}
	for _, p := range placeholderValues {
		if value == p {
			return true
		}
	}
	return false
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
