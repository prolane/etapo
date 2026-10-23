# AGENTS.md — AI Context & Guidelines for Etapo

This document provides context, design principles, and guidelines for AI agents (including Antigravity) working in the `etapo` repository.

---

## 1. Project Overview & Context

- **Purpose:** A lightweight, self-hosted energy monitoring daemon and web dashboard designed to poll **TP-Link Tapo P115** (and P110) smart sockets, persist time-series energy metrics, and expose an API and Web UI.
- **Primary Deployment Target:** **Raspberry Pi 3** (1GB RAM, ARMv8 64-bit Cortex-A53, running Raspberry Pi OS from a microSD card).
- **Core Constraints:**
  - Absolute minimum CPU & RAM consumption (RAM < 30MB, CPU < 1%).
  - MicroSD card wear protection: WAL mode + batched writes.
  - Zero CGO dependency: must build with `CGO_ENABLED=0` for easy cross-compilation from Mac/PC to `linux/arm64` or `linux/arm`.
  - Zero runtime dependencies (no Docker required, no Node.js required, no external databases).

---

## 2. Key References

- **Architecture:** Read [`ARCHITECTURE.md`](file:///Users/gerard/git-repos/etapo/ARCHITECTURE.md) for module decomposition, data flow, and runtime modes.
- **Decisions:** Read [`DECISIONS.md`](file:///Users/gerard/git-repos/etapo/DECISIONS.md) before making architectural changes.
- **Go Version:** Go 1.17+ compatible.

---

## 3. Directory Layout & Separation of Concerns

```
etapo/
├── cmd/
│   └── etapo/                 # CLI entrypoint (subcommands: run, collect, serve)
├── internal/
│   ├── config/                # YAML/JSON/ENV configuration parsing
│   ├── models/                # Core domain structs (Plug, EnergyReading, Summary)
│   ├── storage/               # Pure-Go SQLite persistence, batching, migrations
│   ├── collector/             # Polling daemon, scheduler, DeviceDriver interface
│   │   ├── mock/              # Simulated device driver (for testing/development)
│   │   └── tapo/              # Real Tapo KLAP protocol driver
│   ├── api/                   # HTTP REST handlers & Server-Sent Events (SSE)
│   └── web/                   # Embedded static frontend assets (HTML, CSS, JS)
├── config.example.yaml        # Sample configuration file
├── ARCHITECTURE.md            # System architecture document
├── DECISIONS.md               # Architectural Decision Records (ADRs)
└── AGENTS.md                  # This file
```

---

## 4. Hardware Driver Protocol (Tapo KLAP)

TP-Link Tapo P110/P115 devices use the **KLAP** protocol (AES-128-CBC encryption with SHA256 session handshakes).
- Never remove the `MockDriver`: it is required for development, unit testing, and when users do not yet have physical sockets.
- The `DeviceDriver` interface (`internal/collector/driver.go`) abstracts whether a device is simulated or physical.

---

## 5. Storage & SD Card Guidelines

- Always use `modernc.org/sqlite` (pure Go, CGO-free).
- Always ensure SQLite connections execute:
  ```sql
  PRAGMA journal_mode = WAL;
  PRAGMA synchronous = NORMAL;
  PRAGMA busy_timeout = 5000;
  ```
- Do not execute an `INSERT` per socket per polling tick immediately to disk. Metrics must be queued in memory and flushed periodically in a single transaction (batch write).

---

## 6. Build & Test Commands

- **Run locally with mock data:**
  ```bash
  go run ./cmd/etapo run --config config.example.yaml
  ```
- **Cross-compile for Raspberry Pi 3 (64-bit):**
  ```bash
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/etapo-linux-arm64 ./cmd/etapo
  ```
- **Cross-compile for Raspberry Pi 3 (32-bit):**
  ```bash
  CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o dist/etapo-linux-arm ./cmd/etapo
  ```
- **Run tests:**
  ```bash
  go test -v ./...
  ```

---

## 7. Releases & Versioning

- **Versioning:** Semantic versioning with `vMAJOR.MINOR.PATCH` tags (currently pre-1.0). Bug fixes bump PATCH, new features bump MINOR.
- **Current version:** `v0.1.2` (latest tag on `origin`). Check with `git ls-remote --tags origin`.
- **Release trigger:** Pushing a `v*` tag runs `.github/workflows/release.yml`, which cross-compiles all targets (linux arm64/armv7/amd64, darwin arm64/amd64, windows amd64), packages them with checksums, and publishes a GitHub Release.
- **Rule for AI agents:** Do NOT create or push a tag without explicit user confirmation. When unreleased commits exist on `main` that fix bugs or add features, ask the user whether to cut a new release and propose the next version number.
- **Changelog:** `CHANGELOG.md` follows Keep a Changelog. Before tagging, move the `[Unreleased]` entries under a new `[X.Y.Z] - YYYY-MM-DD` heading and add a link-free entry per change.
- **CI:** `.github/workflows/ci.yml` runs gofmt, vet, `go test -race` and a cross-compile check. The release workflow runs CI first and does not publish if it fails.
- **Release steps (after confirmation):**
  ```bash
  git tag -a vX.Y.Z -m "Release vX.Y.Z: <summary>"
  git push origin vX.Y.Z
  ```
