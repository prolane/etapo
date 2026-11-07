# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.10.0] - 2026-11-08

### Changed
- Readings are summed into 5-minute buckets per plug, and daily totals are kept per plug. Raw 10-second readings are no longer stored. This cuts the database growth and the SD card writes by a large factor. On the first start, the old database is copied to a `-before-rollups-` backup next to it, and the raw readings are removed.
- The dashboard refreshes its summary at most every 10 seconds instead of on every reading.
- `storage.max_buffer_size` is ignored. The setting is still accepted.
- Standby averages use the most recent standby window that has ended: today's window once it has ended, and yesterday's before that.
- The history range `1h` returns 5-minute buckets.
- An unknown history range returns 400. A missing range means 24h.
- The breakdown accepts only `by=plug`. Other values return 400.
- `POST /api/plugs/{id}/power` requires the `on` field.
- Plugs removed from `config.yaml` are hidden from lists and totals. Their data is kept.

### Removed
- The runtime today figure and its dashboard card. It was an estimate from readings above 3 W.
- The breakdown `nights` parameter, which had no effect.

### Fixed
- The live event stream stays open instead of ending after 30 seconds.
- Energy used before a plug was switched off is counted in the totals.
- `POST /api/plugs/{id}/power` for an unknown plug returns 404 instead of 500.

## [1.9.0] - 2026-11-07

### Fixed
- The web dashboard sends at most one summary request and one plugs request at a time. Updates that arrive while a request is running share its result, so the dashboard no longer piles up requests on slow hardware such as the Raspberry Pi.

## [1.8.0] - 2026-11-07

### Added
- `etapo --update` replaces the binary with the latest release, and `etapo --update-to VERSION` installs a given release, for example `v1.8.0`. Before the binary is replaced, the release's checksums must be signed by a trusted release key, the archive's SHA-256 must match, and the new binary must report the expected version. The previous binary is kept as `etapo.prev`. Restart etapo afterwards to run the new version.
- Release checksums are signed with Ed25519 keys. Several keys can be trusted at once, so keys can be rotated without a gap. See docs/release-signing.md.

## [1.6.0] - 2026-11-07

### Added
- `storage.retention_days` is enforced. Readings older than this are deleted once at startup and then once a day, in `etapo run` and `etapo collect`. Deletes run in batches. The database file is not shrunk; freed space is reused for new readings.

## [1.5.0] - 2026-11-07

### Added
- Tapo plugs are found again when the router gives them a new IP address. etapo learns each plug's MAC address on its first connection and, after three failed polls, searches the /24 network of the plug's last known address for it. The plug is used at the new address straight away, without a restart.
- The dashboard shows a banner when a plug is at a different address than the one in config.yaml, with a button to write the new address to the file. Only that plug's `ip` line changes, and a backup of the file is kept.
- Plugs without a recent reading are shown as offline, with an Offline badge on the leaderboard and in the device dialog. The threshold is three poll intervals plus one flush interval.
- `GET /api/plugs` reports `online`, `config_ip` and `mac` for each plug. `GET /api/summary` reports `offline_after_sec`. `POST /api/plugs/:id/config-ip` writes the detected address of a Tapo plug to config.yaml. The event stream sends a `plug_moved` event when a plug is found at a new address.
- The configuration reference is in docs/configuration.md, and the README links to it.

### Changed
- Before a session is opened to a Tapo plug, etapo checks the MAC address of the device at the configured address, and it checks the MAC address again after logging in. A different plug is refused, so no commands reach it.
- Total live power and the live burn rate leave out plugs that are offline.

### Fixed
- A Tapo plug that changed its IP address stopped reporting until etapo was restarted with the new address.
- etapo no longer accepts a different Tapo plug answering at a configured address as the configured plug.

## [1.3.0] - 2026-10-29

### Added
- `etapo --version` prints the version and exits, without needing a configuration file.

### Fixed
- The startup log showed the version as "vv1.2.0".

## [1.2.0] - 2026-10-29

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
