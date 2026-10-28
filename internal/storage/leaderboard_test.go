package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func TestPeakWattsMonthIgnoresPreviousMonth(t *testing.T) {
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
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: prevMonth, PowerWatts: 900})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: now, PowerWatts: 50})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	plugs, err := store.GetPlugs(ctx, 0.30)
	if err != nil {
		t.Fatalf("GetPlugs failed: %v", err)
	}
	if len(plugs) != 1 {
		t.Fatalf("expected 1 plug, got %d", len(plugs))
	}
	if plugs[0].PeakWattsMonth != 50 {
		t.Errorf("expected month peak 50W (previous month's 900W excluded), got %v", plugs[0].PeakWattsMonth)
	}
}
