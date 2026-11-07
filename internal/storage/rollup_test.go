package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"etapo/internal/models"
)

func TestLegacyDatabaseIsBackedUpAndReplaced(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "etapo.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE plugs (id TEXT PRIMARY KEY, name TEXT NOT NULL, model TEXT NOT NULL, driver_type TEXT NOT NULL,
			ip TEXT NOT NULL, mac TEXT DEFAULT '', is_active INTEGER DEFAULT 1, is_on INTEGER DEFAULT 1,
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, category TEXT DEFAULT 'General');
		CREATE TABLE energy_readings (id INTEGER PRIMARY KEY AUTOINCREMENT, plug_id TEXT NOT NULL, timestamp DATETIME NOT NULL,
			power_watts REAL NOT NULL, voltage_v REAL NOT NULL, current_ma REAL NOT NULL,
			energy_today_kwh REAL NOT NULL, energy_month_kwh REAL NOT NULL);
		INSERT INTO energy_readings (plug_id, timestamp, power_watts, voltage_v, current_ma, energy_today_kwh, energy_month_kwh)
			VALUES ('p', '2026-11-01 10:00:00.000000 +0000 UTC', 5, 230, 20, 0.1, 1);
	`); err != nil {
		t.Fatal(err)
	}
	legacy.Close()

	store, err := New(dbPath, time.Hour)
	if err != nil {
		t.Fatalf("New failed on a legacy database: %v", err)
	}
	defer store.Close()

	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'energy_readings'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("energy_readings still exists after migration")
	}

	backups, err := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "etapo-before-rollups-*.db.bak"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one backup, got %v (err %v)", backups, err)
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(`SELECT COUNT(*) FROM energy_readings`).Scan(&n); err != nil || n != 1 {
		t.Errorf("backup should keep the raw reading, got %d (err %v)", n, err)
	}
}

func TestOpenBucketSurvivesRestartWithoutClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "etapo.db")
	first, err := New(dbPath, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := first.UpsertPlug(ctx, models.Plug{ID: "p", Name: "P", Model: "P115", DriverType: "mock", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first.RecordReading(models.EnergyReading{PlugID: "p", Timestamp: now, PowerWatts: 125.5})
	if err := first.Flush(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: the database is closed without Close, so the open bucket is never moved.
	first.db.Close()

	second, err := New(dbPath, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	start := now.Add(-time.Hour).Truncate(5 * time.Minute)
	points, err := second.GetHistory(ctx, "p", start, now.Add(time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := countReadings(points); len(got) != 1 || got[0] != 125.5 {
		t.Errorf("expected the reading from before the restart, got %v", got)
	}
}

func TestHidePlugsExceptKeepsDataOfHiddenPlugs(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := store.UpsertPlug(ctx, models.Plug{ID: id, Name: id, Model: "P115", DriverType: "mock", IsActive: true}); err != nil {
			t.Fatal(err)
		}
		store.RecordReading(models.EnergyReading{PlugID: id, Timestamp: time.Now(), PowerWatts: 1})
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := store.HidePlugsExcept(ctx, []string{"a"}); err != nil {
		t.Fatal(err)
	}

	plugs, err := store.GetPlugs(ctx, 0.3, testStandby)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugs) != 1 || plugs[0].ID != "a" {
		t.Errorf("expected only plug a, got %+v", plugs)
	}
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM plugs WHERE id = 'b'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("hidden plug should stay in the database, got %d (err %v)", n, err)
	}
}

func TestRelayStateIsStoredImmediately(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertPlug(ctx, models.Plug{ID: "p", Name: "P", Model: "P115", DriverType: "mock", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPlugOn(ctx, "p", false); err != nil {
		t.Fatal(err)
	}
	plugs, err := store.GetPlugs(ctx, 0.3, testStandby)
	if err != nil {
		t.Fatal(err)
	}
	if plugs[0].IsOn {
		t.Error("expected the plug to be off right after SetPlugOn, without a flush")
	}
}

func TestSummaryCountsEnergyOfSwitchedOffPlugs(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "test.db"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertPlug(ctx, models.Plug{ID: "p", Name: "P", Model: "P115", DriverType: "mock", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	store.RecordReading(models.EnergyReading{PlugID: "p", Timestamp: time.Now(), PowerWatts: 0, EnergyTodayKWh: 1.5})
	store.RecordPlugState("p", false)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	summary, err := store.GetSummary(ctx, "EUR", 0.3, testStandby, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalEnergyToday != 1.5 {
		t.Errorf("energy used before the plug was switched off should count, got %v", summary.TotalEnergyToday)
	}
}

func TestStandbyWindowsAreCompletedAndOrdered(t *testing.T) {
	period := models.StandbyPeriod{StartMinute: 120, EndMinute: 300, Nights: 3}
	wins := standbyWindows(time.Date(2026, 11, 8, 3, 0, 0, 0, time.Local), period)
	if len(wins) != 3 {
		t.Fatalf("expected 3 completed windows, got %d", len(wins))
	}
	for i := 1; i < len(wins); i++ {
		if wins[i].start <= wins[i-1].end {
			t.Errorf("windows must be ascending and not overlap: %v", wins)
		}
	}
}
