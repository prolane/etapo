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
	store, err := New(filepath.Join(t.TempDir(), "etapo.db"), time.Hour)
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

// storedBucketCount counts the closed five-minute buckets in the database.
func storedBucketCount(t *testing.T, store *Storage) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM samples_5m`).Scan(&n); err != nil {
		t.Fatalf("count failed: %v", err)
	}
	return n
}

func TestPurgeBeforeDeletesOnlyOlderBuckets(t *testing.T) {
	store := newRetentionTestStore(t)
	now := time.Now().UTC()
	cutoff := now.Add(-90 * 24 * time.Hour).Truncate(bucketSec * time.Second)

	recordReadingsAt(t, store,
		cutoff.Add(-time.Hour),
		cutoff.Add(-time.Second),
		cutoff,
		cutoff.Add(time.Hour),
		now,
	)

	deleted, err := store.PurgeBefore(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("purge failed: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	// The newest reading is still in the bucket that is filling, so only the closed buckets are counted.
	if got := storedBucketCount(t, store); got != 2 {
		t.Errorf("remaining closed buckets = %d, want 2", got)
	}
}

func TestPurgeBeforeDeletesManyBuckets(t *testing.T) {
	store := newRetentionTestStore(t)
	now := time.Now().UTC()

	old := 300
	times := make([]time.Time, 0, old+1)
	for i := range old {
		times = append(times, now.Add(-48*time.Hour).Add(time.Duration(i)*bucketSec*time.Second))
	}
	times = append(times, now)
	recordReadingsAt(t, store, times...)

	deleted, err := store.PurgeBefore(context.Background(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("purge failed: %v", err)
	}
	if deleted < int64(old-1) {
		t.Errorf("deleted = %d, want at least %d", deleted, old-1)
	}
	if got := storedBucketCount(t, store); got != 0 {
		t.Errorf("remaining closed buckets = %d, want 0", got)
	}
}

func TestPurgeBeforeStopsWhenCancelled(t *testing.T) {
	store := newRetentionTestStore(t)
	now := time.Now().UTC()
	recordReadingsAt(t, store, now.Add(-2*time.Hour), now)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.PurgeBefore(ctx, now.Add(-time.Hour)); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
}

func TestStartRetentionPurgesOnStartAndCloseStopsIt(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "etapo.db"), time.Hour)
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
	for storedBucketCount(t, store) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("startup purge did not delete the old bucket")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}
