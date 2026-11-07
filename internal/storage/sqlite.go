package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"etapo/internal/models"
)

const (
	// bucketSec is the length of a stored sample bucket. The dashboard never shows anything finer.
	bucketSec = 300
	// dayLayout is the local calendar day used as the key of a daily row.
	dayLayout         = "2006-01-02"
	retentionInterval = 24 * time.Hour
)

// Storage keeps plug metadata, the live state of each plug, five-minute sample buckets and daily totals in SQLite.
// Readings are not stored one by one: they are summed into buckets in memory and written with each flush.
type Storage struct {
	db              *sql.DB
	dbPath          string
	flushInterval   time.Duration
	stopFlush       chan struct{}
	flushDone       chan struct{}
	retentionCancel context.CancelFunc
	retentionDone   chan struct{}

	mu     sync.Mutex
	plugs  map[string]*plugMem
	days   map[dayKey]*dayDelta
	closed []closedBucket
}

// bucketAgg is the sum, maximum and count of the power samples in a bucket. Sums can be merged, averages cannot.
type bucketAgg struct {
	sumMW int64
	maxMW int64
	n     int64
}

func (a *bucketAgg) add(mw int64) {
	if a.n == 0 || mw > a.maxMW {
		a.maxMW = mw
	}
	a.sumMW += mw
	a.n++
}

func (a *bucketAgg) merge(o bucketAgg) {
	if o.n == 0 {
		return
	}
	if a.n == 0 || o.maxMW > a.maxMW {
		a.maxMW = o.maxMW
	}
	a.sumMW += o.sumMW
	a.n += o.n
}

// plugMem is the in-memory state of one plug between two flushes.
type plugMem struct {
	isOn        *bool
	sampled     bool
	sampledAt   int64
	powerMW     int64
	energyToday int64
	energyMonth int64
	hasOpen     bool
	openStart   int64
	open        bucketAgg
	dirty       bool
}

type closedBucket struct {
	plug  string
	start int64
	agg   bucketAgg
}

type dayKey struct {
	plug string
	day  string
}

// dayDelta is what a daily row gains between two flushes. The energy counters are the last values seen.
type dayDelta struct {
	energyToday int64
	energyMonth int64
	lastTS      int64
	peakMW      int64
	sumMW       int64
	n           int64
}

func (d *dayDelta) absorb(o *dayDelta) {
	if o.lastTS >= d.lastTS {
		d.lastTS, d.energyToday, d.energyMonth = o.lastTS, o.energyToday, o.energyMonth
	}
	if o.peakMW > d.peakMW {
		d.peakMW = o.peakMW
	}
	d.sumMW += o.sumMW
	d.n += o.n
}

// New creates and initializes SQLite storage with WAL mode and in-memory batching.
func New(dbPath string, flushInterval time.Duration) (*Storage, error) {
	if flushInterval <= 0 {
		flushInterval = 30 * time.Second
	}

	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory %q: %w", dir, err)
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)

	s := &Storage{
		db:            db,
		dbPath:        dbPath,
		flushInterval: flushInterval,
		stopFlush:     make(chan struct{}),
		flushDone:     make(chan struct{}),
		plugs:         make(map[string]*plugMem),
		days:          make(map[dayKey]*dayDelta),
	}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema migration failed: %w", err)
	}
	if err := s.reopenOpenBuckets(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to restore open buckets: %w", err)
	}

	go s.flushLoop()

	return s, nil
}

