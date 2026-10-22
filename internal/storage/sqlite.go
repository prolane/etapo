package storage

import (
	"context"
	"database/sql"
	"fmt"
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
	_, err := s.db.Exec(schema)
	return err
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
	query := `
	INSERT INTO plugs (id, name, model, driver_type, ip, mac, is_active, is_on, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		model = excluded.model,
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
		p.ID, p.Name, p.Model, p.DriverType, p.IP, p.MAC, p.IsActive, p.IsOn, p.CreatedAt, p.UpdatedAt,
	)
	return err
}

// UpdatePlugState updates the on/off state of a plug.
func (s *Storage) UpdatePlugState(ctx context.Context, id string, isOn bool) error {
	query := `UPDATE plugs SET is_on = ?, updated_at = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, isOn, time.Now().UTC(), id)
	return err
}

// GetPlugs returns all configured plugs with their latest energy reading in a single query.
func (s *Storage) GetPlugs(ctx context.Context) ([]models.PlugWithLatest, error) {
	query := `
		SELECT 
			p.id, p.name, p.model, p.driver_type, p.ip, p.mac, p.is_active, p.is_on, p.created_at, p.updated_at,
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
	for rows.Next() {
		var p models.PlugWithLatest
		var mac sql.NullString
		var rID sql.NullInt64
		var rTimestamp sql.NullTime
		var rPower, rVoltage, rCurrent, rToday, rMonth sql.NullFloat64

		err := rows.Scan(
			&p.ID, &p.Name, &p.Model, &p.DriverType, &p.IP, &mac,
			&p.IsActive, &p.IsOn, &p.CreatedAt, &p.UpdatedAt,
			&rID, &rTimestamp, &rPower, &rVoltage, &rCurrent, &rToday, &rMonth,
		)
		if err != nil {
			return nil, err
		}
		p.MAC = mac.String

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

		result = append(result, p)
	}

	return result, rows.Err()
}

// GetPlug returns a single plug with its latest telemetry.
func (s *Storage) GetPlug(ctx context.Context, id string) (*models.PlugWithLatest, error) {
	query := `
		SELECT 
			p.id, p.name, p.model, p.driver_type, p.ip, p.mac, p.is_active, p.is_on, p.created_at, p.updated_at,
			r.id, r.timestamp, r.power_watts, r.voltage_v, r.current_ma, r.energy_today_kwh, r.energy_month_kwh
		FROM plugs p
		LEFT JOIN energy_readings r ON r.id = (
			SELECT id FROM energy_readings 
			WHERE plug_id = p.id 
			ORDER BY timestamp DESC 
			LIMIT 1
		)
		WHERE p.id = ?
	`
	row := s.db.QueryRowContext(ctx, query, id)

	var p models.PlugWithLatest
	var mac sql.NullString
	var rID sql.NullInt64
	var rTimestamp sql.NullTime
	var rPower, rVoltage, rCurrent, rToday, rMonth sql.NullFloat64

	err := row.Scan(
		&p.ID, &p.Name, &p.Model, &p.DriverType, &p.IP, &mac,
		&p.IsActive, &p.IsOn, &p.CreatedAt, &p.UpdatedAt,
		&rID, &rTimestamp, &rPower, &rVoltage, &rCurrent, &rToday, &rMonth,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.MAC = mac.String

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

	return &p, nil
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
		// Aggregate across all plugs grouped by rounded timestamp
		rows, err = s.db.QueryContext(ctx, `
			SELECT timestamp, SUM(power_watts) as total_power
			FROM energy_readings
			WHERE timestamp >= ?
			GROUP BY strftime('%Y-%m-%d %H:%M', timestamp)
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

// GetSummary returns top-level aggregated figures across all sockets.
func (s *Storage) GetSummary(ctx context.Context, currency string, costPerKWh float64) (*models.SystemSummary, error) {
	plugs, err := s.GetPlugs(ctx)
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
	}

	summary.EstimatedCostDay = summary.TotalEnergyToday * costPerKWh

	return summary, nil
}

// Close gracefully flushes the in-memory write buffer and closes the SQLite database.
func (s *Storage) Close() error {
	close(s.stopFlush)
	<-s.flushDone
	return s.db.Close()
}
