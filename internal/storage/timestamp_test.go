package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func newTimestampTestStore(t *testing.T) *Storage {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.UpsertPlug(context.Background(), models.Plug{
		ID: "p1", Name: "Socket", Model: "P115", DriverType: "mock", IP: "192.168.1.100", IsActive: true, IsOn: true,
	}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}
	return store
}

func TestMonthStatsExcludeReadingsBeforeTheMonth(t *testing.T) {
	store := newTimestampTestStore(t)
	ctx := context.Background()

	now := time.Now()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: thisMonth.Add(-time.Second), PowerWatts: 900, EnergyMonthKWh: 99})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: thisMonth, PowerWatts: 50, EnergyMonthKWh: 2})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	stats, err := store.GetMonthStats(ctx, now, 0.30)
	if err != nil {
		t.Fatalf("GetMonthStats failed: %v", err)
	}
	if len(stats) != 1 || stats[0].PeakWatts != 50 || stats[0].EnergyKWh != 2 {
		t.Errorf("expected only this month's readings (50W, 2 kWh), got %+v", stats)
	}
}