func (s *Storage) migrate() error {
	var legacy int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'energy_readings'`).Scan(&legacy); err != nil {
		return err
	}
	if legacy > 0 {
		if err := s.replaceRawReadings(); err != nil {
			return err
		}
	}

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

	CREATE TABLE IF NOT EXISTS plug_state (
		plug_id         TEXT PRIMARY KEY,
		is_on           INTEGER NOT NULL DEFAULT 1,
		sampled_at      INTEGER,
		power_mw        INTEGER NOT NULL DEFAULT 0,
		energy_today_wh INTEGER NOT NULL DEFAULT 0,
		energy_month_wh INTEGER NOT NULL DEFAULT 0,
		open_bucket     INTEGER,
		open_sum_mw     INTEGER NOT NULL DEFAULT 0,
		open_max_mw     INTEGER NOT NULL DEFAULT 0,
		open_n          INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS samples_5m (
		plug_id TEXT    NOT NULL,
		bucket  INTEGER NOT NULL,
		sum_mw  INTEGER NOT NULL,
		max_mw  INTEGER NOT NULL,
		n       INTEGER NOT NULL,
		PRIMARY KEY (plug_id, bucket)
	) WITHOUT ROWID;

	CREATE TABLE IF NOT EXISTS daily (
		plug_id         TEXT    NOT NULL,
		day             TEXT    NOT NULL,
		energy_today_wh INTEGER NOT NULL,
		energy_month_wh INTEGER NOT NULL,
		peak_mw         INTEGER NOT NULL,
		sum_mw          INTEGER NOT NULL,
		n               INTEGER NOT NULL,
		PRIMARY KEY (plug_id, day)
	) WITHOUT ROWID;
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Migration for existing databases that might not have category column yet
	_, _ = s.db.Exec(`ALTER TABLE plugs ADD COLUMN category TEXT DEFAULT 'General';`)

	return nil
}

// replaceRawReadings removes the table of individual readings from databases made before 1.10.0. The whole
// database is copied first, so the raw data can still be examined. The copy is never removed by etapo.
func (s *Storage) replaceRawReadings() error {
	ext := filepath.Ext(s.dbPath)
	backup := strings.TrimSuffix(s.dbPath, ext) + "-before-rollups-" + time.Now().Format("20060102-150405") + ext + ".bak"
	if _, err := s.db.Exec(`VACUUM INTO ?`, backup); err != nil {
		return fmt.Errorf("failed to back up the database to %s before replacing raw readings: %w", backup, err)
	}
	if _, err := s.db.Exec(`DROP TABLE energy_readings`); err != nil {
		return fmt.Errorf("failed to drop raw readings: %w", err)
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("failed to shrink the database: %w", err)
	}
	log.Printf("[Storage] Replaced raw readings with 5-minute rollups; the previous database is kept at %s", backup)
	return nil
}

// reopenOpenBuckets moves the bucket that was still filling when the previous process stopped into the bucket
// table. A bucket that is restarted later is merged with it by the same upsert.
func (s *Storage) reopenOpenBuckets() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO samples_5m (plug_id, bucket, sum_mw, max_mw, n)
		SELECT plug_id, open_bucket, open_sum_mw, open_max_mw, open_n FROM plug_state WHERE open_n > 0
		ON CONFLICT (plug_id, bucket) DO UPDATE SET
			sum_mw = sum_mw + excluded.sum_mw,
			max_mw = max(max_mw, excluded.max_mw),
			n = n + excluded.n
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE plug_state SET open_bucket = NULL, open_sum_mw = 0, open_max_mw = 0, open_n = 0 WHERE open_n > 0`); err != nil {
		return err
	}
	return tx.Commit()
}

func toMilliwatts(watts float64) int64 {
	return int64(math.Round(watts * 1000))
}

func toWattHours(kwh float64) int64 {
	return int64(math.Round(kwh * 1000))
}

func (s *Storage) plugFor(id string) *plugMem {
	p := s.plugs[id]
	if p == nil {
		p = &plugMem{}
		s.plugs[id] = p
	}
	return p
}

// RecordReading adds a reading to the bucket, the daily totals and the live state of its plug. Nothing is written
// to the database until the next flush.
func (s *Storage) RecordReading(r models.EnergyReading) {
	ts := r.Timestamp.Unix()
	mw := toMilliwatts(r.PowerWatts)
	start := ts - ts%bucketSec
	day := r.Timestamp.In(time.Local).Format(dayLayout)
	wh := toWattHours(r.EnergyTodayKWh)
	mwh := toWattHours(r.EnergyMonthKWh)

	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.plugFor(r.PlugID)
	if p.hasOpen && p.openStart != start {
		s.closed = append(s.closed, closedBucket{plug: r.PlugID, start: p.openStart, agg: p.open})
		p.hasOpen = false
	}
	if !p.hasOpen {
		p.hasOpen, p.openStart, p.open = true, start, bucketAgg{}
	}
	p.open.add(mw)

	if !p.sampled || ts >= p.sampledAt {
		p.sampled, p.sampledAt, p.powerMW, p.energyToday, p.energyMonth = true, ts, mw, wh, mwh
	}
	p.dirty = true

	k := dayKey{plug: r.PlugID, day: day}
	d := s.days[k]
	if d == nil {
		d = &dayDelta{}
		s.days[k] = d
	}
	if mw > d.peakMW {
		d.peakMW = mw
	}
	d.sumMW += mw
	d.n++
	if ts >= d.lastTS {
		d.lastTS, d.energyToday, d.energyMonth = ts, wh, mwh
	}
}

