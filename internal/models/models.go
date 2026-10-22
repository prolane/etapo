package models

import "time"

// Plug represents a registered Tapo or mock smart plug.
type Plug struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Model      string    `json:"model"`       // e.g. "P115", "P110"
	DriverType string    `json:"driver_type"` // "mock" or "tapo"
	IP         string    `json:"ip"`
	MAC        string    `json:"mac,omitempty"`
	IsActive   bool      `json:"is_active"`
	IsOn       bool      `json:"is_on"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// EnergyReading is a point-in-time measurement from a plug.
type EnergyReading struct {
	ID             int64     `json:"id,omitempty"`
	PlugID         string    `json:"plug_id"`
	Timestamp      time.Time `json:"timestamp"`
	PowerWatts     float64   `json:"power_watts"`      // Current power consumption in Watts
	VoltageV       float64   `json:"voltage_v"`        // Voltage in Volts
	CurrentMA      float64   `json:"current_ma"`       // Current in Milliamperes
	EnergyTodayKWh float64   `json:"energy_today_kwh"` // Cumulative energy consumed today
	EnergyMonthKWh float64   `json:"energy_month_kwh"` // Cumulative energy consumed this month
}

// DeviceInfo represents the status returned by the device driver.
type DeviceInfo struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
	Model    string `json:"model"`
	MAC      string `json:"mac"`
	IsOn     bool   `json:"is_on"`
	RSSI     int    `json:"rssi,omitempty"` // Signal strength
}

// EnergyUsage represents raw energy telemetry from the device driver.
type EnergyUsage struct {
	PowerWatts     float64 `json:"power_watts"`
	VoltageV       float64 `json:"voltage_v"`
	CurrentMA      float64 `json:"current_ma"`
	EnergyTodayKWh float64 `json:"energy_today_kwh"`
	EnergyMonthKWh float64 `json:"energy_month_kwh"`
}

// PlugWithLatest combines a plug metadata with its most recent telemetry.
type PlugWithLatest struct {
	Plug
	LatestReading *EnergyReading `json:"latest_reading,omitempty"`
}

// SystemSummary provides top-level metrics across all sockets.
type SystemSummary struct {
	TotalPowerWatts  float64   `json:"total_power_watts"`
	TotalEnergyToday float64   `json:"total_energy_today_kwh"`
	TotalEnergyMonth float64   `json:"total_energy_month_kwh"`
	EstimatedCostDay float64   `json:"estimated_cost_day"`
	Currency         string    `json:"currency"`
	ActivePlugsCount int       `json:"active_plugs_count"`
	TotalPlugsCount  int       `json:"total_plugs_count"`
	LastUpdated      time.Time `json:"last_updated"`
}

// HistoryPoint is a compact data point for charts.
type HistoryPoint struct {
	Timestamp  time.Time `json:"timestamp"`
	PowerWatts float64   `json:"power_watts"`
}
