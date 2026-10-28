package storage

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"etapo/internal/models"
)

// Storage handles persistence of plugs and energy readings in SQLite.
type Storage struct {
	db            *sql.DB
	bufferMu      sync.Mutex
	buffer        []models.EnergyReading
	maxBufferSize int
	flushInterval time.Duration
	stopFlush     chan struct{}
	flushDone     chan struct{}
}

// New creates and initializes SQLite storage with WAL mode and in-memory batching.
func New(dbPath string, flushInterval time.Duration, maxBufferSize int) (*Storage, error) {
	if maxBufferSize <= 0 {
		maxBufferSize = 100
	}
	if flushInterval <= 0 {
		flushInterval = 30 * time.Second
	}

	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory %q: %w", dir, err)
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// Pool settings: In WAL mode, SQLite allows concurrent readers and 1 writer
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)

	s := &Storage{
		db:            db,
		buffer:        make([]models.EnergyReading, 0, maxBufferSize),
		maxBufferSize: maxBufferSize,
		flushInterval: flushInterval,
		stopFlush:     make(chan struct{}),
		flushDone:     make(chan struct{}),
	}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema migration failed: %w", err)
	}

	go s.flushLoop()

	return s, nil
}

func (s *Storage) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS plugs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		model TEXT NOT NULL,
		category TEXT DEFAULT 'General',
		driver_type TEXT NOT NULL,
		ip TEXT NOT NULL,
		mac TEXT DEFAULT '',
		is_active INTEGER DEFAULT 1,
		is_on INTEGER DEFAULT 1,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS energy_readings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		plug_id TEXT NOT NULL,
		timestamp DATETIME NOT NULL,
		power_watts REAL NOT NULL,
		voltage_v REAL NOT NULL,
		current_ma REAL NOT NULL,
		energy_today_kwh REAL NOT NULL,
		energy_month_kwh REAL NOT NULL,
		FOREIGN KEY(plug_id) REFERENCES plugs(id)
	);

	CREATE INDEX IF NOT EXISTS idx_readings_plug_ts ON energy_readings(plug_id, timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_readings_ts ON energy_readings(timestamp DESC);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Migration for existing databases that might not have category column yet
	_, _ = s.db.Exec(`ALTER TABLE plugs ADD COLUMN category TEXT DEFAULT 'General';`)

	return nil
}

// RecordReading appends an energy reading to the in-memory write buffer.
// If the buffer reaches maxBufferSize, it triggers an immediate synchronous flush.
func (s *Storage) RecordReading(r models.EnergyReading) {
	s.bufferMu.Lock()
	s.buffer = append(s.buffer, r)
	shouldFlush := len(s.buffer) >= s.maxBufferSize
	s.bufferMu.Unlock()

	if shouldFlush {
		s.Flush()
	}
}