// RecordPlugState stores the relay state a poll reported. It is written with the next flush.
func (s *Storage) RecordPlugState(id string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.plugFor(id)
	p.isOn = &on
	p.dirty = true
}

// SetPlugOn stores the relay state immediately, so a change made from the dashboard is visible at once.
func (s *Storage) SetPlugOn(ctx context.Context, id string, on bool) error {
	s.RecordPlugState(id, on)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO plug_state (plug_id, is_on) VALUES (?, ?)
		ON CONFLICT (plug_id) DO UPDATE SET is_on = excluded.is_on
	`, id, on)
	return err
}

// stateWrite is a copy of the in-memory state of one plug, taken under the lock.
type stateWrite struct {
	plug        string
	isOn        *bool
	sampled     bool
	sampledAt   int64
	powerMW     int64
	energyToday int64
	energyMonth int64
	hasOpen     bool
	openStart   int64
	open        bucketAgg
}

// Flush writes the state of every plug, the closed buckets and the daily totals in one transaction.
func (s *Storage) Flush() error {
	s.mu.Lock()
	var states []stateWrite
	for id, p := range s.plugs {
		if !p.dirty {
			continue
		}
		p.dirty = false
		states = append(states, stateWrite{
			plug: id, isOn: p.isOn, sampled: p.sampled, sampledAt: p.sampledAt,
			powerMW: p.powerMW, energyToday: p.energyToday, energyMonth: p.energyMonth,
			hasOpen: p.hasOpen, openStart: p.openStart, open: p.open,
		})
	}
	closed := s.closed
	s.closed = nil
	days := s.days
	s.days = make(map[dayKey]*dayDelta)
	s.mu.Unlock()

	if len(states) == 0 && len(closed) == 0 && len(days) == 0 {
		return nil
	}
	if err := s.writeFlush(states, closed, days); err != nil {
		// Keep what did not reach the database for the next flush.
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = append(closed, s.closed...)
		for k, o := range days {
			if cur := s.days[k]; cur != nil {
				cur.absorb(o)
			} else {
				s.days[k] = o
			}
		}
		for _, st := range states {
			s.plugFor(st.plug).dirty = true
		}
		return err
	}
	return nil
}

func (s *Storage) writeFlush(states []stateWrite, closed []closedBucket, days map[dayKey]*dayDelta) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin flush transaction: %w", err)
	}
	defer tx.Rollback()

	sampleStmt, err := tx.Prepare(`
		INSERT INTO plug_state (plug_id, sampled_at, power_mw, energy_today_wh, energy_month_wh, open_bucket, open_sum_mw, open_max_mw, open_n)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (plug_id) DO UPDATE SET
			sampled_at = excluded.sampled_at,
			power_mw = excluded.power_mw,
			energy_today_wh = excluded.energy_today_wh,
			energy_month_wh = excluded.energy_month_wh,
			open_bucket = excluded.open_bucket,
			open_sum_mw = excluded.open_sum_mw,
			open_max_mw = excluded.open_max_mw,
			open_n = excluded.open_n
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare state statement: %w", err)
	}
	defer sampleStmt.Close()

	onStmt, err := tx.Prepare(`
		INSERT INTO plug_state (plug_id, is_on) VALUES (?, ?)
		ON CONFLICT (plug_id) DO UPDATE SET is_on = excluded.is_on
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare relay statement: %w", err)
	}
	defer onStmt.Close()

	for _, st := range states {
		if st.sampled {
			var openBucket any
			var openSum, openMax, openN int64
			if st.hasOpen {
				openBucket, openSum, openMax, openN = st.openStart, st.open.sumMW, st.open.maxMW, st.open.n
			}
			if _, err := sampleStmt.Exec(st.plug, st.sampledAt, st.powerMW, st.energyToday, st.energyMonth, openBucket, openSum, openMax, openN); err != nil {
				return fmt.Errorf("failed to write plug state: %w", err)
			}
		}
		if st.isOn != nil {
			if _, err := onStmt.Exec(st.plug, *st.isOn); err != nil {
				return fmt.Errorf("failed to write relay state: %w", err)
			}
		}
	}

	for _, c := range closed {
		if _, err := tx.Exec(`
			INSERT INTO samples_5m (plug_id, bucket, sum_mw, max_mw, n) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (plug_id, bucket) DO UPDATE SET
				sum_mw = sum_mw + excluded.sum_mw,
				max_mw = max(max_mw, excluded.max_mw),
				n = n + excluded.n
		`, c.plug, c.start, c.agg.sumMW, c.agg.maxMW, c.agg.n); err != nil {
			return fmt.Errorf("failed to write bucket: %w", err)
		}
	}

	for k, d := range days {
		if _, err := tx.Exec(`
			INSERT INTO daily (plug_id, day, energy_today_wh, energy_month_wh, peak_mw, sum_mw, n) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (plug_id, day) DO UPDATE SET
				energy_today_wh = excluded.energy_today_wh,
				energy_month_wh = excluded.energy_month_wh,
				peak_mw = max(peak_mw, excluded.peak_mw),
				sum_mw = sum_mw + excluded.sum_mw,
				n = n + excluded.n
		`, k.plug, k.day, d.energyToday, d.energyMonth, d.peakMW, d.sumMW, d.n); err != nil {
			return fmt.Errorf("failed to write daily totals: %w", err)
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

// StartRetention deletes data older than retention once at startup and then once a day, until Close is called.
// Freed pages are reused by SQLite; the file is not shrunk.
func (s *Storage) StartRetention(retention time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	s.retentionCancel = cancel
	s.retentionDone = make(chan struct{})
	go s.retentionLoop(ctx, retention)
}

func (s *Storage) retentionLoop(ctx context.Context, retention time.Duration) {
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	defer close(s.retentionDone)

	for {
		deleted, err := s.PurgeBefore(ctx, time.Now().Add(-retention))
		if err != nil && ctx.Err() == nil {
			log.Printf("[Storage] Retention purge failed after deleting %d rows: %v", deleted, err)
		} else if deleted > 0 {
			log.Printf("[Storage] Retention purge deleted %d rows older than %s", deleted, retention)
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// PurgeBefore deletes buckets and daily totals before cutoff. Each plug is handled with its primary key, so no
// extra index is needed. It stops early when ctx is cancelled and returns the number of rows deleted so far.
func (s *Storage) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM plugs`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	bucketCutoff := cutoff.Unix()
	dayCutoff := cutoff.In(time.Local).Format(dayLayout)
	var total int64
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := s.db.ExecContext(ctx, `DELETE FROM samples_5m WHERE plug_id = ? AND bucket < ?`, id, bucketCutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n

		res, err = s.db.ExecContext(ctx, `DELETE FROM daily WHERE plug_id = ? AND day < ?`, id, dayCutoff)
		if err != nil {
			return total, err
		}
		n, _ = res.RowsAffected()
		total += n
	}
	return total, nil
}

