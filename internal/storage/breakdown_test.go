package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func newBreakdownStore(t *testing.T) *Storage {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second, 10)
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

func assertBucket(t *testing.T, data []*float64, i int, want float64) {
	t.Helper()
	if data[i] == nil {
		t.Errorf("expected %v in bucket %d, got no data", want, i)
		return
	}
	if *data[i] != want {
		t.Errorf("expected %v in bucket %d, got %v", want, i, *data[i])
	}
}

func TestGetBreakdownTodayBucketsByLocalHour(t *testing.T) {
	store := newBreakdownStore(t)
	ctx := context.Background()

	midnight := startOfLocalDay(time.Now())
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: midnight.Add(30 * time.Minute), PowerWatts: 7})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: midnight.Add(1*time.Hour + 10*time.Minute), PowerWatts: 10})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: midnight.Add(2*time.Hour + 10*time.Minute), PowerWatts: 20})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	resp, err := store.GetBreakdown(ctx, "today", "plug", midnight, 0.30)
	if err != nil {
		t.Fatalf("GetBreakdown failed: %v", err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(resp.Series))
	}

	data := resp.Series[0].Data
	assertBucket(t, data, 0, 7)
	assertBucket(t, data, 1, 10)
	assertBucket(t, data, 2, 20)
}

func TestGetBreakdownSelectedDayOnlyUsesThatDay(t *testing.T) {
	store := newBreakdownStore(t)
	ctx := context.Background()

	today := startOfLocalDay(time.Now())
	yesterday := today.AddDate(0, 0, -1)
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: yesterday.Add(5*time.Hour + 10*time.Minute), PowerWatts: 10})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today.Add(30 * time.Minute), PowerWatts: 7})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: today.Add(1*time.Hour + 10*time.Minute), PowerWatts: 20})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	resp, err := store.GetBreakdown(ctx, "today", "plug", yesterday, 0.30)
	if err != nil {
		t.Fatalf("GetBreakdown failed: %v", err)
	}

	data := resp.Series[0].Data
	assertBucket(t, data, 5, 10)
	if data[0] != nil || data[1] != nil {
		t.Errorf("expected no readings from today in yesterday's view, got 00:00=%v 01:00=%v", data[0], data[1])
	}
	if now := time.Now().Hour(); now != 5 && data[now] != nil {
		t.Errorf("expected no current-hour fill for a past day, got %v at %02d:00", *data[now], now)
	}
}

func TestGetBreakdownSelectedMonthUsesThatMonth(t *testing.T) {
	store := newBreakdownStore(t)
	ctx := context.Background()

	now := time.Now()
	prevStart := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.Local)
	daysInPrev := time.Date(prevStart.Year(), prevStart.Month()+1, 0, 0, 0, 0, 0, time.Local).Day()

	store.RecordReading(models.EnergyReading{
		PlugID:         "p1",
		Timestamp:      time.Date(prevStart.Year(), prevStart.Month(), 15, 12, 0, 0, 0, time.Local),
		PowerWatts:     10,
		EnergyTodayKWh: 2.5,
	})
	store.RecordReading(models.EnergyReading{PlugID: "p1", Timestamp: now, PowerWatts: 20, EnergyTodayKWh: 9})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	resp, err := store.GetBreakdown(ctx, "month", "plug", prevStart, 0.30)
	if err != nil {
		t.Fatalf("GetBreakdown failed: %v", err)
	}
	if len(resp.Labels) != daysInPrev {
		t.Fatalf("expected %d day labels for a past month, got %d", daysInPrev, len(resp.Labels))
	}

	data := resp.Series[0].Data
	assertBucket(t, data, 14, 2.5)
	if data[daysInPrev-1] != nil {
		t.Errorf("expected no current-day fill for a past month, got %v on the last day", *data[daysInPrev-1])
	}
}
