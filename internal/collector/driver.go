package collector

import (
	"context"

	"etapo/internal/models"
)

// DeviceDriver defines the contract for communicating with a smart plug (hardware or mock).
type DeviceDriver interface {
	// GetInfo returns current device metadata and on/off status.
	GetInfo(ctx context.Context) (*models.DeviceInfo, error)

	// GetEnergyUsage returns current real-time telemetry (Watts, Volts, mA, kWh).
	GetEnergyUsage(ctx context.Context) (*models.EnergyUsage, error)

	// SetPower turns the socket relay on or off.
	SetPower(ctx context.Context, on bool) error
}