// UpsertPlug inserts or updates plug metadata. A plug that is configured is shown again.
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

// HidePlugsExcept hides every plug whose ID is not in ids. Their data is kept.
func (s *Storage) HidePlugsExcept(ctx context.Context, ids []string) error {
	query := `UPDATE plugs SET is_active = 0 WHERE id NOT IN (` + placeholders(len(ids)) + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if len(ids) == 0 {
		query = `UPDATE plugs SET is_active = 0`
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// PlugMACs returns the MAC addresses learned from plugs, keyed by plug ID. Plugs without one are left out.
func (s *Storage) PlugMACs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(mac, '') FROM plugs WHERE COALESCE(mac, '') != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	macs := make(map[string]string)
	for rows.Next() {
		var id, mac string
		if err := rows.Scan(&id, &mac); err != nil {
			return nil, err
		}
		macs[id] = mac
	}
	return macs, rows.Err()
}

// SetPlugMAC stores the MAC address a plug reported.
func (s *Storage) SetPlugMAC(ctx context.Context, id, mac string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE plugs SET mac = ?, updated_at = ? WHERE id = ?`, mac, time.Now().UTC(), id)
	return err
}

// PlugIP returns the address a plug was last seen at, and whether the plug exists.
func (s *Storage) PlugIP(ctx context.Context, id string) (string, bool, error) {
	var ip string
	err := s.db.QueryRowContext(ctx, `SELECT ip FROM plugs WHERE id = ?`, id).Scan(&ip)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return ip, true, nil
}

// UpdatePlugIP stores the address a plug is now reachable at.
func (s *Storage) UpdatePlugIP(ctx context.Context, id, ip string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE plugs SET ip = ?, updated_at = ? WHERE id = ?`, ip, time.Now().UTC(), id)
	return err
}

