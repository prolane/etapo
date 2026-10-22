package mock

import (
	"context"
	"testing"
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
