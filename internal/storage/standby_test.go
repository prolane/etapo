package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func TestStandbyRequiresNightReadings(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	for _, id := range []string{"day-only", "with-night"} {
		if err := store.UpsertPlug(ctx, models.Plug{
			ID: id, Name: id, Model: "P115", DriverType: "mock", IP: "192.168.1.100", IsActive: true, IsOn: true,
		}); err != nil {
			t.Fatalf("failed to upsert plug %s: %v", id, err)
		}
	}

	y, m, d := time.Now().Date()
	noon := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	night := time.Date(y, m, d, 3, 0, 0, 0, time.Local)

	store.RecordReading(models.EnergyReading{PlugID: "day-only", Timestamp: noon, PowerWatts: 38.8})
	store.RecordReading(models.EnergyReading{PlugID: "with-night", Timestamp: noon, PowerWatts: 38.8})
	store.RecordReading(models.EnergyReading{PlugID: "with-night", Timestamp: night, PowerWatts: 4.2})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	plugs, err := store.GetPlugs(ctx, 0.30, testStandby)
	if err != nil {
		t.Fatalf("GetPlugs failed: %v", err)
	}
	byID := make(map[string]models.PlugWithLatest)
	for _, p := range plugs {
		byID[p.ID] = p
	}

	dayOnly := byID["day-only"]
	if dayOnly.HasStandbyData {
		t.Error("expected no standby data for a plug without 02:00-05:00 readings")
	}
	if dayOnly.StandbyWatts != 0 {
		t.Errorf("expected no standby value without night readings, got %v", dayOnly.StandbyWatts)
	}

	withNight := byID["with-night"]
	if !withNight.HasStandbyData {
		t.Error("expected standby data for a plug with a 03:00 reading")
	}
	if withNight.StandbyWatts != 4.2 {
		t.Errorf("expected 4.2W standby from the 03:00 reading, got %v", withNight.StandbyWatts)
	}

	summary, err := store.GetSummary(ctx, "EUR", 0.30, testStandby)
	if err != nil {
		t.Fatalf("GetSummary failed: %v", err)
	}
	if !summary.StandbyDataAvailable {
		t.Error("expected summary to report standby data available")
	}
	if summary.TotalStandbyWatts != 4.2 {
		t.Errorf("expected total standby 4.2W, got %v", summary.TotalStandbyWatts)
	}
}

var testStandby = models.StandbyPeriod{StartMinute: 120, EndMinute: 300, Nights: 7}

func TestStandbyWindowIsConfigurable(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertPlug(ctx, models.Plug{
		ID: "p1", Name: "p1", Model: "P115", DriverType: "mock", IP: "192.168.1.100", IsActive: true, IsOn: true,
	}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}

	today := startOfLocalDay(time.Now())
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today.Add(12 * time.Hour), PowerWatts: 50})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today.Add(23*time.Hour + 30*time.Minute), PowerWatts: 3})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	wrapping := models.StandbyPeriod{StartMinute: 23 * 60, EndMinute: 60, Nights: 7}
	plugs, err := store.GetPlugs(ctx, 0.30, wrapping)
	if err != nil {
		t.Fatalf("GetPlugs failed: %v", err)
	}
	if !plugs[0].HasStandbyData || plugs[0].StandbyWatts != 3 {
		t.Errorf("23:00-01:00 window: expected 3W from the 23:30 reading, got has=%v watts=%v", plugs[0].HasStandbyData, plugs[0].StandbyWatts)
	}

	daytime := models.StandbyPeriod{StartMinute: 13 * 60, EndMinute: 14 * 60, Nights: 7}
	plugs, err = store.GetPlugs(ctx, 0.30, daytime)
	if err != nil {
		t.Fatalf("GetPlugs failed: %v", err)
	}
	if plugs[0].HasStandbyData {
		t.Errorf("13:00-14:00 window: expected no data, got %v W", plugs[0].StandbyWatts)
	}
}

func TestStandbyAveragesWithinSelectedNights(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertPlug(ctx, models.Plug{
		ID: "p1", Name: "p1", Model: "P115", DriverType: "mock", IP: "192.168.1.100", IsActive: true, IsOn: true,
	}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}

	today := startOfLocalDay(time.Now())
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today.Add(3 * time.Hour), PowerWatts: 4})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today.AddDate(0, 0, -3).Add(3 * time.Hour), PowerWatts: 6})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today.AddDate(0, 0, -8).Add(3 * time.Hour), PowerWatts: 100})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	window := models.StandbyPeriod{StartMinute: 120, EndMinute: 300}

	window.Nights = 7
	plugs, err := store.GetPlugs(ctx, 0.30, window)
	if err != nil {
		t.Fatalf("GetPlugs failed: %v", err)
	}
	if plugs[0].StandbyWatts != 5 {
		t.Errorf("last 7 nights: expected the average 5W of the two nights in range, got %v", plugs[0].StandbyWatts)
	}

	window.Nights = 1
	plugs, err = store.GetPlugs(ctx, 0.30, window)
	if err != nil {
		t.Fatalf("GetPlugs failed: %v", err)
	}
	if plugs[0].StandbyWatts != 4 {
		t.Errorf("last night: expected 4W from last night only, got %v", plugs[0].StandbyWatts)
	}
}