// plugRow is a visible plug with its stored live state.
type plugRow struct {
	models.Plug
	isOn        bool
	sampled     bool
	sampledAt   int64
	powerMW     int64
	energyToday int64
	energyMonth int64
}

// plugRows returns the visible plugs, by name, with their live state.
func (s *Storage) plugRows(ctx context.Context) ([]plugRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.name, p.model, p.category, p.driver_type, p.ip, p.mac, p.is_active, p.created_at, p.updated_at,
			COALESCE(st.is_on, 1), st.sampled_at, COALESCE(st.power_mw, 0), COALESCE(st.energy_today_wh, 0), COALESCE(st.energy_month_wh, 0)
		FROM plugs p
		LEFT JOIN plug_state st ON st.plug_id = p.id
		WHERE p.is_active = 1
		ORDER BY p.name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []plugRow
	for rows.Next() {
		var r plugRow
		var category, mac sql.NullString
		var sampledAt sql.NullInt64
		var isOn, active int64
		if err := rows.Scan(
			&r.ID, &r.Name, &r.Model, &category, &r.DriverType, &r.IP, &mac, &active, &r.CreatedAt, &r.UpdatedAt,
			&isOn, &sampledAt, &r.powerMW, &r.energyToday, &r.energyMonth,
		); err != nil {
			return nil, err
		}
		r.IsActive = active == 1
		r.IsOn = isOn == 1
		r.isOn = isOn == 1
		r.MAC = mac.String
		r.Category = category.String
		if r.Category == "" {
			r.Category = "General"
		}
		if sampledAt.Valid {
			r.sampled = true
			r.sampledAt = sampledAt.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// bucketAggs returns the buckets of one plug that start in [from, to), including the bucket still filling.
func (s *Storage) bucketAggs(ctx context.Context, plugID string, from, to int64) (map[int64]bucketAgg, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT bucket, sum_mw, max_mw, n FROM samples_5m
		WHERE plug_id = ? AND bucket >= ? AND bucket < ?
	`, plugID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]bucketAgg)
	for rows.Next() {
		var b int64
		var a bucketAgg
		if err := rows.Scan(&b, &a.sumMW, &a.maxMW, &a.n); err != nil {
			return nil, err
		}
		cur := out[b]
		cur.merge(a)
		out[b] = cur
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var openBucket sql.NullInt64
	var open bucketAgg
	err = s.db.QueryRowContext(ctx, `
		SELECT open_bucket, open_sum_mw, open_max_mw, open_n FROM plug_state WHERE plug_id = ?
	`, plugID).Scan(&openBucket, &open.sumMW, &open.maxMW, &open.n)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if openBucket.Valid && open.n > 0 && openBucket.Int64 >= from && openBucket.Int64 < to {
		cur := out[openBucket.Int64]
		cur.merge(open)
		out[openBucket.Int64] = cur
	}
	return out, nil
}

// dailyRow is the stored totals of one plug for one local day.
type dailyRow struct {
	day         string
	energyToday int64
	energyMonth int64
	peakMW      int64
	sumMW       int64
	n           int64
}

// dailyRange returns the daily rows of one plug for the days from `from` up to but not including `to`, both as
// local dates (YYYY-MM-DD), oldest first.
func (s *Storage) dailyRange(ctx context.Context, plugID, from, to string) ([]dailyRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT day, energy_today_wh, energy_month_wh, peak_mw, sum_mw, n FROM daily
		WHERE plug_id = ? AND day >= ? AND day < ?
		ORDER BY day
	`, plugID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []dailyRow
	for rows.Next() {
		var d dailyRow
		if err := rows.Scan(&d.day, &d.energyToday, &d.energyMonth, &d.peakMW, &d.sumMW, &d.n); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// window is one standby window: the bucket starts in [start, end) belong to it. Times are Unix seconds.
type window struct {
	start, end int64
}

// standbyWindows returns the most recent `Nights` standby windows that have ended by now, oldest first. The
// reference is the most recent window that has completed: after the window ends, that is today's window; during
// the window or before it starts, it is yesterday's.
func standbyWindows(now time.Time, period models.StandbyPeriod) []window {
	wrap := period.EndMinute <= period.StartMinute
	nights := max(period.Nights, 1)
	day0 := startOfLocalDay(now)

	var out []window
	for k := 0; len(out) < nights && k < nights+3; k++ {
		day := day0.AddDate(0, 0, -k)
		endDay := day
		if wrap {
			endDay = day.AddDate(0, 0, 1)
		}
		w := window{
			start: atMinute(day, period.StartMinute).Unix(),
			end:   atMinute(endDay, period.EndMinute).Unix(),
		}
		if w.end > now.Unix() {
			continue
		}
		out = append(out, w)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func atMinute(day time.Time, minute int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, time.Local)
}

// standbyTotal adds the buckets that fall inside one of the windows. wins must be sorted and not overlap.
func standbyTotal(aggs map[int64]bucketAgg, wins []window) bucketAgg {
	var total bucketAgg
	for b, a := range aggs {
		i := sort.Search(len(wins), func(i int) bool { return wins[i].end > b })
		if i < len(wins) && wins[i].start <= b {
			total.merge(a)
		}
	}
	return total
}

func avgWatts(sumMW, n int64) float64 {
	if n == 0 {
		return 0
	}
	return round(float64(sumMW)/float64(n)/1000, 10)
}

func round(v float64, factor float64) float64 {
	return math.Round(v*factor) / factor
}

// GetPlugs returns all visible plugs enriched with their latest reading and analytics.
func (s *Storage) GetPlugs(ctx context.Context, costPerKWh float64, standbyPeriod models.StandbyPeriod) ([]models.PlugWithLatest, error) {
	rows, err := s.plugRows(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	todayStart := startOfLocalDay(now)
	todayKey := todayStart.Format(dayLayout)
	tomorrowKey := todayStart.AddDate(0, 0, 1).Format(dayLayout)
	wins := standbyWindows(now, standbyPeriod)

	result := make([]models.PlugWithLatest, 0, len(rows))
	for _, row := range rows {
		p := models.PlugWithLatest{Plug: row.Plug}
		p.IsOn = row.isOn

		if row.sampled {
			watts := float64(row.powerMW) / 1000
			p.LatestReading = &models.EnergyReading{
				PlugID:         row.ID,
				Timestamp:      time.Unix(row.sampledAt, 0).UTC(),
				PowerWatts:     watts,
				VoltageV:       230,
				CurrentMA:      watts / 230 * 1000,
				EnergyTodayKWh: float64(row.energyToday) / 1000,
				EnergyMonthKWh: float64(row.energyMonth) / 1000,
			}
		}

		daily, err := s.dailyRange(ctx, row.ID, todayKey, tomorrowKey)
		if err != nil {
			return nil, err
		}
		if len(daily) > 0 {
			d := daily[0]
			p.PeakWattsToday = round(float64(d.peakMW)/1000, 10)
			p.AvgWattsToday = avgWatts(d.sumMW, d.n)
		} else if p.LatestReading != nil {
			p.PeakWattsToday = p.LatestReading.PowerWatts
		}

		if len(wins) > 0 {
			aggs, err := s.bucketAggs(ctx, row.ID, wins[0].start, wins[len(wins)-1].end)
			if err != nil {
				return nil, err
			}
			st := standbyTotal(aggs, wins)
			p.HasStandbyData = st.n > 0
			if p.HasStandbyData {
				p.StandbyWatts = avgWatts(st.sumMW, st.n)
			}
		}
		p.AnnualStandbyCost = math.Round((p.StandbyWatts*24.0*365.25/1000.0*costPerKWh)*100) / 100

		if p.LatestReading != nil {
			p.EstimatedMonthCost = math.Round((p.LatestReading.EnergyMonthKWh*costPerKWh)*100) / 100
		}

		result = append(result, p)
	}

	return result, nil
}

// GetPlug returns a single plug with its latest telemetry and analytics, or nil if it does not exist.
func (s *Storage) GetPlug(ctx context.Context, id string, costPerKWh float64, standbyPeriod models.StandbyPeriod) (*models.PlugWithLatest, error) {
	plugs, err := s.GetPlugs(ctx, costPerKWh, standbyPeriod)
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

// GetHistory returns the average power of a plug for each bucket from start up to end.
// Buckets are counted from start, so start should be aligned to the bucket size.
// A bucket without readings has a nil PowerWatts, so the chart shows a gap.
func (s *Storage) GetHistory(ctx context.Context, plugID string, start, end time.Time, bucket time.Duration) ([]models.HistoryPoint, error) {
	aggs, err := s.bucketAggs(ctx, plugID, start.Unix(), end.Unix())
	if err != nil {
		return nil, err
	}

	bucketSecs := int64(bucket / time.Second)
	slots := make(map[int64]bucketAgg)
	for b, a := range aggs {
		slot := (b - start.Unix()) / bucketSecs
		cur := slots[slot]
		cur.merge(a)
		slots[slot] = cur
	}

	var points []models.HistoryPoint
	for i := int64(0); ; i++ {
		ts := start.Add(time.Duration(i) * bucket)
		if !ts.Before(end) {
			break
		}
		pt := models.HistoryPoint{Timestamp: ts}
		if a, ok := slots[i]; ok && a.n > 0 {
			w := avgWatts(a.sumMW, a.n)
			pt.PowerWatts = &w
		}
		points = append(points, pt)
	}
	return points, nil
}

// GetSummary returns top-level aggregated figures across all sockets for the top KPI bar.
// A plug without a reading within offlineAfter adds no live power, because its last value is out of date.
func (s *Storage) GetSummary(ctx context.Context, currency string, costPerKWh float64, standbyPeriod models.StandbyPeriod, offlineAfter time.Duration) (*models.SystemSummary, error) {
	plugs, err := s.GetPlugs(ctx, costPerKWh, standbyPeriod)
	if err != nil {
		return nil, err
	}

	summary := &models.SystemSummary{
		Currency:        currency,
		TotalPlugsCount: len(plugs),
		LastUpdated:     time.Now().UTC(),
	}

	now := time.Now()
	for _, p := range plugs {
		if p.IsActive {
			summary.ActivePlugsCount++
		}
		if p.LatestReading != nil {
			if p.IsOn && models.Online(p.LatestReading, now, offlineAfter) {
				summary.TotalPowerWatts += p.LatestReading.PowerWatts
			}
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

// GetBreakdown returns stacked time-series data per plug for charts.
// start selects the local day (rangeType "today") or month (rangeType "month") to show.
func (s *Storage) GetBreakdown(ctx context.Context, rangeType string, start time.Time) (*models.BreakdownResponse, error) {
	plugs, err := s.plugRows(ctx)
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
			days, err := s.dailyRange(ctx, p.ID, monthStart.Format(dayLayout), monthEnd.Format(dayLayout))
			if err != nil {
				return nil, err
			}
			for _, d := range days {
				t, err := time.ParseInLocation(dayLayout, d.day, time.Local)
				if err != nil {
					continue
				}
				if n := t.Day(); n >= 1 && n <= daysInMonth {
					series.Data[n-1] = valuePtr(round(float64(d.energyToday)/1000, 100))
				}
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

		for idx, p := range plugs {
			series := models.BreakdownSeries{
				ID:    p.ID,
				Label: p.Name,
				Color: colors[idx%len(colors)],
				Data:  make([]*float64, 24),
			}
			aggs, err := s.bucketAggs(ctx, p.ID, dayStart.Unix(), dayEnd.Unix())
			if err != nil {
				return nil, err
			}
			hourly := make(map[int]bucketAgg)
			for b, a := range aggs {
				h := time.Unix(b, 0).In(time.Local).Hour()
				cur := hourly[h]
				cur.merge(a)
				hourly[h] = cur
			}
			for h, a := range hourly {
				if a.n > 0 && h >= 0 && h < 24 {
					series.Data[h] = valuePtr(avgWatts(a.sumMW, a.n))
				}
			}
			resp.Series = append(resp.Series, series)
		}
	}

	return resp, nil
}

// Close flushes the open buckets and closes the SQLite database.
func (s *Storage) Close() error {
	if s.retentionCancel != nil {
		s.retentionCancel()
		<-s.retentionDone
	}

	s.mu.Lock()
	for id, p := range s.plugs {
		if p.hasOpen {
			s.closed = append(s.closed, closedBucket{plug: id, start: p.openStart, agg: p.open})
			p.hasOpen = false
			p.open = bucketAgg{}
			p.dirty = true
		}
	}
	s.mu.Unlock()

	close(s.stopFlush)
	<-s.flushDone
	return s.db.Close()
}