// Flush writes all pending buffered readings to SQLite in a single transaction.
func (s *Storage) Flush() error {
	s.bufferMu.Lock()
	if len(s.buffer) == 0 {
		s.bufferMu.Unlock()
		return nil
	}
	toWrite := s.buffer
	s.buffer = make([]models.EnergyReading, 0, s.maxBufferSize)
	s.bufferMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin flush transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO energy_readings (plug_id, timestamp, power_watts, voltage_v, current_ma, energy_today_kwh, energy_month_kwh)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer stmt.Close()

	for _, r := range toWrite {
		_, err := stmt.Exec(
			r.PlugID,
			r.Timestamp.UTC(),
			r.PowerWatts,
			r.VoltageV,
			r.CurrentMA,
			r.EnergyTodayKWh,
			r.EnergyMonthKWh,
		)
		if err != nil {
			return fmt.Errorf("failed to execute batch insert: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Storage) flushLoop() {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	defer close(s.flushDone)

	for {
		select {
		case <-ticker.C:
			_ = s.Flush()
		case <-s.stopFlush:
			_ = s.Flush()
			return
		}
	}
}

// UpsertPlug inserts or updates plug metadata.
func (s *Storage) UpsertPlug(ctx context.Context, p models.Plug) error {
	if p.Category == "" {
		p.Category = "General"
	}

	query := `
	INSERT INTO plugs (id, name, model, category, driver_type, ip, mac, is_active, is_on, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		model = excluded.model,
		category = excluded.category,
		driver_type = excluded.driver_type,
		ip = excluded.ip,
		is_active = excluded.is_active,
		updated_at = excluded.updated_at
	`
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, query,
		p.ID, p.Name, p.Model, p.Category, p.DriverType, p.IP, p.MAC, p.IsActive, p.IsOn, p.CreatedAt, p.UpdatedAt,
	)
	return err
}

// UpdatePlugState updates the on/off state of a plug.
func (s *Storage) UpdatePlugState(ctx context.Context, id string, isOn bool) error {
	query := `UPDATE plugs SET is_on = ?, updated_at = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, isOn, time.Now().UTC(), id)
	return err
}

// GetPlugs returns all configured plugs enriched with latest reading and homeowner analytics.
func (s *Storage) GetPlugs(ctx context.Context, costPerKWh float64) ([]models.PlugWithLatest, error) {
	query := `
		SELECT 
			p.id, p.name, p.model, p.category, p.driver_type, p.ip, p.mac, p.is_active, p.is_on, p.created_at, p.updated_at,
			r.id, r.timestamp, r.power_watts, r.voltage_v, r.current_ma, r.energy_today_kwh, r.energy_month_kwh
		FROM plugs p
		LEFT JOIN energy_readings r ON r.id = (
			SELECT id FROM energy_readings 
			WHERE plug_id = p.id 
			ORDER BY timestamp DESC 
			LIMIT 1
		)
		ORDER BY p.name ASC
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.PlugWithLatest
	midnightToday := startOfLocalDay(time.Now())
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)

	for rows.Next() {
		var p models.PlugWithLatest
		var mac, category sql.NullString
		var rID sql.NullInt64
		var rTimestamp sql.NullTime
		var rPower, rVoltage, rCurrent, rToday, rMonth sql.NullFloat64

		err := rows.Scan(
			&p.ID, &p.Name, &p.Model, &category, &p.DriverType, &p.IP, &mac,
			&p.IsActive, &p.IsOn, &p.CreatedAt, &p.UpdatedAt,
			&rID, &rTimestamp, &rPower, &rVoltage, &rCurrent, &rToday, &rMonth,
		)
		if err != nil {
			return nil, err
		}
		p.MAC = mac.String
		p.Category = category.String
		if p.Category == "" {
			p.Category = "General"
		}

		if rID.Valid {
			p.LatestReading = &models.EnergyReading{
				ID:             rID.Int64,
				PlugID:         p.ID,
				Timestamp:      rTimestamp.Time,
				PowerWatts:     rPower.Float64,
				VoltageV:       rVoltage.Float64,
				CurrentMA:      rCurrent.Float64,
				EnergyTodayKWh: rToday.Float64,
				EnergyMonthKWh: rMonth.Float64,
			}
		}

		// Calculate Homeowner Analytics for this plug:
		// 1. Peak power recorded today (Max W)
		var peakToday, avgToday sql.NullFloat64
		_ = s.db.QueryRowContext(ctx, `
			SELECT MAX(power_watts), AVG(power_watts)
			FROM energy_readings
			WHERE plug_id = ? AND timestamp >= ?
		`, p.ID, midnightToday.UTC()).Scan(&peakToday, &avgToday)

		if peakToday.Valid {
			p.PeakWattsToday = math.Round(peakToday.Float64*10) / 10
		} else if p.LatestReading != nil {
			p.PeakWattsToday = p.LatestReading.PowerWatts
		}

		if avgToday.Valid {
			p.AvgWattsToday = math.Round(avgToday.Float64*10) / 10
		}

		// 1b. Peak power recorded this month
		var peakMonth sql.NullFloat64
		_ = s.db.QueryRowContext(ctx, `
			SELECT MAX(power_watts)
			FROM energy_readings
			WHERE plug_id = ? AND timestamp >= ?
		`, p.ID, startOfMonth.UTC()).Scan(&peakMonth)

		if peakMonth.Valid {
			p.PeakWattsMonth = math.Round(peakMonth.Float64*10) / 10
		} else if p.LatestReading != nil {
			p.PeakWattsMonth = p.LatestReading.PowerWatts
		}

		// 2. Standby/Vampire Load (lowest Watts recorded between 02:00 and 05:00 local time).
		// Without readings in that window there is no baseline, so none is reported.
		var nightSamples int
		var standby sql.NullFloat64
		_ = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*), MIN(CASE WHEN power_watts > 0.05 THEN power_watts END)
			FROM energy_readings
			WHERE plug_id = ? AND strftime('%H', substr(timestamp, 1, 19), 'localtime') IN ('02', '03', '04')
		`, p.ID).Scan(&nightSamples, &standby)

		p.HasStandbyData = nightSamples > 0
		if p.HasStandbyData && standby.Valid {
			p.StandbyWatts = math.Round(standby.Float64*10) / 10
		}

		// Annualized standby cost: StandbyWatts * 24 * 365.25 / 1000 * costPerKWh
		p.AnnualStandbyCost = math.Round((p.StandbyWatts*24.0*365.25/1000.0*costPerKWh)*100) / 100

		// 3. Estimated Monthly Cost: based on month energy so far, or extrapolated
		if p.LatestReading != nil {
			p.EstimatedMonthCost = math.Round((p.LatestReading.EnergyMonthKWh*costPerKWh)*100) / 100
		}

		// 4. Runtime today: approximate hours with power > 3.0 W
		var activeCount, totalCount int
		_ = s.db.QueryRowContext(ctx, `
			SELECT 
				COUNT(CASE WHEN power_watts > 3.0 THEN 1 END),
				COUNT(*)
			FROM energy_readings
			WHERE plug_id = ? AND timestamp >= ?
		`, p.ID, midnightToday.UTC()).Scan(&activeCount, &totalCount)

		if totalCount > 0 {
			hoursSinceMidnight := time.Since(midnightToday).Hours()
			if hoursSinceMidnight > 0 {
				p.RuntimeTodayHours = math.Round((float64(activeCount)/float64(totalCount)*hoursSinceMidnight)*10) / 10
			}
		}

		result = append(result, p)
	}

	return result, rows.Err()
}

// GetPlug returns a single plug with its latest telemetry and analytics.
func (s *Storage) GetPlug(ctx context.Context, id string, costPerKWh float64) (*models.PlugWithLatest, error) {
	plugs, err := s.GetPlugs(ctx, costPerKWh)
	if err != nil {
		return nil, err
	}
	for _, p := range plugs {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, nil
}

// GetLatestReading fetches the most recent reading for a given plug.
func (s *Storage) GetLatestReading(ctx context.Context, plugID string) (*models.EnergyReading, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, plug_id, timestamp, power_watts, voltage_v, current_ma, energy_today_kwh, energy_month_kwh
		FROM energy_readings
		WHERE plug_id = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`, plugID)

	var r models.EnergyReading
	err := row.Scan(
		&r.ID, &r.PlugID, &r.Timestamp, &r.PowerWatts, &r.VoltageV, &r.CurrentMA,
		&r.EnergyTodayKWh, &r.EnergyMonthKWh,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// GetHistory returns time-series history for a plug within a time window.
func (s *Storage) GetHistory(ctx context.Context, plugID string, since time.Time, limit int) ([]models.HistoryPoint, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var rows *sql.Rows
	var err error

	if plugID == "all" || plugID == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT timestamp, SUM(power_watts) as total_power
			FROM energy_readings
			WHERE timestamp >= ?
			GROUP BY strftime('%Y-%m-%d %H:%M', substr(timestamp, 1, 19))
			ORDER BY timestamp ASC
			LIMIT ?
		`, since.UTC(), limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT timestamp, power_watts
			FROM energy_readings
			WHERE plug_id = ? AND timestamp >= ?
			ORDER BY timestamp ASC
			LIMIT ?
		`, plugID, since.UTC(), limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []models.HistoryPoint
	for rows.Next() {
		var pt models.HistoryPoint
		if err := rows.Scan(&pt.Timestamp, &pt.PowerWatts); err != nil {
			return nil, err
		}
		points = append(points, pt)
	}

	return points, rows.Err()
}

// GetSummary returns top-level aggregated figures across all sockets for the top KPI bar.
func (s *Storage) GetSummary(ctx context.Context, currency string, costPerKWh float64) (*models.SystemSummary, error) {
	plugs, err := s.GetPlugs(ctx, costPerKWh)
	if err != nil {
		return nil, err
	}

	summary := &models.SystemSummary{
		Currency:        currency,
		TotalPlugsCount: len(plugs),
		LastUpdated:     time.Now().UTC(),
	}

	for _, p := range plugs {
		if p.IsActive {
			summary.ActivePlugsCount++
		}
		if p.LatestReading != nil && p.IsOn {
			summary.TotalPowerWatts += p.LatestReading.PowerWatts
			summary.TotalEnergyToday += p.LatestReading.EnergyTodayKWh
			summary.TotalEnergyMonth += p.LatestReading.EnergyMonthKWh
		}
		if p.HasStandbyData {
			summary.StandbyDataAvailable = true
		}
		summary.TotalStandbyWatts += p.StandbyWatts
		summary.AnnualStandbyWaste += p.AnnualStandbyCost
	}

	summary.TotalPowerWatts = math.Round(summary.TotalPowerWatts*10) / 10
	summary.TotalEnergyToday = math.Round(summary.TotalEnergyToday*1000) / 1000
	summary.TotalEnergyMonth = math.Round(summary.TotalEnergyMonth*100) / 100
	summary.TotalStandbyWatts = math.Round(summary.TotalStandbyWatts*10) / 10
	summary.AnnualStandbyWaste = math.Round(summary.AnnualStandbyWaste*100) / 100

	// Live Burn Rate in €/hour: (Watts / 1000) * costPerKWh
	summary.LiveBurnRatePerHour = math.Round(((summary.TotalPowerWatts/1000.0)*costPerKWh)*1000) / 1000
	summary.TotalCostToday = math.Round((summary.TotalEnergyToday*costPerKWh)*100) / 100
	summary.EstimatedCostMonth = math.Round((summary.TotalEnergyMonth*costPerKWh)*100) / 100

	return summary, nil
}

// startOfLocalDay returns local midnight of the day containing t.
func startOfLocalDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func valuePtr(v float64) *float64 {
	return &v
}

// GetBreakdown returns stacked time-series data grouped by plug or category for charts.
// start selects the local day (rangeType "today") or month (rangeType "month") to show.
func (s *Storage) GetBreakdown(ctx context.Context, rangeType, byType string, start time.Time, costPerKWh float64) (*models.BreakdownResponse, error) {
	plugs, err := s.GetPlugs(ctx, costPerKWh)
	if err != nil {
		return nil, err
	}

	resp := &models.BreakdownResponse{
		Range:  rangeType,
		Labels: make([]string, 0),
		Series: make([]models.BreakdownSeries, 0),
	}

	// Distinct colors for chart series
	colors := []string{
		"#10b981", "#3b82f6", "#8b5cf6", "#f59e0b",
		"#ec4899", "#06b6d4", "#84cc16", "#f97316",
	}

	if rangeType == "month" {
		// Daily view for the selected month; the current month stops at today
		monthStart := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.Local)
		monthEnd := monthStart.AddDate(0, 1, 0)
		daysInMonth := monthEnd.AddDate(0, 0, -1).Day()
		now := time.Now()
		isCurrentMonth := !now.Before(monthStart) && now.Before(monthEnd)
		if isCurrentMonth {
			daysInMonth = now.Day()
		}

		for d := 1; d <= daysInMonth; d++ {
			resp.Labels = append(resp.Labels, fmt.Sprintf("Day %d", d))
		}

		for idx, p := range plugs {
			series := models.BreakdownSeries{
				ID:    p.ID,
				Label: p.Name,
				Color: colors[idx%len(colors)],
				Data:  make([]*float64, daysInMonth),
			}

			// Query daily consumption for this plug
			rows, qErr := s.db.QueryContext(ctx, `
				SELECT strftime('%d', substr(timestamp, 1, 19), 'localtime') as day_str, MAX(energy_today_kwh)
				FROM energy_readings
				WHERE plug_id = ? AND timestamp >= ? AND timestamp < ?
				GROUP BY day_str
			`, p.ID, monthStart.UTC(), monthEnd.UTC())

			if qErr == nil {
				for rows.Next() {
					var dayStr string
					var val float64
					if err := rows.Scan(&dayStr, &val); err == nil {
						var dayNum int
						fmt.Sscanf(dayStr, "%d", &dayNum)
						if dayNum >= 1 && dayNum <= daysInMonth {
							series.Data[dayNum-1] = valuePtr(math.Round(val*100) / 100)
						}
					}
				}
				rows.Close()
			}

			// If data is sparse in dev, fill current day with today's kWh
			if isCurrentMonth && p.LatestReading != nil && series.Data[daysInMonth-1] == nil {
				series.Data[daysInMonth-1] = valuePtr(math.Round(p.LatestReading.EnergyTodayKWh*100) / 100)
			}

			resp.Series = append(resp.Series, series)
		}

	} else {
		// Hourly view for the selected day (00:00 to 23:00 local time)
		for h := 0; h < 24; h++ {
			resp.Labels = append(resp.Labels, fmt.Sprintf("%02d:00", h))
		}

		dayStart := startOfLocalDay(start)
		dayEnd := dayStart.AddDate(0, 0, 1)
		now := time.Now()
		isToday := !now.Before(dayStart) && now.Before(dayEnd)

		for idx, p := range plugs {
			series := models.BreakdownSeries{
				ID:    p.ID,
				Label: p.Name,
				Color: colors[idx%len(colors)],
				Data:  make([]*float64, 24),
			}

			// Query average watts per hour slot today
			rows, qErr := s.db.QueryContext(ctx, `
				SELECT strftime('%H', substr(timestamp, 1, 19), 'localtime') as hour_str, AVG(power_watts)
				FROM energy_readings
				WHERE plug_id = ? AND timestamp >= ? AND timestamp < ?
				GROUP BY hour_str
			`, p.ID, dayStart.UTC(), dayEnd.UTC())

			if qErr == nil {
				for rows.Next() {
					var hourStr string
					var avgWatts float64
					if err := rows.Scan(&hourStr, &avgWatts); err == nil {
						var hNum int
						fmt.Sscanf(hourStr, "%d", &hNum)
						if hNum >= 0 && hNum < 24 {
							series.Data[hNum] = valuePtr(math.Round(avgWatts*10) / 10)
						}
					}
				}
				rows.Close()
			}

			// If the current hour has no samples yet, show the latest reading for today
			currentHour := now.Hour()
			if isToday && p.LatestReading != nil && series.Data[currentHour] == nil {
				series.Data[currentHour] = valuePtr(math.Round(p.LatestReading.PowerWatts*10) / 10)
			}

			resp.Series = append(resp.Series, series)
		}
	}

	return resp, nil
}

// Close gracefully flushes the in-memory write buffer and closes the SQLite database.
func (s *Storage) Close() error {
	close(s.stopFlush)
	<-s.flushDone
	return s.db.Close()
}
