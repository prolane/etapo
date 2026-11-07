package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"etapo/internal/config"
	"etapo/internal/models"
	"etapo/internal/storage"
)

const apiTestConfig = `server:
  port: 8080

tapo_auth:
  username: "user@example.com"
  password: "secret"

plugs:
  - id: "office"
    name: "Office"
    type: "tapo"
    ip: "192.168.1.103"
`

// newConfigIPServer returns an API server whose config.yaml lists one Tapo plug at 192.168.1.103 that is now reachable at 192.168.1.120.
func newConfigIPServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(apiTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	store, err := storage.New(filepath.Join(dir, "etapo.db"), time.Second)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.UpsertPlug(t.Context(), models.Plug{
		ID: "office", Name: "Office", Model: "P115", DriverType: "tapo", IP: "192.168.1.120", IsActive: true, IsOn: true,
	}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}

	return NewServer(cfg, store, nil, NewSSEBroker(), nil), cfgPath
}

func TestPlugsReportConfigAddressAndOnlineState(t *testing.T) {
	server, _ := newConfigIPServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/plugs", nil)
	rr := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var plugs []models.PlugWithLatest
	if err := json.Unmarshal(rr.Body.Bytes(), &plugs); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(plugs) != 1 {
		t.Fatalf("expected 1 plug, got %d", len(plugs))
	}
	if plugs[0].IP != "192.168.1.120" || plugs[0].ConfigIP != "192.168.1.103" {
		t.Errorf("expected ip 192.168.1.120 and config_ip 192.168.1.103, got %q and %q", plugs[0].IP, plugs[0].ConfigIP)
	}
	if plugs[0].Online {
		t.Error("expected a plug without readings to be offline")
	}
}

func TestConfigIPWritesTheDetectedAddress(t *testing.T) {
	server, cfgPath := newConfigIPServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/plugs/office/config-ip", strings.NewReader(`{"ip":"192.168.1.120"}`))
	rr := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `ip: "192.168.1.120"`) {
		t.Errorf("expected config.yaml to hold the new address, got:\n%s", data)
	}
	if server.cfg.Plugs[0].IP != "192.168.1.120" {
		t.Errorf("expected the in-memory address to be updated, got %s", server.cfg.Plugs[0].IP)
	}

	// The address is now the configured one, so writing it again is refused
	req = httptest.NewRequest(http.MethodPost, "/api/plugs/office/config-ip", strings.NewReader(`{"ip":"192.168.1.120"}`))
	rr = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409 when config.yaml already uses the address, got %d", rr.Code)
	}
}

func TestConfigIPRejectsAnAddressThatIsNotCurrent(t *testing.T) {
	server, cfgPath := newConfigIPServer(t)
	before, _ := os.ReadFile(cfgPath)

	req := httptest.NewRequest(http.MethodPost, "/api/plugs/office/config-ip", strings.NewReader(`{"ip":"192.168.1.199"}`))
	rr := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("expected 409 for an address the plug is not at, got %d", rr.Code)
	}

	after, _ := os.ReadFile(cfgPath)
	if string(before) != string(after) {
		t.Error("config.yaml changed although the request was rejected")
	}
}

func TestConfigIPRejectsUnknownPlugsAndWrongMethods(t *testing.T) {
	server, _ := newConfigIPServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/plugs/missing/config-ip", strings.NewReader(`{"ip":"192.168.1.120"}`))
	rr := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for a plug that is not in config.yaml, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/plugs/office/config-ip", nil)
	rr = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", rr.Code)
	}
}
