# Etapo HTTP API

This is the HTTP API of etapo as it behaves in the current code (version 1.9.0). It is the API the dashboard uses, and it can be used from scripts too. There is no authentication; see [Security](#security).

- Base URL: `http://<host>:<port>` (default port `8080`, see `server.port` in [configuration.md](configuration.md)).
- Responses are JSON (`application/json`), except errors, which are plain text (see [Errors](#errors)).
- Every response has `Access-Control-Allow-Origin: *`. Allowed methods: `GET`, `POST`, `OPTIONS`.
- Times in JSON use RFC 3339. Values from the database (`created_at`, `updated_at`, `latest_reading.timestamp`, `last_updated`) are in UTC. History points carry the server's local offset.
- Money values are in the currency set by `billing.currency`, and are shown without the symbol.

Data and the Tapo side are described in [tapo-api.md](tapo-api.md). The planned storage and read changes are in [data-design.md](data-design.md).

## Contents

- [Common parameters](#common-parameters)
- [Endpoints](#endpoints)
- [Response objects](#response-objects)
- [Errors](#errors)
- [Dashboard usage](#dashboard-usage)
- [Known issues](#known-issues)
- [Security](#security)

---

## Common parameters

| Parameter | Used by | Format | Default | Valid values | Error when invalid |
|---|---|---|---|---|---|
| `nights` | `/api/plugs`, `/api/plugs/{id}`, `/api/summary` | integer | `7` | 1 to 365 | `400 nights must be a whole number from 1 to 365` |
| `date` | `/api/breakdown` (`range=today`), `/api/leaderboard/day`, `/api/plugs/{id}/history` (`range=day` or `today`) | `YYYY-MM-DD`, local date | today | not after today | `400 date must be formatted as YYYY-MM-DD`, or `400 cannot show a day or month that has not started yet` |
| `month` | `/api/breakdown` (`range=month`), `/api/leaderboard/month`, `/api/plugs/{id}/history` (`range=month`) | `YYYY-MM`, local | current month | not after the current month | `400 month must be formatted as YYYY-MM`, or the same "not started yet" message |

Standby window (`standby.start`, `standby.end`) comes from `config.yaml`, and is shown as `standby_window_start` and `standby_window_end` in `/api/summary`.

---

## Endpoints

### Overview

| Method | Path | Purpose | Dashboard |
|---|---|---|---|
| GET | `/health` | Liveness check | — |
| GET | `/api/plugs` | All plugs with latest reading and analytics | Page load, after plug moves and power changes |
| GET | `/api/plugs/{id}` | One plug, same shape as an item of `/api/plugs` | — |
| GET | `/api/plugs/{id}/history` | Power over time for one plug | Device timeline |
| POST | `/api/plugs/{id}/power` | Switch a plug on or off | Turn ON / Turn OFF |
| POST | `/api/plugs/{id}/config-ip` | Write a moved plug's address to `config.yaml` | Update config.yaml |
| GET | `/api/summary` | Totals for the KPI bar | Page load, every reading, every 30 s |
| GET | `/api/breakdown` | Stacked chart data | Page load, every 30 s |
| GET | `/api/leaderboard/day` | Per-plug figures for one day | Page load, every 30 s |
| GET | `/api/leaderboard/month` | Per-plug figures for one month | Every 30 s |
| GET | `/api/events` | Live updates (Server-Sent Events) | Page load |

---

### GET `/health`

```json
{"status": "ok", "app": "etapo"}
```

---

### GET `/api/plugs`

All configured plugs, sorted by `name`. Each item is a [PlugWithLatest](#plugwithlatest).

Query: `nights` (see [Common parameters](#common-parameters)).

```bash
curl 'http://localhost:8080/api/plugs?nights=1'
```

Errors: `400` (bad `nights`), `500` (database error). Other methods: `405`.

---

### GET `/api/plugs/{id}`

One plug, with the same fields as an item of `/api/plugs`.

Query: `nights`.

Errors: `404 Plug not found`, `400`, `500`, `405`.

---

### GET `/api/plugs/{id}/history`

Average power over time for one plug. Each point is one bucket. Buckets without readings have `power_watts: null`, so the chart shows a gap.

Query `range`:

| `range` | Period | Bucket | Points | Parameter | Used by dashboard |
|---|---|---|---|---|---|
| `24h` (default if missing) | last 24 hours, ending now | 5 minutes | about 289 | — | yes |
| `1h` | last hour, ending now | 5 minutes | about 13 | — | no (API only) |
| `day` or `today` | one local day | 5 minutes | up to 288 | `date` (default today) | yes |
| `7d` | last 7 days, ending now | 1 hour | about 169 | — | yes (in the standby view when `nights` is 7) |
| `30d` | last 30 days, ending now | 4 hours | about 181 | — | no (API only) |
| `month` | one local month | 4 hours | up to 186 | `month` (default current) | yes |

Notes:

- An unknown `range` is an error: `400 range must be 1h, 24h, day, 7d, 30d or month`.
- The buckets are averages of the power samples in the bucket. There is no maximum or sample count in the response.
- Samples are summed into 5-minute buckets when they are stored. The shortest bucket is therefore 5 minutes, and the newest bucket is shown from the last flush (about 30 seconds).

Response: an array of [HistoryPoint](#historypoint). An empty array if there is no data.

```bash
curl 'http://localhost:8080/api/plugs/kitchen-fridge/history?range=7d'
```

Errors: `400` (bad `date` or `month`), `500`, `405`.

---

### POST `/api/plugs/{id}/power`

Switches the relay of a plug. The request is sent to the plug, the stored state is updated, and a poll is started in the background.

Body:

```json
{"on": true}
```

Response:

```json
{"success": true, "plug_id": "kitchen-kettle", "on": true}
```

Errors:

| Status | Message | Cause |
|---|---|---|
| 400 | `Invalid request body: expected {"on": true\|false}` | Body is not JSON. |
| 503 | `Collector not configured in serve-only mode` | Started with `etapo serve`, which has no collector. |
| 404 | `Plug not found` | Unknown `{id}`. |
| 500 | `Failed to toggle socket: …` | The plug did not accept the command, for example it is offline. |

The field `on` is required. A body without it, such as `{}`, is rejected with 400.

---

### POST `/api/plugs/{id}/config-ip`

Writes the address a plug was found at into `config.yaml`. Only the `ip` line of that plug is changed. See [docs/configuration.md](configuration.md#what-etapo-writes-to-the-file) and ADR-008.

Body:

```json
{"ip": "192.168.20.104"}
```

The `ip` must be the address the plug is at now. This protects against writing an address that changed again.

Response:

```json
{"success": true, "plug_id": "meterkast", "ip": "192.168.20.104"}
```

Errors:

| Status | Message | Cause |
|---|---|---|
| 400 | `Invalid request body: expected {"ip": "x.x.x.x"}` | Missing or empty `ip`. |
| 404 | `Plug is not a Tapo plug in the configuration` | `{id}` is not a `tapo` plug in `config.yaml`. |
| 404 | `Plug not found` | `{id}` is not in the database. |
| 409 | `The plug is now at … Reload the dashboard.` | The plug moved again. |
| 409 | `config.yaml already uses this address` | Nothing to change. |
| 409 | `etapo was started without a configuration file…` | No `--config` path. |
| 500 | `Failed to update config.yaml: …` | Write failed. The file is not changed. |

---

### GET `/api/summary`

Totals for the top KPI bar.

Query: `nights`.

Example response (values are illustrative):

```json
{
  "total_power_watts": 183.4,
  "live_burn_rate_per_hour": 0.059,
  "total_energy_today_kwh": 2.134,
  "total_cost_today": 0.68,
  "total_energy_month_kwh": 41.22,
  "estimated_cost_month": 13.19,
  "total_standby_watts": 9.8,
  "standby_data_available": true,
  "standby_window_start": "02:00",
  "standby_window_end": "05:00",
  "annual_standby_waste": 27.41,
  "currency": "EUR",
  "active_plugs_count": 8,
  "total_plugs_count": 8,
  "offline_after_sec": 60,
  "last_updated": "2026-11-07T22:41:03.123Z"
}
```

| Field | Unit | Calculation |
|---|---|---|
| `total_power_watts` | W | Sum of `latest_reading.power_watts` over plugs that are on **and** online. One decimal. |
| `live_burn_rate_per_hour` | currency/h | `total_power_watts / 1000 × billing.cost_per_kwh`. Three decimals. |
| `total_energy_today_kwh` | kWh | Sum of `energy_today_kwh` over all plugs with a reading, including switched-off plugs. Three decimals. |
| `total_cost_today` | currency | `total_energy_today_kwh × cost_per_kwh`. |
| `total_energy_month_kwh` | kWh | Sum of `energy_month_kwh` over all plugs with a reading, including switched-off plugs. |
| `estimated_cost_month` | currency | Month-to-date energy × `cost_per_kwh`. Not a projection. |
| `total_standby_watts` | W | Sum of `standby_watts` of all plugs. A plug with no data adds 0. |
| `standby_data_available` | bool | Any plug has readings in the standby window. |
| `annual_standby_waste` | currency/year | Sum of `annual_standby_cost` of all plugs. |
| `active_plugs_count` | count | Visible plugs. Equal to `total_plugs_count`. |
| `total_plugs_count` | count | Visible plugs. Plugs removed from `config.yaml` are hidden; their data is kept. |
| `offline_after_sec` | s | A plug is offline when its latest reading is older than this. |
| `last_updated` | UTC time | When the summary was computed. |

Errors: `400` (bad `nights`), `500`, `405`.

---

### GET `/api/breakdown`

Chart data for the stacked chart.

Query:

| Parameter | Values | Default | Notes |
|---|---|---|---|
| `range` | `today`, `month` | `today` | Any other value is treated as `today`. |
| `date` | `YYYY-MM-DD` | today | Used with `range=today`. |
| `month` | `YYYY-MM` | current month | Used with `range=month`. |
| `by` | `plug` | `plug` | The only value. Any other value is a 400. |

Response:

```json
{
  "range": "today",
  "labels": ["00:00", "01:00", "…", "23:00"],
  "series": [
    {"id": "kitchen-fridge", "label": "Kitchen Refrigerator", "color": "#10b981", "data": [4.6, 4.7, null, "…"]}
  ]
}
```

- `range=today`: 24 labels, one per local hour. `data` is the average power in that hour, in W, one decimal. `null` if there were no readings in that hour.
- `range=month`: one label per day up to today (for the current month) or to the end of the month. `data` is the device's daily counter (`energy_today_kwh` at the last sample of the day), in kWh, two decimals.
- `color` cycles through eight colours in plug order.

Errors: `400` (bad `date` or `month`), `500`, `405`.

---

### GET `/api/leaderboard/day`

Per-plug figures for one local day. Array of [PeriodStats](#periodstats), sorted by plug name. Empty array if there are no plugs.

Query: `date` (default today).

```json
[
  {"plug_id": "kitchen-fridge", "peak_watts": 112.4, "energy_kwh": 1.87, "estimated_cost": 0.6}
]
```

- `peak_watts`: maximum power of the day, one decimal.
- `energy_kwh`: maximum `energy_today_kwh` of the day, two decimals.
- `estimated_cost`: `energy_kwh × cost_per_kwh`, two decimals.

Errors: `400` (bad `date`), `500`, `405`.

---

### GET `/api/leaderboard/month`

As `/api/leaderboard/day`, for one local month. `energy_kwh` is the maximum `energy_month_kwh` in the month. Query: `month` (default current).

Errors: `400` (bad `month`), `500`, `405`.

---

### GET `/api/events`

Server-Sent Events stream of live updates. The first line is the comment `: connected`.

| Event | Data | When |
|---|---|---|
| `reading` | [EnergyReading](#energyreading) (without `id`) | After every poll of every plug, about every 10 seconds per plug. |
| `plug_moved` | `{"plug_id": "…", "ip": "…"}` | When a plug is found at a new address. |

```bash
curl -N 'http://localhost:8080/api/events'
```

The stream stays open until the client disconnects.

---

## Response objects

### Plug

| Field | Type | Notes |
|---|---|---|
| `id` | string | From `config.yaml`. Used in URLs. |
| `name` | string | Shown on the dashboard. |
| `model` | string | Defaults to `P115`. |
| `category` | string | Defaults to `General`. |
| `driver_type` | string | `tapo` or `mock`. |
| `ip` | string | Address the plug was last seen at. |
| `mac` | string | Learned from the plug. Omitted until the first successful poll. |
| `is_active` | bool | Always `true` today. |
| `is_on` | bool | Relay state from the last poll, or from the last `POST …/power`. |
| `created_at`, `updated_at` | time | UTC. |

### PlugWithLatest

Everything in [Plug](#plug), plus:

| Field | Type | Notes |
|---|---|---|
| `online` | bool | `latest_reading` is newer than `offline_after_sec`. |
| `config_ip` | string | Address in `config.yaml`. Only for `tapo` plugs. Omitted otherwise. |
| `latest_reading` | [EnergyReading](#energyreading) | Omitted if the plug has no readings. |
| `peak_watts_today` | W | Highest reading since local midnight. Falls back to the latest reading if there is none. One decimal. |
| `avg_watts_today` | W | Average of readings since local midnight. 0 if none. One decimal. |
| `standby_watts` | W | Average power inside the standby window (`standby.start` to `standby.end`) over the last `nights` windows that have ended. One decimal. See the rule below. |
| `has_standby_data` | bool | There are readings in the standby window. |
| `annual_standby_cost` | currency/year | `standby_watts × 24 × 365.25 / 1000 × cost_per_kwh`, two decimals. |
| `estimated_month_cost` | currency | `latest_reading.energy_month_kwh × cost_per_kwh`. Month to date. |

**Standby rule.** The reference is the most recent standby window that has ended. After today's window has ended, that is today's window. During the window, or before it starts, it is yesterday's. `nights=N` averages the N windows ending with the reference. A window that wraps past midnight (for example 23:00 to 01:00) counts as ended at its end time.

### EnergyReading

| Field | Unit | Notes |
|---|---|---|
| `id` | — | Database id. Omitted on live `reading` events. |
| `plug_id` | — | |
| `timestamp` | UTC | Time of the poll. |
| `power_watts` | W | From `current_power` (mW / 1000). |
| `voltage_v` | V | **Not measured.** P115 does not report voltage; the value is 230. |
| `current_ma` | mA | **Derived:** `power_watts / voltage_v × 1000`. |
| `energy_today_kwh` | kWh | Device counter, since device-local midnight. |
| `energy_month_kwh` | kWh | Device counter, since the start of the device-local month. |

### SystemSummary

See [GET /api/summary](#get-apisummary).

### HistoryPoint

| Field | Type | Notes |
|---|---|---|
| `timestamp` | time | Start of the bucket, with the server's UTC offset. |
| `power_watts` | number or `null` | Average power of the bucket, W. `null` for an empty bucket. |

### BreakdownResponse

| Field | Type | Notes |
|---|---|---|
| `range` | string | `today` or `month`. |
| `labels` | array of string | See [GET /api/breakdown](#get-apibreakdown). |
| `series` | array of `{id, label, color, data}` | `data` has one value per label, or `null`. |

### PeriodStats

| Field | Unit | Notes |
|---|---|---|
| `plug_id` | — | |
| `peak_watts` | W | |
| `energy_kwh` | kWh | |
| `estimated_cost` | currency | |

---

## Errors

Errors are plain text in the body, with an HTTP status.

| Status | When |
|---|---|
| 400 | Invalid parameter or body (messages in the tables above). |
| 404 | Unknown plug (`/api/plugs/{id}`, `…/config-ip`), or an unknown path under `/api/plugs/`. |
| 405 | Wrong method. Each route accepts only its own method, except `/health` and `/api/events`, which do not check it. |
| 409 | `config-ip`: conflicts (see the table above). |
| 500 | Database or Tapo error. |
| 503 | `POST …/power` in `etapo serve` mode. |

`OPTIONS` requests get `200` with the CORS headers.

---

## Dashboard usage

What the dashboard in `internal/web/static/index.html` requests, and when. The numbers show how often each request runs, which is what makes the load on the host running etapo.

| Trigger | Requests |
|---|---|
| Page load | `/api/summary?nights=1`, `/api/plugs?nights=1`, `/api/breakdown?range=today&date=…`, `/api/leaderboard/day?date=…`, then `/api/events` |
| Every SSE `reading` event | `/api/summary?nights=…`, at most once every 10 seconds |
| Every 30 s | `/api/summary`, `/api/breakdown`, `/api/leaderboard/…` |
| Every 5 s | None. The client compares timestamps with its own clock. |
| Switching a plug | `POST …/power`, then `/api/plugs` and `/api/summary` |
| Opening the device timeline or changing its range | `/api/plugs/{id}/history` |
| Standby 1 night / 7 nights | `/api/summary` and `/api/plugs` with the new `nights` |
| Leaderboard or breakdown navigation | `/api/leaderboard/…` or `/api/breakdown` |

Cost of the heaviest calls before 1.10.0 on a MacBook with eight plugs and 30 days of 10-second readings (about 2 million rows): `/api/summary?nights=1` 0.29 s, `?nights=7` 1.36 s, `/api/breakdown?range=month` 2.9 s, `/api/leaderboard/month` 1.1 s, `/api/plugs/{id}/history?range=30d` 0.8 s. The full table is in [data-design.md](data-design.md#22-macbook-synthetic-30-day-dataset).

---

## Known issues

1. `active_plugs_count` always equals `total_plugs_count`, because every visible plug is active.
2. `voltage_v` is a constant 230 and `current_ma` is derived, because the P115 does not report either. Both are kept for compatibility and will be removed later.
3. `estimated_month_cost` is month to date, not a projection.

## Security

- The API has **no authentication**. Anyone who can reach the port can read the data and switch the plugs.
- Every response sends `Access-Control-Allow-Origin: *`, and `POST` is allowed. A web page open in a browser on the same network can therefore call `POST /api/plugs/{id}/power`. Whether the browser lets a public page reach a LAN address depends on its private-network rules. Restricting the origin, or requiring a custom header on POST requests, would close this.
- `/api/plugs/{id}/config-ip` writes to `config.yaml`. It is guarded by the address check and by the requirement that the file was given at startup.
