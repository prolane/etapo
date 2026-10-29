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

func TestDayStatsUseRequestedDay(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertPlug(ctx, models.Plug{
		ID: "p1", Name: "Socket", Model: "P115", DriverType: "mock", IP: "192.168.1.100", IsActive: true,
	}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	yesterday := today.AddDate(0, 0, -1)
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: yesterday.Add(23*time.Hour + 59*time.Minute + 59*time.Second), PowerWatts: 900, EnergyTodayKWh: 7})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: yesterday.Add(12 * time.Hour), PowerWatts: 300, EnergyTodayKWh: 4})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today, PowerWatts: 40, EnergyTodayKWh: 0.5})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today.Add(12 * time.Hour), PowerWatts: 60, EnergyTodayKWh: 3})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	stats, err := store.GetDayStats(ctx, today, 0.30)
	if err != nil {
		t.Fatalf("GetDayStats failed: %v", err)
	}
	if len(stats) != 1 || stats[0].PeakWatts != 60 || stats[0].EnergyKWh != 3 {
		t.Errorf("today: expected peak 60W and 3 kWh, got %+v", stats)
	}

	stats, err = store.GetDayStats(ctx, yesterday, 0.30)
	if err != nil {
		t.Fatalf("GetDayStats failed: %v", err)
	}
	if len(stats) != 1 || stats[0].PeakWatts != 900 || stats[0].EnergyKWh != 7 {
		t.Errorf("yesterday: expected peak 900W and 7 kWh, got %+v", stats)
	}
	if stats[0].EstimatedCost != 2.1 {
		t.Errorf("yesterday: expected cost 2.10, got %v", stats[0].EstimatedCost)
	}
}
