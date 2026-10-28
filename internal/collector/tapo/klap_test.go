package tapo

import (
	"math"
	"testing"
)

func approx(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestParseEnergyUsageUnits(t *testing.T) {
	res := map[string]interface{}{
		"current_power": 5000.0,
		"voltage":       230500.0,
		"today_energy":  50.0,
		"month_energy":  12300.0,
	}

	usage := parseEnergyUsage(res)

	if !approx(usage.PowerWatts, 5.0) {
		t.Errorf("expected 5 W from 5000 mW, got %f", usage.PowerWatts)
	}
	if !approx(usage.VoltageV, 230.5) {
		t.Errorf("expected 230.5 V from 230500 mV, got %f", usage.VoltageV)
	}
	if !approx(usage.EnergyTodayKWh, 0.05) {
		t.Errorf("expected 0.05 kWh from 50 Wh, got %f", usage.EnergyTodayKWh)
	}
	if !approx(usage.EnergyMonthKWh, 12.3) {
		t.Errorf("expected 12.3 kWh from 12300 Wh, got %f", usage.EnergyMonthKWh)
	}
}

func TestDecodeNickname(t *testing.T) {
	// Encoded by a real P115 for the nickname "Kantoor netwerk"
	if got := decodeNickname("S2FudG9vciBuZXR3ZXJr"); got != "Kantoor netwerk" {
		t.Errorf("expected decoded nickname, got %q", got)
	}
	if got := decodeNickname("Plain name"); got != "Plain name" {
		t.Errorf("expected invalid base64 to be returned unchanged, got %q", got)
	}
}
