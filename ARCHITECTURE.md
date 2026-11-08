# Etapo Architecture & System Design

Etapo is an ultra-lightweight, self-hosted energy monitoring daemon and web dashboard designed specifically to collect, store, and visualize metrics from **TP-Link Tapo P115** (and P110) smart plugs.

---

## 1. Primary Constraints & Goals

- **Target Platforms:** Small, low-power machines first, such as a Raspberry Pi 3 (1GB RAM, ARMv8 Cortex-A53), which is the reference device for the measurements. Also supported: macOS, Windows, and Linux on amd64 or arm64.
- **Resource Budget:** 
  - Memory: < 30MB RAM.
  - CPU: Idle < 0.5%, polling spikes < 2%.
  - Storage: flash storage wear (SD cards, eMMC) is a concern, so minimize random I/O and synchronous writes.
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
│  - Sums readings into 5-minute buckets in memory, flushed every interval        │
└─────────────────────────────────┬───────────────────────────────────────────────┘
                                  │ Batched writes
                                  ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│ Storage Component (`internal/storage`)                                          │
│  - Pure Go SQLite (modernc.org/sqlite - no CGO required)                        │
│  - WAL mode (Write-Ahead Logging) + PRAGMA synchronous = NORMAL                 │
│  - Schema: plugs, plug_state, samples_5m, daily (see ADR-011)                  │
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
│  - Host CPU overhead for web UI: zero (browser does rendering)                  │
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

---

## 5. Plug Addressing and Recovery

A Tapo plug is identified by its MAC address, not by its IP address, because the router can change the IP address when the plug reconnects. See [ADR-007](DECISIONS.md#adr-007-identify-tapo-plugs-by-mac-address) and [ADR-008](DECISIONS.md#adr-008-write-configyaml-only-on-request).
- **Learning:** the MAC address from `get_device_info` is stored in `plugs.mac` on the first successful poll.
- **Pre-login check:** before a KLAP session is opened, the driver sends the discovery probe (UDP port 20002) to the configured address. If the device there reports a different MAC address, the login is refused, so no session is opened on another plug. After the login the MAC address is checked again.
- **Search:** after three failed polls in a row, the collector sends the same probe to the 254 hosts of the plug's /24 network, at most once a minute. A plug that answers from a new address is moved there: the driver is pointed at the new address, the stored address is updated and a `plug_moved` event is sent.
- **Config address:** the dashboard compares the plug's current address with `config.yaml`. The file is only changed when the user confirms, through `POST /api/plugs/:id/config-ip`.
