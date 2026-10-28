package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func TestGetBreakdownTodayBucketsByLocalHour(t *testing.T) {
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

	midnight := startOfLocalDay(time.Now())
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: midnight.Add(1*time.Hour + 10*time.Minute), PowerWatts: 10})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: midnight.Add(2*time.Hour + 10*time.Minute), PowerWatts: 20})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	resp, err := store.GetBreakdown(ctx, "today", "plug", 0.30)
	if err != nil {
		t.Fatalf("GetBreakdown failed: %v", err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Series))
	}

	data := resp.Series[0].Data
	if data[1] != 10 {
		t.Errorf("expected 10W in the 01:00 local bucket, got %v", data[1])
	}
	if data[2] != 20 {
		t.Errorf("expected 20W in the 02:00 local bucket, got %v", data[2])
	}
}
