package storage

import (
	"context"
	"database/sql"
	"math"
	"time"

	"etapo/internal/models"
)

// GetDayStats returns each plug's peak power and energy for the local day containing start.
func (s *Storage) GetDayStats(ctx context.Context, start time.Time, costPerKWh float64) ([]models.PeriodStats, error) {
	dayStart := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	return s.periodStats(ctx, dayStart, dayStart.AddDate(0, 0, 1), "energy_today_kwh", costPerKWh)
}

// GetMonthStats returns each plug's peak power and energy for the local calendar month containing start.
func (s *Storage) GetMonthStats(ctx context.Context, start time.Time, costPerKWh float64) ([]models.PeriodStats, error) {
	monthStart := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.Local)
	return s.periodStats(ctx, monthStart, monthStart.AddDate(0, 1, 0), "energy_month_kwh", costPerKWh)
}

// periodStats aggregates readings from start up to end. energyColumn is the device's running
// counter that resets at the start of the period, so its maximum is the energy used in it.
// It must be one of the energy column names in the schema, never user input.
func (s *Storage) periodStats(ctx context.Context, start, end time.Time, energyColumn string, costPerKWh float64) ([]models.PeriodStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, MAX(r.power_watts), MAX(r.`+energyColumn+`)
		FROM plugs p
		LEFT JOIN energy_readings r ON r.plug_id = p.id AND r.timestamp >= ? AND r.timestamp < ?
		GROUP BY p.id
		ORDER BY p.name ASC
	`, start.UTC(), end.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.PeriodStats
	for rows.Next() {
		var id string
		var peak, energy sql.NullFloat64
		if err := rows.Scan(&id, &peak, &energy); err != nil {
			return nil, err
		}
		st := models.PeriodStats{PlugID: id}
		st.PeakWatts = math.Round(peak.Float64*10) / 10
		st.EnergyKWh = math.Round(energy.Float64*100) / 100
		st.EstimatedCost = math.Round((st.EnergyKWh*costPerKWh)*100) / 100
		result = append(result, st)
	}
	return result, rows.Err()
}
