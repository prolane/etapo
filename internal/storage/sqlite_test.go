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

	// 3. Verify Plugs query returns latest reading and analytics
	plugs, err := store.GetPlugs(ctx, 0.30, testStandby)
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
	if plugs[0].PeakWattsToday < 125.0 {
		t.Errorf("expected peak watts to be recorded, got %f", plugs[0].PeakWattsToday)
	}

	// 4. Verify Summary
	summary, err := store.GetSummary(ctx, "EUR", 0.30, testStandby)
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
	now := time.Now()
	history, err := store.GetHistory(ctx, "test-plug-1", now.Add(-1*time.Hour).Truncate(time.Minute), now.Add(time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("failed to get history: %v", err)
	}
	if readings := countReadings(history); len(readings) != 1 || readings[0] != 125.5 {
		t.Fatalf("expected one bucket with 125.5W, got %v", readings)
	}
}

func TestHistoryCoversWholePeriod(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), 1*time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertPlug(ctx, models.Plug{ID: "p", Name: "P", Model: "P115", DriverType: "mock", IsActive: true}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}

	// One reading every 10 minutes for the last 24 hours
	now := time.Now().UTC()
	for i := 0; i < 24*6; i++ {
		store.RecordReading(models.EnergyReading{
			PlugID:     "p",
			Timestamp:  now.Add(-time.Duration(i) * 10 * time.Minute),
			PowerWatts: float64(i),
		})
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	start := now.Add(-24 * time.Hour).Truncate(5 * time.Minute)
	history, err := store.GetHistory(ctx, "p", start, now.Add(time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatalf("failed to get history: %v", err)
	}
	if len(history) < 288 {
		t.Errorf("expected at least 288 five-minute buckets for 24 hours, got %d", len(history))
	}
	if got := len(countReadings(history)); got != 144 {
		t.Errorf("expected 144 buckets with readings, got %d", got)
	}
	var newest models.HistoryPoint
	for _, pt := range history {
		if pt.PowerWatts != nil {
			newest = pt
		}
	}
	if now.Sub(newest.Timestamp) > 10*time.Minute {
		t.Errorf("expected the newest reading in the last 10 minutes, got bucket at %v", newest.Timestamp)
	}
}

// countReadings returns the power of every bucket that has readings.
func countReadings(points []models.HistoryPoint) []float64 {
	var watts []float64
	for _, pt := range points {
		if pt.PowerWatts != nil {
			watts = append(watts, *pt.PowerWatts)
		}
	}
	return watts
}
