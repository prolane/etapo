# Data storage and reads: design proposal

- **Status:** accepted and implemented on branch `feat/5-minute-rollups` (1.10.0). See ADR-011 in `DECISIONS.md`. The flash-write measurement in section 9 is still to be done on a Raspberry Pi 3.
- **Decisions:** [section 10](#10-decisions).
- **Date:** 2026-11-07
- **Scope:** how etapo stores what the plugs return, and how the API reads it back for the dashboard.
- **Related:** [tapo-api.md](tapo-api.md) (what the plugs return), [api.md](api.md) (the API as it is today), ADR-002 (SQLite and batching), ADR-009 (retention).

---

## 1. Summary

**Problem.** With eight plugs, etapo writes every 10-second reading to SQLite, keeps them for up to 1095 days (the setting of the reference installation; the `config.example.yaml` default is 90), and recomputes every dashboard figure from those raw rows. On a Raspberry Pi 3 this costs much more than the data is worth:

- The process writes about **680 to 870 MB per day** to its SD card (three measurements, section 2.1). The data itself is about 24 MB per day, so the writes are roughly 30 times the data.
- At full rate and 1095 days, the database grows to about **26 GB**.
- The dashboard asks for `/api/summary` on every live reading, and each request runs the full per-plug analytics over raw rows.

**Proposal.**

1. **Store what the dashboard shows, not every poll.** The finest resolution any view needs is 5 minutes. Store one row per plug per 5-minute bucket, made of integer sums, a maximum and a count, so buckets can be merged. Store one daily row per plug with the device counters. Stop storing raw 10-second readings.
2. **Keep the live state of each plug in one small table** (8 rows), rewritten on each flush.
3. **Read from those tables.** The summary and the plug list become a few small indexed reads, cached for a few seconds. Breakdown and leaderboard read daily rows.
4. **Stop asking for a summary on every reading.** Push it with the live events, or throttle the requests.
5. **Fix the 30-second cut-off of the live stream** (a separate bug, see section 8).

**Expected effect** (estimates, to be verified on a Raspberry Pi 3 with the method in section 9):

| | Now | Proposed |
|---|---|---|
| Rows per day (8 plugs) | 69 120 | 2 304 |
| Data per day | about 24 MB | about 0.12 MB |
| Data at 1095 days | about 26 GB | about 140 MB |
| Flash writes per day | 680 to 870 MB (measured) | tens of MB (estimate) |
| Summary cost | rows since midnight and the standby window, per plug, per request | 8 state rows and a few hundred bucket rows, cached |

---

## 2. Measurements

### 2.1 Raspberry Pi 3, reference installation (v1.9.0, 2026-11-07)

Read-only checks: `ls`, `systemctl`, a read-only SQLite query, `/proc/<pid>/status` and `/proc/<pid>/io`.

| Item | Value |
|---|---|
| Database file | 62.8 MB, 15 333 pages of 4 KB, 0 free pages |
| Rows | 183 973, from 2026-10-29 |
| Plugs with data | 2 plugs at full rate since 29 Oct. 6 plugs since about 10:00 on 2026-11-07. |
| Bytes per row | 341 (file size / rows, includes both indexes) |
| Configuration | poll 10 s, flush 30 s, buffer 100, retention 1095 days, 8 `tapo` plugs |
| Full-rate data | 8 × 8 640 = 69 120 rows per day, about 23.6 MB per day, about 25.8 GB at 1095 days |
| Idle process (no dashboard open) | 23 MB RSS, 5 CPU ticks in 10 s (0.5 %), load average 0.00 |
| SD writes, 61 s window | 0.50 MB, about 680 MB per day |
| SD writes, 180 s window | 1.90 MB, about 870 MB per day |
| SD writes, since start (17.7 min) | 9.3 MB, about 750 MB per day |

The write figures come from `write_bytes` in `/proc/<pid>/io`, which counts the bytes the process sent to the storage layer. They include the WAL and its checkpoints. The shared-memory file may be included too; this was not checked.

### 2.2 MacBook, synthetic 30-day dataset

Same code and SQLite driver, eight plugs, 30 days at 10 s: 2 073 600 rows, 620 MB (299 bytes per row). The timings are the median of three requests, through the real HTTP handlers.

| Request | UI use | Response size | Time |
|---|---|---|---|
| `/api/summary?nights=1` | every reading, every 30 s | 454 B | 288 ms |
| `/api/summary?nights=7` | when the 7-night view is selected | 454 B | 1 358 ms |
| `/api/plugs?nights=1` | page load | 4.8 kB | 314 ms |
| `/api/breakdown?range=today` | every 30 s | 1.6 kB | 1 524 ms |
| `/api/breakdown?range=month` | when the month view is selected | 0.8 kB | 2 901 ms |
| `/api/leaderboard/day` | every 30 s | 0.6 kB | 155 ms |
| `/api/leaderboard/month` | when the month view is selected | 0.6 kB | 1 108 ms |
| `/api/plugs/{id}/history?range=24h` | timeline | 20.7 kB | 26 ms |
| `/api/plugs/{id}/history?range=7d` | timeline | 12.3 kB | 188 ms |
| `/api/plugs/{id}/history?range=30d` | API only | 13.3 kB | 807 ms |
| `/api/plugs/{id}/history?range=month` | timeline | 3.1 kB | 188 ms |

Most of the time goes to `GetPlugs`. Every summary and breakdown request runs it for every plug: a latest-reading lookup and three aggregate queries per plug. The 7-night query applies `strftime` with the local time zone to every row. `GetBreakdown` runs it too, although it only needs the plug list.

### 2.3 What one open dashboard costs

The dashboard requests `/api/summary` once per `reading` event. Requests are single-flight, so one runs while the next waits, and the waiting request runs right after. v1.9.0 limited this to one summary and one plugs request at a time (PR #31). That caps how many run in parallel, not how many are requested. Assuming two summary runs per 10-second poll, plus a breakdown and a leaderboard every 30 seconds, a MacBook spends about **1 s of CPU per 10 s** with `nights=1` and about **3 s** with `nights=7`. A Raspberry Pi 3 is roughly 10 to 20 times slower per core (an estimate, not measured). At that speed the summary alone would need more than one core, which would explain a loaded device while a dashboard is open. This was not measured with a browser attached. With no dashboard open, the reference device is idle (section 2.1).

---

## 3. What the sockets return, and what to keep

From [tapo-api.md](tapo-api.md). Each poll returns one set of values:

| Tapo value | Changes | Keep? | Where it goes |
|---|---|---|---|
| `current_power` (mW) | every poll | Yes, as sum, maximum and count per 5-minute bucket. The current value is kept as live state. | `samples_5m`, `plug_state` |
| `today_energy` (Wh) | about every few minutes at 10 W | Yes, the value at the last sample of each day | `daily`, `plug_state` |
| `month_energy` (Wh) | as above | Yes, the value at the last sample of each day | `daily`, `plug_state` |
| `today_runtime` (min) | every minute | Yes, last value of the day. Used for "runtime today" (it replaces the 3 W estimate). | `daily`, `plug_state` |
| `month_runtime` (min) | every minute | No. Not used by the dashboard. | — |
| `device_on` | on change | Yes, live state | `plug_state.is_on` |
| `rssi` | every poll | Optional, live state only | `plug_state.rssi_dbm` |
| `voltage`, `current` | never returned by P115 | No. The etapo columns hold a constant and a derived value. | Drop the columns (see section 7) |
| `today_energy_mwh`, `month_energy_mwh` | every poll | No. Wh is precise enough. | — |
| `electricity_charge`, `local_time`, `nickname`, `ssid`, location, identifiers | | No | — |

Two points decide the design:

- **The counters are cumulative within a day.** Only the last value of the day is needed for daily energy, so storing one per poll repeats the same value about 288 times a day.
- **The sum of microwatt samples is exact.** Averages, peaks and energies computed from 5-minute sums are identical to the current results computed from raw rows. A regression test can therefore compare old and new output exactly.

---

## 4. What the dashboard needs

From the code in `internal/web/static/index.html` and the API.

| View | Request | Refresh | Needs | Finest resolution |
|---|---|---|---|---|
| Live watts, online and on/off per plug | SSE `reading` | every poll (10 s) | latest power, state | 10 s (unchanged) |
| KPI bar | `/api/summary` | every reading today, every 30 s | live total, today and month energy, standby average, cost | live value; counters |
| Plug table | `/api/plugs` | page load and changes | latest, peak and average today, runtime, standby, month cost | live; daily |
| Leaderboard, day | `/api/leaderboard/day` | 30 s | peak W, kWh, cost for one day | one day |
| Leaderboard, month | `/api/leaderboard/month` | 30 s | peak W, kWh, cost for one month | one day |
| Breakdown, today | `/api/breakdown?range=today` | 30 s | average W per hour | one hour |
| Breakdown, month | `/api/breakdown?range=month` | 30 s | kWh per day | one day |
| Timeline, Day and Last 24 h | `/api/plugs/{id}/history` | when opened or changed | average W | **5 minutes** (288 points) |
| Timeline, Last 7 days | same | | average W | 1 hour (168 points) |
| Timeline, Month | same | | average W | 4 hours (about 180 points) |
| Standby baseline | `/api/summary`, `/api/plugs` | when changed | average W in the window over N nights | 5 minutes, averaged |
| API only: `range=30d` | `/api/plugs/{id}/history` | none | average W | 4 hours |
| API only: `range=1h` and the default | `/api/plugs/{id}/history` | none | average W | 1 minute |

Conclusions:

- **No view shows anything finer than 5 minutes** except the live value, which comes from memory.
- **Peaks** are taken from every 10-second sample, so the maximum per bucket keeps the peak exact. A kettle spike of about 2 kW lasting minutes is caught by 10-second polls, and the 5-minute bucket keeps its maximum.
- **1-minute history** (`range=1h`) is not used by the dashboard. Dropping it, or serving 5-minute buckets for it, is a decision (section 10).

---

## 5. Proposed storage

### 5.1 Tables

All timestamps are Unix seconds in UTC. Power is in integer milliwatts and energy in integer watt-hours, as the plugs report them. The `WITHOUT ROWID` tables are clustered on their primary key, so one plug's buckets are stored together and an insert touches few pages.

```sql
-- Live state: one row per plug, rewritten on each flush
CREATE TABLE plug_state (
  plug_id           TEXT PRIMARY KEY,
  sampled_at        INTEGER NOT NULL,   -- last successful poll
  power_mw          INTEGER NOT NULL,
  is_on             INTEGER NOT NULL,
  rssi_dbm          INTEGER,
  energy_today_wh   INTEGER NOT NULL,   -- device counter
  energy_month_wh   INTEGER NOT NULL,   -- device counter
  runtime_today_min INTEGER NOT NULL    -- device counter
);

-- Five-minute buckets of samples. Sums and counts, so partial buckets can be merged.
CREATE TABLE samples_5m (
  plug_id  TEXT    NOT NULL,
  bucket   INTEGER NOT NULL,            -- start of the bucket, multiple of 300
  sum_mw   INTEGER NOT NULL,            -- sum of power samples
  max_mw   INTEGER NOT NULL,
  n        INTEGER NOT NULL,            -- number of samples
  PRIMARY KEY (plug_id, bucket)
) WITHOUT ROWID;

-- One row per plug per local day
CREATE TABLE daily (
  plug_id           TEXT    NOT NULL,
  day               TEXT    NOT NULL,   -- local date, YYYY-MM-DD
  energy_today_wh   INTEGER NOT NULL,   -- device counter at the last sample of the day
  energy_month_wh   INTEGER NOT NULL,   -- device counter at the last sample of the day
  runtime_today_min INTEGER NOT NULL,   -- device counter at the last sample of the day
  peak_mw           INTEGER NOT NULL,
  sum_mw            INTEGER NOT NULL,
  n                 INTEGER NOT NULL,
  PRIMARY KEY (plug_id, day)
) WITHOUT ROWID;

CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);  -- schema_version
```

The existing `plugs` table stays as it is.

Writing a bucket with merge (so a restart within the same bucket adds to it rather than replacing it):

```sql
INSERT INTO samples_5m (plug_id, bucket, sum_mw, max_mw, n) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (plug_id, bucket) DO UPDATE SET
  sum_mw = sum_mw + excluded.sum_mw,
  max_mw = max(max_mw, excluded.max_mw),
  n      = n + excluded.n;
```

### 5.2 Sizes

| Table | Rows per day | About per row | About per day | At 1095 days |
|---|---|---|---|---|
| `energy_readings` (now) | 69 120 | 341 B | 23.6 MB | 25.8 GB |
| `samples_5m` | 2 304 | 50 B | 0.12 MB | 0.13 GB |
| `daily` | 8 | 60 B | 0.5 kB | 0.5 MB |
| `plug_state` | — (8 rows, rewritten) | | | |

### 5.3 Write path

Each poll (every 10 s, as now):

1. Update the in-memory state of the plug.
2. Add the sample to the open 5-minute bucket of the plug: `sum_mw += power`, `max_mw = max(…)`, `n++`.
3. Send the `reading` event (unchanged).

Each flush (every `flush_interval_sec`, 30 s by default), in one transaction:

1. Upsert the 8 `plug_state` rows.
2. For each bucket that closed since the last flush, upsert its `samples_5m` row, and upsert the `daily` row with the latest counters, `peak_mw = max(…)`, and the added `sum_mw` and `n`.

A bucket closes when the first poll of the next 5 minutes arrives. On shutdown, the open buckets are flushed too, and the merge rule above makes that safe.

Counters: the device counter is taken as it is. If it goes down within a day (a device restart), the last value wins. No delta arithmetic is needed.

Write volume (estimate): `plug_state` is one page per flush, about 2 880 flushes per day, so roughly 12 MB per day. The bucket commits are 288 per day, each touching about 12 pages, so roughly 14 MB per day. Checkpoints copy some pages again. The total should be tens of MB per day, against 680 to 870 MB now. Section 9 has the measurement.

`max_buffer_size` no longer controls anything, because nothing is written per poll. It can be kept as a deprecated setting or reused as the limit of unsent buckets.

### 5.4 Retention

- `samples_5m` and `daily` use `storage.retention_days`, as now. The purge runs in the writer process only (ADR-009). Each delete uses the primary key, `WHERE plug_id = ? AND bucket < ?`, so no extra index is needed.
- At 1095 days the 5-minute data is about 140 MB, so the current 3-year setting costs little. The daily rows are tiny and can use the same setting.
- Raw 10-second readings are not kept. A short raw window is possible later if you want one (section 10).

### 5.5 Migration of existing data

Done once, when the new version starts for the first time with the old tables present. Each step is idempotent.

1. Copy the database file to a backup next to it (the reference database is 63 MB).
2. Create the new tables (`CREATE TABLE IF NOT EXISTS`) and write `schema_version`.
3. Backfill `samples_5m` from `energy_readings`:

   ```sql
   INSERT OR IGNORE INTO samples_5m (plug_id, bucket, sum_mw, max_mw, n)
   SELECT plug_id,
          CAST(strftime('%s', substr(timestamp, 1, 19)) AS INTEGER) / 300 * 300,
          SUM(CAST(ROUND(power_watts * 1000) AS INTEGER)),
          MAX(CAST(ROUND(power_watts * 1000) AS INTEGER)),
          COUNT(*)
   FROM energy_readings
   GROUP BY plug_id, 2;
   ```

   The timestamp text is UTC, so `strftime` without a zone modifier is correct.

4. Backfill `daily` by local day (`strftime('%Y-%m-%d', substr(timestamp, 1, 19), 'localtime')`), taking the counters from each day's last reading, the maximum power as the peak, and the sums of the samples.
5. Keep `energy_readings` for a verification period, with no new writes. Then drop it, and shrink the file once with `VACUUM`. That is a one-time exception to ADR-009, which avoids `VACUUM` in normal operation. It is a decision for you (section 10).

Step 3 uses `INSERT OR IGNORE` so that a migration that is interrupted and rerun does not double the sums.

---

## 6. Read path

| Endpoint | Source | Expected cost |
|---|---|---|
| `/api/plugs` | `plug_state` (8 rows), `daily` (today), `samples_5m` for the standby window | a few ms, cached 5 s |
| `/api/plugs/{id}` | as above, one plug | a few ms |
| `/api/summary` | as `/api/plugs`, summed | a few ms, cached 5 s, or pushed each poll |
| `/api/plugs/{id}/history` | `samples_5m` by plug and time range, grouped in Go to 5 min, 1 h or 4 h | up to 8 640 rows for a month, a few tens of ms |
| `/api/breakdown?range=today` | `samples_5m` for one day, grouped by local hour in Go | 288 rows per plug |
| `/api/breakdown?range=month` | `daily` | 31 rows per plug |
| `/api/leaderboard/day` | `daily` (the current day from `plug_state`) | 8 rows |
| `/api/leaderboard/month` | `daily` (energy from the last day of the month) | up to 248 rows |
| `/api/events` | in memory, as now | no database reads |

Notes:

- **Standby:** average = `SUM(sum_mw) / SUM(n)` over the buckets whose local time is in the window. The window test is done in Go, so `strftime` is not run per row.
- **Hour grouping:** done in Go with the local time zone, so 23- and 25-hour days (daylight saving) are handled correctly.
- **Today:** `daily` is at most one bucket (5 minutes) behind, so today's values come from `plug_state`, which is current to the last flush.
- **`etapo serve`** (a separate process) reads the same tables. `plug_state` is at most one flush interval old.

### 6.1 Dashboard changes (proposed)

1. Remove the `fetchSummary()` call from the `reading` handler. Either the server sends a `summary` event after each poll (computed from the cache), or the client throttles its requests to one per 10 seconds.
2. Keep the 30-second refresh for the breakdown and leaderboards, which become cheap.
3. Show "runtime today" from the device counter, with the label changed to match (section 7).

---

## 7. API changes (proposed)

Everything not listed keeps its name and shape.

| # | Change | Breaking? | Reason |
|---|---|---|---|
| 1 | Totals include plugs that are off (`total_energy_today_kwh`, `total_energy_month_kwh`) | Changes numbers | Fixes known issue 3 in [api.md](api.md#known-issues). |
| 2 | `POST …/power` without `on` returns 400 | Yes, for `{}` | Stops `{}` switching a plug off. |
| 3 | `POST …/power` for an unknown plug returns 404 | Status only | Known issue 5. |
| 4 | Unknown `range` in history returns 400 | Yes, for scripts relying on the fallback | Known issue 6. See section 10. |
| 5 | `by=category` on breakdown is implemented or removed | Depends | Known issue 7. See section 10. |
| 6 | `runtime_today_hours` comes from the device's `today_runtime` | Changes numbers | More accurate than the 3 W estimate. See section 10. |
| 7 | `voltage_v` and `current_ma` marked deprecated, then removed | Later | They are not measured. |
| 8 | Plugs no longer in `config.yaml` are hidden from lists and totals (their data is kept) | Yes | Known issue 8. |
| 9 | `nights=1` before the standby window ends shows the previous night | Changes numbers | Known issue 12. See section 10. |
| 10 | History points gain optional `max_watts` and `samples` | Additive | Lets clients see peaks in a bucket. |
| 11 | `storage.max_buffer_size` is deprecated | Config | No per-poll buffer any more. |

---

## 8. Bugs to fix separately

These do not depend on the storage design and can ship as small fixes first.

1. **The live stream is cut after 30 seconds.** `SSEBroker.ServeHTTP` should clear the write deadline (`http.NewResponseController(w).SetWriteDeadline(time.Time{})`) for `/api/events`. This was reproduced with a test: the stream ended at exactly 30 s.
2. **`{}` switches a plug off** (`POST …/power`). Require the field.
3. **Totals drop switched-off plugs' energy** (`GetSummary`).
4. **Unknown plug returns 500** from `POST …/power`.

---

## 9. Acceptance criteria

Measured on a Raspberry Pi 3, with the method of section 2.1, before and after:

| Measure | Now | Target |
|---|---|---|
| Flash writes, `write_bytes` per day, dashboard closed | 680 to 870 MB | under 100 MB |
| Flash writes, dashboard open | not measured | under 100 MB |
| Database growth per day | about 24 MB | under 0.2 MB |
| `/api/summary` and `/api/plugs`, eight plugs, 1095 days of data | not measured (MacBook: 288 ms and 314 ms at 30 days) | under 50 ms on a Raspberry Pi 3, a few ms on a MacBook |
| etapo CPU with dashboard open | not measured (estimate: over one core) | under 5 % |
| Live stream | cut after 30 s | open for hours |

Correctness checks:

- For a migrated copy of the reference database, old and new responses match for `/api/leaderboard/*`, `/api/breakdown`, the 5-minute history, the standby averages and the peaks. Differences are allowed only where section 7 says so.
- Unit tests: bucket merge after a restart, counter reset within a day, the standby window over midnight, a daylight-saving day in the breakdown, and the migration backfill.
- The end-to-end tests (`make e2e`) keep passing.

---

## 10. Decisions

### 10.1 Decided (2026-11-08)

- **Raw readings:** not kept. No 10-second history.
- **Retention:** 1095 days for the 5-minute data, as now.
- **Migration:** the old `energy_readings` table is removed. Before that, the database file is backed up so the raw data can still be investigated.
- **Unknown history `range`:** returns 400.
- **`range=1h`:** kept, served from 5-minute buckets for the last hour.
- **`by=category`:** removed. Only `by=plug` remains, and an unknown `by` returns 400.
- **Removed plugs:** hidden from lists and totals. Their data is kept.
- **Standby for the KPI and the plug table:**
  - After the window has ended today, show the average power inside today's window.
  - During the window, or before it starts, show the average inside yesterday's window.
  - `nights=N` averages the N windows ending with that reference night.
- **Release:** minor, 1.10.0.

### 10.2 Open

- **Runtime widget:** whether to keep it. If it stays, it shows the device's `today_runtime`.
- **Two etapo installations on the same plugs:** each plug gets two sessions and each installation keeps its own database. Behaviour with two concurrent sessions is not tested (see [tapo-api.md](tapo-api.md#8-known-quirks-and-risks)).

---

## 11. Implementation order

1. The small fixes from section 8, each with a test and a CHANGELOG entry.
2. The new tables, the collector's bucket aggregation and the flush, with the migration and its tests.
3. The read path, checked against the old code on a copy of a real database (section 9).
4. The dashboard: no summary request per reading.
5. Measure on a Raspberry Pi 3 with the method of section 2.1, and record the results here.
6. Update `ARCHITECTURE.md`, `docs/configuration.md` (retention and flush settings), and add an ADR to `DECISIONS.md` for the 5-minute rollups.
