package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func newRetentionTestStore(t *testing.T) *Storage {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "etapo.db"), time.Hour, 100000)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.UpsertPlug(context.Background(), models.Plug{
		ID: "plug-1", Name: "Socket", Model: "P115", DriverType: "mock", IP: "10.0.0.1", IsActive: true, IsOn: true,
	}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}
	return store
}

func recordReadingsAt(t *testing.T, store *Storage, times ...time.Time) {
	t.Helper()
	for _, ts := range times {
		store.RecordReading(models.EnergyReading{PlugID: "plug-1", Timestamp: ts, PowerWatts: 10})
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
}

func storedReadingCount(t *testing.T, store *Storage) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM energy_readings`).Scan(&n); err != nil {
		t.Fatalf("count failed: %v", err)
	}
	return n
}

func TestPurgeReadingsBeforeDeletesOnlyOlderReadings(t *testing.T) {
	store := newRetentionTestStore(t)
	now := time.Now().UTC()
	cutoff := now.Add(-90 * 24 * time.Hour)

	recordReadingsAt(t, store,
		cutoff.Add(-time.Hour),
		cutoff.Add(-time.Second),
		cutoff,
		cutoff.Add(time.Hour),
		now,
	)

	deleted, err := store.PurgeReadingsBefore(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("purge failed: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	if got := storedReadingCount(t, store); got != 3 {
		t.Errorf("remaining readings = %d, want 3", got)
	}
}

func TestPurgeReadingsBeforeDeletesAcrossBatches(t *testing.T) {
	store := newRetentionTestStore(t)
	now := time.Now().UTC()
	cutoff := now.Add(-time.Hour)

	old := retentionBatchSize*2 + 7
	times := make([]time.Time, 0, old+1)
	for i := range old {
		times = append(times, now.Add(-2*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	times = append(times, now)
	recordReadingsAt(t, store, times...)

	deleted, err := store.PurgeReadingsBefore(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("purge failed: %v", err)
	}
	if deleted != int64(old) {
		t.Errorf("deleted = %d, want %d", deleted, old)
	}
	if got := storedReadingCount(t, store); got != 1 {
		t.Errorf("remaining readings = %d, want 1", got)
	}
}

func TestPurgeReadingsBeforeStopsWhenCancelled(t *testing.T) {
	store := newRetentionTestStore(t)
	now := time.Now().UTC()

	times := make([]time.Time, 0, retentionBatchSize*2)
	for i := range retentionBatchSize * 2 {
		times = append(times, now.Add(-2*time.Hour).Add(time.Duration(i)*time.Millisecond))
	}
	recordReadingsAt(t, store, times...)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.PurgeReadingsBefore(ctx, now.Add(-time.Hour))
	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if got := storedReadingCount(t, store); got == 0 {
		t.Error("cancelled purge deleted everything, want it to stop after the current batch")
	}
}

func TestStartRetentionPurgesOnStartAndCloseStopsIt(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "etapo.db"), time.Hour, 100000)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	if err := store.UpsertPlug(context.Background(), models.Plug{
		ID: "plug-1", Name: "Socket", Model: "P115", DriverType: "mock", IP: "10.0.0.1", IsActive: true, IsOn: true,
	}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}
	now := time.Now().UTC()
	recordReadingsAt(t, store, now.Add(-48*time.Hour), now)

	store.StartRetention(24 * time.Hour)

	deadline := time.Now().Add(5 * time.Second)
	for storedReadingCount(t, store) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("startup purge did not delete the old reading")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}
