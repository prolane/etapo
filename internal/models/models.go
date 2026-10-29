package models

import "time"

// Plug represents a registered Tapo or mock smart plug.
type Plug struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Model      string    `json:"model"`       // e.g. "P115", "P110"
	Category   string    `json:"category"`    // e.g. "Kitchen", "Office", "Entertainment", "Utility"
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

// PlugWithLatest combines plug metadata, latest reading, and homeowner analytics.
type PlugWithLatest struct {
	Plug
	LatestReading      *EnergyReading `json:"latest_reading,omitempty"`
	PeakWattsToday     float64        `json:"peak_watts_today"`     // Peak power recorded today (Max W)
	AvgWattsToday      float64        `json:"avg_watts_today"`      // Average power consumption today
	RuntimeTodayHours  float64        `json:"runtime_today_hours"`  // Hours active (> threshold) today
	StandbyWatts       float64        `json:"standby_watts"`        // Baseline vampire/standby load (02:00-05:00 AM)
	HasStandbyData     bool           `json:"has_standby_data"`     // Readings exist in the 02:00-05:00 window
	AnnualStandbyCost  float64        `json:"annual_standby_cost"`  // Projected cost of standby per year (€/yr)
	EstimatedMonthCost float64        `json:"estimated_month_cost"` // Projected monthly electricity cost (€/mo)
}

// PeriodStats is one plug's peak power and energy for a day or a calendar month.
type PeriodStats struct {
	PlugID        string  `json:"plug_id"`
	PeakWatts     float64 `json:"peak_watts"`
	EnergyKWh     float64 `json:"energy_kwh"`
	EstimatedCost float64 `json:"estimated_cost"`
}

// StandbyPeriod selects the readings used for the standby baseline.
type StandbyPeriod struct {
	StartMinute int // Minutes after local midnight when the window starts
	EndMinute   int // Minutes after local midnight when the window ends; earlier than StartMinute wraps past midnight
	Nights      int // Number of nights to average, counting back from the most recent night (1 = last night)
}

// SystemSummary provides top-level metrics across all sockets for the top KPI bar.
type SystemSummary struct {
	TotalPowerWatts      float64   `json:"total_power_watts"`       // Instantaneous total draw in Watts
	LiveBurnRatePerHour  float64   `json:"live_burn_rate_per_hour"` // Live burn rate (€/hour or $/hour)
	TotalEnergyToday     float64   `json:"total_energy_today_kwh"`  // Cumulative consumption today (kWh)
	TotalCostToday       float64   `json:"total_cost_today"`        // Total cost accrued today (€)
	TotalEnergyMonth     float64   `json:"total_energy_month_kwh"`  // Cumulative consumption this month (kWh)
	EstimatedCostMonth   float64   `json:"estimated_cost_month"`    // Projected monthly cost (€)
	TotalStandbyWatts    float64   `json:"total_standby_watts"`     // Combined baseline standby load (W)
	StandbyDataAvailable bool      `json:"standby_data_available"`  // Any socket has readings in the standby window
	StandbyWindowStart   string    `json:"standby_window_start"`    // Standby window start, HH:MM local time
	StandbyWindowEnd     string    `json:"standby_window_end"`      // Standby window end, HH:MM local time
	AnnualStandbyWaste   float64   `json:"annual_standby_waste"`    // Total household cost of standby per year (€)
	Currency             string    `json:"currency"`
	ActivePlugsCount     int       `json:"active_plugs_count"`
	TotalPlugsCount      int       `json:"total_plugs_count"`
	LastUpdated          time.Time `json:"last_updated"`
}

// HistoryPoint is the average power of one time bucket. PowerWatts is nil when the bucket has no readings.
type HistoryPoint struct {
	Timestamp  time.Time `json:"timestamp"`
	PowerWatts *float64  `json:"power_watts"`
}

// BreakdownSeries represents one series in a stacked chart (e.g. one plug or category).
type BreakdownSeries struct {
	ID    string     `json:"id"`
	Label string     `json:"label"`
	Color string     `json:"color,omitempty"`
	Data  []*float64 `json:"data"` // nil where the period has no readings
}

// BreakdownResponse contains formatted data for stacked bar/area consumption charts.
type BreakdownResponse struct {
	Range  string            `json:"range"`  // "today" (hourly) or "month" (daily)
	Labels []string          `json:"labels"` // ["00:00", "01:00", ...] or ["Day 1", "Day 2", ...]
	Series []BreakdownSeries `json:"series"` // Series per plug or category
}
