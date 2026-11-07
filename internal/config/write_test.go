package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const writeSampleConfig = `# Etapo configuration
server:
  port: 8080

# Tapo account
tapo_auth:
  username: "user@example.com"
  password: "secret"

plugs:
  - id: "office"
    name: "Office"
    type: "tapo"
    ip: "192.168.1.103"   # plug on the desk

  - id: "kitchen"
    name: "Kitchen"
    type: "tapo"
    ip: 192.168.1.104
    model: "P115"
`

func writeSampleFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSetPlugIPChangesOnlyTheAddress(t *testing.T) {
	path := writeSampleFile(t, writeSampleConfig)

	if err := SetPlugIP(path, "office", "192.168.1.120"); err != nil {
		t.Fatalf("SetPlugIP failed: %v", err)
	}

	want := strings.Replace(writeSampleConfig, `ip: "192.168.1.103"   # plug`, `ip: "192.168.1.120"   # plug`, 1)
	if got := readFile(t, path); got != want {
		t.Errorf("unexpected file after edit\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSetPlugIPKeepsUnquotedValues(t *testing.T) {
	path := writeSampleFile(t, writeSampleConfig)

	if err := SetPlugIP(path, "kitchen", "192.168.1.130"); err != nil {
		t.Fatalf("SetPlugIP failed: %v", err)
	}

	want := strings.Replace(writeSampleConfig, "ip: 192.168.1.104\n", "ip: 192.168.1.130\n", 1)
	if got := readFile(t, path); got != want {
		t.Errorf("unexpected file after edit\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSetPlugIPResultStillLoads(t *testing.T) {
	path := writeSampleFile(t, writeSampleConfig)

	if err := SetPlugIP(path, "office", "192.168.1.120"); err != nil {
		t.Fatalf("SetPlugIP failed: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load after edit failed: %v", err)
	}
	if cfg.Path() != path {
		t.Errorf("expected Path %q, got %q", path, cfg.Path())
	}
	if cfg.Plugs[0].IP != "192.168.1.120" {
		t.Errorf("expected the new IP to be loaded, got %q", cfg.Plugs[0].IP)
	}
}

func TestSetPlugIPKeepsTwoBackups(t *testing.T) {
	path := writeSampleFile(t, writeSampleConfig)
	dir := filepath.Dir(path)
	for _, old := range []string{"config-20200101-000000.bak", "config-20200102-000000.bak", "config-20200103-000000.bak", "other-file.bak"} {
		if err := os.WriteFile(filepath.Join(dir, old), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := SetPlugIP(path, "office", "192.168.1.120"); err != nil {
		t.Fatalf("SetPlugIP failed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bak") {
			backups = append(backups, e.Name())
		}
	}
	// The two newest configuration backups are kept, the new one among them. Other .bak files are not etapo's.
	var configBackups []string
	for _, name := range backups {
		if strings.HasPrefix(name, "config-") {
			configBackups = append(configBackups, name)
		}
	}
	if len(configBackups) != 2 {
		t.Fatalf("expected 2 configuration backups, got %v", configBackups)
	}
	sort.Strings(configBackups)
	if configBackups[0] != "config-20200103-000000.bak" {
		t.Errorf("expected the newest old backup to be kept, got %v", configBackups)
	}
	if got := readFile(t, filepath.Join(dir, configBackups[1])); got != writeSampleConfig {
		t.Error("the new backup does not hold the configuration from before the edit")
	}
	if !contains(backups, "other-file.bak") {
		t.Error("a .bak file that is not etapo's backup was removed")
	}
}

func TestSetPlugIPRejectsBadInputWithoutChangingTheFile(t *testing.T) {
	path := writeSampleFile(t, writeSampleConfig)

	if err := SetPlugIP(path, "missing", "192.168.1.120"); err == nil {
		t.Error("expected an error for an unknown plug id")
	}
	if err := SetPlugIP(path, "office", "not-an-ip"); err == nil {
		t.Error("expected an error for an invalid IP address")
	}
	if got := readFile(t, path); got != writeSampleConfig {
		t.Error("the config file changed although the edit was rejected")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
