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
- **Go Version:** Go 1.27 (pinned in `go.mod`; CI and release workflows read it from there). Use the Homebrew install (`brew install go`), not the legacy `/usr/local/go`.

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

Prefer the `Makefile` targets (`make help` lists them): `make build`, `make run`, `make test`, `make lint`, `make cross`, `make ci`, `make clean`. The raw commands below are what the targets run.

- **Run locally with mock data:**
  ```bash
  go run ./cmd/etapo --config config.example.yaml
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

- **Versioning:** Semantic versioning with `vMAJOR.MINOR.PATCH` tags (currently pre-1.0).
- **Current version:** the latest tag on `origin`. Check with `git ls-remote --tags origin`.
- **Automatic releases:** Merging a pull request to `main` creates the next tag and publishes a release (`.github/workflows/auto-tag.yml`):
  - Dependabot PRs bump the PATCH version.
  - All other PRs (features and fixes made by us) bump the MINOR version and reset PATCH to 0.
  - A PR with the `release:major` label bumps the MAJOR version and resets MINOR and PATCH to 0. Use it for the first stable release (v1.0.0) and for breaking changes. Add the label before the PR is merged; the label is read at merge time.
- **Release build:** `.github/workflows/release.yml` cross-compiles all targets (linux arm64/armv7/amd64, darwin arm64/amd64, windows amd64), packages them with checksums, and publishes a GitHub Release. It runs from a tag push or from `auto-tag.yml`.
- **Rule for AI agents:** Do NOT create or push tags by hand. A release happens by merging to `main`. Before opening or merging a PR that changes behaviour, make sure the next version is what the user expects; ask first when the version matters.
- **Changelog:** `CHANGELOG.md` follows Keep a Changelog. Add a link-free entry under `[Unreleased]` for each user-visible change in the same PR. Automatic tagging does not move these entries; the maintainer renames `[Unreleased]` to the new version when it is released.
- **CI:** `.github/workflows/ci.yml` runs gofmt, vet, `go test -race`, the end-to-end tests (`make e2e`) and a cross-compile check. The release workflow runs CI first and does not publish if it fails.
- **End-to-end tests:** `e2e/e2e_test.go` builds the binary and runs it with two mock plugs. It checks the dashboard, API validation, polling, relay control, the event stream, persistence across a restart, and CLI handling. Keep it passing when changing the API or the CLI. Mock drivers must keep matching the real P115 (see the `tapo` driver for the units and fields).
- **Security:** `.github/workflows/security.yml` runs `govulncheck` on push, pull request and weekly. `.github/dependabot.yml` opens weekly update PRs for Go modules and GitHub Actions. `.github/workflows/dependabot-auto-merge.yml` approves and auto-merges all Dependabot updates, including major ones, once required checks pass. Each merged Dependabot update gets a patch release (see Automatic releases). Secret scanning and push protection are enabled in GitHub settings (not in a workflow). `.github/workflows/codeql.yml` runs CodeQL static analysis for Go and GitHub Actions on push, pull request and weekly; results appear under the repo's Security tab. All GitHub Actions are pinned to full commit SHAs with the version as a trailing comment; Dependabot proposes updates to these pins. Known false positives are dismissed in CodeQL with a reference to the ADR in `DECISIONS.md`.
- **Manual release (emergency only, after user confirmation):**
  ```bash
  git tag -a vX.Y.Z -m "Release vX.Y.Z: <summary>"
  git push origin vX.Y.Z
  ```
