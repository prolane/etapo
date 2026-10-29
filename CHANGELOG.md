# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- The device detail timeline can be zoomed: drag across the chart to select a period. A scrollbar moves the zoomed period along the data, and Reset zoom returns to the full period.

### Changed
- The device leaderboard has a Day view with a date picker, like the energy breakdown. Its columns follow the selected period: Day (kWh) and Est. Day Cost for a day, Month (kWh) and Est. Month Cost for a month. Live Watts is only shown for today.
- The device detail timeline has a Day view with a date picker, replacing Today. Opening a device from the leaderboard's Day view shows that day.

### Fixed
- Without a date, the breakdown and leaderboard APIs start at local midnight of the current day or month, instead of the current time.

## [1.1.0] - 2026-10-29

### Changed
- The device detail chart has Today, Last 24 h (5-minute average), Last 7 days (hourly) and Month (4-hour, with a month picker) views. Gaps without readings are shown as breaks. It opens on the period of the widget it was opened from: the standby widget's night or 7-night view, or the leaderboard's Today or Month view.
- The device leaderboard is shown above the standby widget.

### Fixed
- The 24-hour device timeline stopped hours early, because it showed only the oldest readings of the window.
- The release is published under the tag it was started for. Previously the release step failed for merged pull requests, so v1.0.0 was tagged without a release.

## [1.0.0] - 2026-10-28

### Added
- GitHub Actions CI workflow: formatting check, `go vet`, `go test -race`, and a cross-compile smoke test for all release targets on every push and pull request.
- `etapo --help` describes the `run`, `collect` and `serve` modes and the flag order.
- The configuration is validated at startup. Invalid settings, unknown plug types, missing or placeholder Tapo credentials, and Tapo plugs without a valid IP are reported with the field name.
- Configurable standby window (`standby.start` and `standby.end`, default 02:00 to 05:00 local time). The Standby Leak Detector shows the average power inside it for the last night or the last 7 nights, and reports "No data" when there are no readings in the window.
- The breakdown chart has a day and month picker and a Lines toggle. Hours without readings show as gaps, not zero.
- The device leaderboard's Month view has a month picker. Peak power, month kWh and estimated cost come from the new `GET /api/leaderboard/month?month=YYYY-MM` endpoint.
- Merged pull requests to `main` are tagged and released automatically: Dependabot updates get a patch version, other changes a minor version.
- End-to-end tests run the built binary against mock plugs in CI (`make e2e`).
- The dashboard favicon is served as `/favicon.svg`.

### Changed
- The release workflow now runs the CI workflow first and only publishes binaries if it passes.
- The release workflow and CI read the Go version from `go.mod`, which is now pinned to Go 1.27.
- A missing config file is now an error instead of silently falling back to `config.example.yaml` (mock devices).
- Unknown modes and extra arguments (such as flags placed after the mode) exit with a usage message instead of starting nothing.
- On shutdown the API stops first (closing event streams), then the collector, then pending readings are flushed and the database is closed.
- The Device Leaderboard no longer has a Relay column. Switching a socket on or off is still available from the device dialog.
- The standby leak detector opens on "Last night" by default.

### Fixed
- The Tapo driver decodes the base64 nickname that P115 devices return.
- The mock driver matches the P115: MAC address format, a fixed 230 V, and daily and monthly counters that restart at midnight and the first of the month.
- The Month view of the device leaderboard shows 0 W peak for a month without data, instead of the current live wattage.
- Timestamps are compared in the local time zone for breakdown and standby queries, so day boundaries match what is shown.

## [0.1.2] - 2026-10-23

### Fixed
- Power, voltage and energy values from Tapo sockets are converted using the documented units (mW, mV, Wh). Previously a 5 W draw could be shown as 5000 W.

## [0.1.1] - 2026-10-23

### Fixed
- Tapo KLAP v2 handshake and session encryption, which caused `handshake2 returned HTTP 400` when collecting data from Tapo P115 sockets.

## [0.1.0] - 2026-10-23

### Added
- Core daemon: SQLite storage with WAL mode and batched writes, polling collector, Tapo KLAP driver, mock device simulator, SSE API and embedded web UI.
- Homeowner insights: burn rate KPI, stacked consumption chart, standby leak detector and device leaderboard with detail modal.
- Multi-platform release builds (Linux arm64/armv7/amd64, macOS arm64/amd64, Windows amd64) with checksums.
