package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"etapo/internal/collector"
	"etapo/internal/config"
	"etapo/internal/storage"
)

func TestAPIServer(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "etapo-api-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := config.DefaultConfig()
	cfg.Storage.DBPath = filepath.Join(tempDir, "test.db")
	cfg.Plugs = []config.PlugConfig{
		{
			ID:          "p1",
			Name:        "Desk Lamp",
			Model:       "P115",
			Type:        "mock",
			MockProfile: "default",
		},
	}

	store, err := storage.New(cfg.Storage.DBPath, 1*time.Second, 10)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	broker := NewSSEBroker()
	col, err := collector.New(cfg, store, broker)
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	// Create test server
	server := NewServer(cfg, store, col, broker, nil)

	// 1. Test /health
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	// 2. Test /api/plugs
	req = httptest.NewRequest(http.MethodGet, "/api/plugs", nil)
	rr = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Desk Lamp") {
		t.Errorf("expected response to contain 'Desk Lamp', got %s", rr.Body.String())
	}

	// 3. Test /api/summary
	req = httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rr = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	// 4. Test /api/plugs/p1/power toggle
	toggleBody := strings.NewReader(`{"on": false}`)
	req = httptest.NewRequest(http.MethodPost, "/api/plugs/p1/power", toggleBody)
	rr = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 on toggle, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify plug is now OFF in storage
	plug, err := store.GetPlug(context.Background(), "p1", 0.30)
	if err != nil || plug == nil {
		t.Fatalf("failed to get plug: %v", err)
	}
	if plug.IsOn {
		t.Errorf("expected plug to be turned off")
	}
}
