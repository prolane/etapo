package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

const plugMAC = "60-15-6F-00-00-01"

func TestPlugMACAndAddressRoundTrip(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertPlug(ctx, models.Plug{ID: "office", Name: "Office", Model: "P115", DriverType: "tapo", IP: "192.168.1.103", IsActive: true}); err != nil {
		t.Fatalf("failed to upsert plug: %v", err)
	}

	macs, err := store.PlugMACs(ctx)
	if err != nil {
		t.Fatalf("PlugMACs failed: %v", err)
	}
	if len(macs) != 0 {
		t.Fatalf("expected no MAC addresses before any is learned, got %v", macs)
	}

	if err := store.SetPlugMAC(ctx, "office", plugMAC); err != nil {
		t.Fatalf("SetPlugMAC failed: %v", err)
	}
	macs, err = store.PlugMACs(ctx)
	if err != nil {
		t.Fatalf("PlugMACs failed: %v", err)
	}
	if macs["office"] != plugMAC {
		t.Errorf("expected MAC %s, got %v", plugMAC, macs)
	}

	// Startup upserts the configured address and must keep the learned MAC address
	if err := store.UpsertPlug(ctx, models.Plug{ID: "office", Name: "Office", Model: "P115", DriverType: "tapo", IP: "192.168.1.103", IsActive: true}); err != nil {
		t.Fatalf("failed to upsert plug again: %v", err)
	}
	macs, _ = store.PlugMACs(ctx)
	if macs["office"] != plugMAC {
		t.Errorf("expected the MAC address to survive an upsert, got %v", macs)
	}

	if err := store.UpdatePlugIP(ctx, "office", "192.168.1.120"); err != nil {
		t.Fatalf("UpdatePlugIP failed: %v", err)
	}
	ip, exists, err := store.PlugIP(ctx, "office")
	if err != nil || !exists || ip != "192.168.1.120" {
		t.Errorf("expected PlugIP to return the moved address, got %q exists=%v err=%v", ip, exists, err)
	}

	if _, exists, err := store.PlugIP(ctx, "missing"); err != nil || exists {
		t.Errorf("expected no plug for an unknown id, exists=%v err=%v", exists, err)
	}
}

func TestSummaryLeavesOutOfflinePlugsOutOfLivePower(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second, 10)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	for _, id := range []string{"online", "offline"} {
		if err := store.UpsertPlug(ctx, models.Plug{ID: id, Name: id, Model: "P115", DriverType: "tapo", IP: "192.168.1.103", IsActive: true, IsOn: true}); err != nil {
			t.Fatalf("failed to upsert plug %s: %v", id, err)
		}
	}
	store.RecordReading(models.EnergyReading{PlugID: "online", Timestamp: time.Now().UTC(), PowerWatts: 100})
	store.RecordReading(models.EnergyReading{PlugID: "offline", Timestamp: time.Now().Add(-10 * time.Minute).UTC(), PowerWatts: 50})
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	summary, err := store.GetSummary(ctx, "EUR", 0.30, testStandby, time.Minute)
	if err != nil {
		t.Fatalf("GetSummary failed: %v", err)
	}
	if summary.TotalPowerWatts != 100 {
		t.Errorf("expected only the online plug's 100 W in live power, got %v", summary.TotalPowerWatts)
	}
}
