package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func TestStorageLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "etapo-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	store, err := New(dbPath, 1*time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// 1. Insert Plugs
	plug := models.Plug{
		ID:         "test-plug-1",
		Name:       "Test Socket",
		Model:      "P115",
		DriverType: "mock",
		IP:         "192.168.1.100",
		IsActive:   true,
		IsOn:       true,
	}
	if err := store.UpsertPlug(ctx, plug); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}

	// 2. Record Readings into Write Buffer
	reading := models.EnergyReading{
		PlugID:         "test-plug-1",
		Timestamp:      time.Now().UTC(),
		PowerWatts:     125.5,
		VoltageV:       231.0,
		CurrentMA:      543.0,
		EnergyTodayKWh: 0.450,
		EnergyMonthKWh: 12.30,
	}
	store.RecordReading(reading)

	// Flush buffer
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	// 3. Verify Plugs query returns latest reading
	plugs, err := store.GetPlugs(ctx)
	if err != nil {
		t.Fatalf("failed to get plugs: %v", err)
	}
	if len(plugs) != 1 {
		t.Fatalf("expected 1 plug, got %d", len(plugs))
	}
	if plugs[0].LatestReading == nil {
		t.Fatalf("expected latest reading to be populated")
	}
	if plugs[0].LatestReading.PowerWatts != 125.5 {
		t.Errorf("expected 125.5W, got %f", plugs[0].LatestReading.PowerWatts)
	}

	// 4. Verify Summary
	summary, err := store.GetSummary(ctx, "EUR", 0.30)
	if err != nil {
		t.Fatalf("failed to get summary: %v", err)
	}
	if summary.TotalPowerWatts != 125.5 {
		t.Errorf("expected 125.5W total power, got %f", summary.TotalPowerWatts)
	}
	if summary.ActivePlugsCount != 1 {
		t.Errorf("expected 1 active plug, got %d", summary.ActivePlugsCount)
	}

	// 5. Test History query
	history, err := store.GetHistory(ctx, "test-plug-1", time.Now().Add(-1*time.Hour), 50)
	if err != nil {
		t.Fatalf("failed to get history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 history point, got %d", len(history))
	}
}
