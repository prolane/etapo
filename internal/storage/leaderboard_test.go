package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func TestMonthStatsUsesRequestedMonth(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertPlug(ctx, models.Plug{
		ID: "p1", Name: "Socket", Model: "P115", DriverType: "mock", IP: "192.168.1.100", IsActive: true, IsOn: true,
	}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}

	now := time.Now()
	prevMonth := time.Date(now.Year(), now.Month()-1, 15, 12, 0, 0, 0, time.Local)
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: prevMonth, PowerWatts: 900, EnergyMonthKWh: 40})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: now, PowerWatts: 50, EnergyMonthKWh: 2})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	current, err := store.GetMonthStats(ctx, now, 0.30)
	if err != nil {
		t.Fatalf("GetMonthStats failed: %v", err)
	}
	if len(current) != 1 || current[0].PeakWatts != 50 || current[0].EnergyKWh != 2 {
		t.Errorf("current month: expected peak 50W and 2 kWh, got %+v", current)
	}

	previous, err := store.GetMonthStats(ctx, prevMonth, 0.30)
	if err != nil {
		t.Fatalf("GetMonthStats failed: %v", err)
	}
	if len(previous) != 1 || previous[0].PeakWatts != 900 || previous[0].EnergyKWh != 40 {
		t.Errorf("previous month: expected peak 900W and 40 kWh, got %+v", previous)
	}
	if previous[0].EstimatedCost != 12 {
		t.Errorf("previous month: expected cost 12.00, got %v", previous[0].EstimatedCost)
	}
}
