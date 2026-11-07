package storage

import (
	"context"
	"math"
	"time"

	"etapo/internal/models"
)

// GetDayStats returns each plug's peak power and energy for the local day containing start.
func (s *Storage) GetDayStats(ctx context.Context, start time.Time, costPerKWh float64) ([]models.PeriodStats, error) {
	dayStart := startOfLocalDay(start)
	return s.periodStats(ctx, dayStart, dayStart.AddDate(0, 0, 1), false, costPerKWh)
}

// GetMonthStats returns each plug's peak power and energy for the local calendar month containing start.
func (s *Storage) GetMonthStats(ctx context.Context, start time.Time, costPerKWh float64) ([]models.PeriodStats, error) {
	monthStart := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.Local)
	return s.periodStats(ctx, monthStart, monthStart.AddDate(0, 1, 0), true, costPerKWh)
}

// periodStats reads the daily rows from start up to end. The device counters reset at the start of each day
// and month, so the energy of a day is its last counter value, and the energy of a month is the last counter
// value of its last day.
func (s *Storage) periodStats(ctx context.Context, start, end time.Time, monthly bool, costPerKWh float64) ([]models.PeriodStats, error) {
	plugs, err := s.plugRows(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]models.PeriodStats, 0, len(plugs))
	for _, p := range plugs {
		days, err := s.dailyRange(ctx, p.ID, start.Format(dayLayout), end.Format(dayLayout))
		if err != nil {
			return nil, err
		}
		var peak, energy int64
		for _, d := range days {
			if d.peakMW > peak {
				peak = d.peakMW
			}
			energy = d.energyToday
			if monthly {
				energy = d.energyMonth
			}
		}
		st := models.PeriodStats{PlugID: p.ID}
		st.PeakWatts = math.Round(float64(peak)/1000*10) / 10
		st.EnergyKWh = math.Round(float64(energy)/1000*100) / 100
		st.EstimatedCost = math.Round((st.EnergyKWh*costPerKWh)*100) / 100
		result = append(result, st)
	}
	return result, nil
}
