package storage

import (
	"context"
	"database/sql"
	"math"
	"time"

	"etapo/internal/models"
)

// GetMonthStats returns each plug's peak power and energy for the local calendar month containing start.
func (s *Storage) GetMonthStats(ctx context.Context, start time.Time, costPerKWh float64) ([]models.MonthStats, error) {
	monthStart := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.Local)
	monthEnd := monthStart.AddDate(0, 1, 0)

	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, MAX(r.power_watts), MAX(r.energy_month_kwh)
		FROM plugs p
		LEFT JOIN energy_readings r ON r.plug_id = p.id AND r.timestamp >= ? AND r.timestamp < ?
		GROUP BY p.id
		ORDER BY p.name ASC
	`, monthStart.UTC(), monthEnd.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.MonthStats
	for rows.Next() {
		var id string
		var peak, energy sql.NullFloat64
		if err := rows.Scan(&id, &peak, &energy); err != nil {
			return nil, err
		}
		st := models.MonthStats{PlugID: id}
		st.PeakWatts = math.Round(peak.Float64*10) / 10
		st.EnergyKWh = math.Round(energy.Float64*100) / 100
		st.EstimatedCost = math.Round((st.EnergyKWh*costPerKWh)*100) / 100
		result = append(result, st)
	}
	return result, rows.Err()
}
