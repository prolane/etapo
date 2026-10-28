package mock

import (
	"context"
	"regexp"
	"testing"
	"time"
)

func TestMockDriver(t *testing.T) {
	ctx := context.Background()
	driver := NewDriver("plug-1", "Test TV", "P115", ProfileTV)

	info, err := driver.GetInfo(ctx)
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	if !info.IsOn {
		t.Errorf("expected plug to be ON initially")
	}

	usage, err := driver.GetEnergyUsage(ctx)
	if err != nil {
		t.Fatalf("GetEnergyUsage failed: %v", err)
	}
	if usage.PowerWatts <= 0 {
		t.Errorf("expected positive power draw for TV profile, got %f", usage.PowerWatts)
	}
	if usage.VoltageV < 220 || usage.VoltageV > 240 {
		t.Errorf("unexpected voltage: %f", usage.VoltageV)
	}

	// Turn off
	if err := driver.SetPower(ctx, false); err != nil {
		t.Fatalf("SetPower(false) failed: %v", err)
	}
	infoOff, _ := driver.GetInfo(ctx)
	if infoOff.IsOn {
		t.Errorf("expected plug to be OFF")
	}

	usageOff, _ := driver.GetEnergyUsage(ctx)
	if usageOff.PowerWatts > 2.0 {
		t.Errorf("expected standby draw < 2W when OFF, got %f", usageOff.PowerWatts)
	}
}

func TestMockMACMatchesDeviceFormat(t *testing.T) {
	mac := regexp.MustCompile(`^60-15-6F-[0-9A-F]{2}-[0-9A-F]{2}-[0-9A-F]{2}$`)
	for _, id := range []string{"a", "office-network", "living-room-tv-corner"} {
		info, err := NewDriver(id, "x", "P115", ProfileDefault).GetInfo(context.Background())
		if err != nil {
			t.Fatalf("GetInfo failed: %v", err)
		}
		if !mac.MatchString(info.MAC) {
			t.Errorf("MAC for %q does not match the device format: %q", id, info.MAC)
		}
	}
}

func TestMockCountersResetLikeDevice(t *testing.T) {
	ctx := context.Background()
	// Switched off, so the time since the last poll adds only standby energy (about 1 W)
	driver := NewDriver("plug-1", "Test", "P115", ProfileDefault)
	driver.isOn = false

	// Last poll was yesterday: today's counter must restart
	driver.energyToday = 5
	driver.lastPoll = time.Now().AddDate(0, 0, -1)
	usage, _ := driver.GetEnergyUsage(ctx)
	if usage.EnergyTodayKWh > 1 {
		t.Errorf("expected today's energy to restart at midnight, got %v kWh", usage.EnergyTodayKWh)
	}

	// Last poll was last month: the monthly counter must restart too
	driver.energyMonth = 50
	driver.lastPoll = time.Now().AddDate(0, 0, -40)
	usage, _ = driver.GetEnergyUsage(ctx)
	if usage.EnergyMonthKWh > 5 {
		t.Errorf("expected monthly energy to restart on the 1st, got %v kWh", usage.EnergyMonthKWh)
	}
}
