# Etapo Architecture & System Design

Etapo is an ultra-lightweight, self-hosted energy monitoring daemon and web dashboard designed specifically to collect, store, and visualize metrics from **TP-Link Tapo P115** (and P110) smart plugs.

---

## 1. Primary Constraints & Goals

- **Target Platform:** Raspberry Pi 3 (1GB RAM, ARMv8 Cortex-A53).
- **Resource Budget:** 
  - Memory: < 30MB RAM.
  - CPU: Idle < 0.5%, polling spikes < 2%.
  - Storage: SD card protection is paramount (minimize random I/O and synchronous writes).
- **Zero External Dependencies:** Single compiled binary without requiring external services like Docker, Redis, or Node.js.
- **Cross-Compilation:** Can be built with `CGO_ENABLED=0` for `linux/arm64` or `linux/arm` directly from macOS or Linux.

---

## 2. Component Separation of Responsibilities

```
                      ┌───────────────────────────────────────┐
                      │        Tapo P115 Plugs (LAN)          │
                      │  (or Simulated Mocks for local dev)   │
                      └───────────────────┬───────────────────┘
                                          │ Poll (e.g. every 10-30s)
                                          ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│ Collector Component (`internal/collector`)                                      │
│  - Plug Driver Interface: KlapDriver (hardware) vs MockDriver (development)     │
│  - Periodic poller running on configurable intervals                            │
│  - In-memory write buffer to batch metrics before flushing                      │
└─────────────────────────────────┬───────────────────────────────────────────────┘
                                  │ Batched writes
                                  ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│ Storage Component (`internal/storage`)                                          │
│  - Pure Go SQLite (modernc.org/sqlite - no CGO required)                        │
│  - WAL mode (Write-Ahead Logging) + PRAGMA synchronous = NORMAL                 │
│  - Schema: Plugs, EnergyReadings, DailyAggregates                              │
└─────────────────────────────────┬───────────────────────────────────────────────┘
                                  │ Queries
                                  ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│ API Component (`internal/api`)                                                  │
│  - REST API: /api/plugs, /api/plugs/:id/history, /api/summary, /api/toggle      │
│  - Real-time SSE (Server-Sent Events) at /api/events for instant UI updates     │
└─────────────────────────────────┬───────────────────────────────────────────────┘
                                  │ Serves
                                  ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│ Frontend UI (`internal/web` embedded via go:embed)                              │
│  - Single HTML/JS/CSS bundle compiled directly into the Go binary               │
│  - Zero client build tools needed; client-side rendering with Chart.js          │
│  - Total Pi CPU overhead for web UI: zero (browser does rendering)              │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Subcommand & Runtime Modes

Etapo can be executed as a unified daemon or as individual decoupled components:
- `etapo run` (default): Runs collector, storage, API, and web UI in a single lightweight process.
- `etapo collect`: Runs only the collector & storage daemon (e.g., if running API separately).
- `etapo serve`: Runs only the storage, API, and web UI.

---

## 4. Hardware Driver Protocol (Tapo KLAP)

TP-Link Tapo P115 sockets communicate over local Wi-Fi using the **KLAP** (Kasa Light Anonymous Protocol).
- Local authentication requires the user's Tapo/TP-Link account email & password or local credentials configured in the Tapo App.
- Drivers implement a common interface `PlugDriver` allowing the app to switch between `MockDriver` (simulated real-world curves) and `KlapDriver` (physical sockets).
