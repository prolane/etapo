# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- GitHub Actions CI workflow: formatting check, `go vet`, `go test -race`, and a cross-compile smoke test for all release targets on every push and pull request.

### Changed
- The release workflow now runs the CI workflow first and only publishes binaries if it passes.
- The release workflow and CI read the Go version from `go.mod`, which is now pinned to Go 1.27.

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
