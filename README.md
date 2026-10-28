# Etapo ⚡

> Ultra-lightweight, self-hosted energy monitoring daemon and web dashboard designed for **TP-Link Tapo P115** (and P110) smart plugs. Built in pure Go, optimized specifically for **Raspberry Pi 3**.

---

## Highlights

- **Ultra-Low Footprint:** Consumes < 20MB of RAM and < 0.5% CPU when idle on a Raspberry Pi 3.
- **MicroSD Card Protection:** In-memory batch buffering + SQLite WAL mode (`PRAGMA synchronous = NORMAL;`) to minimize write amplification and prevent SD card degradation.
- **Zero CGO / Pure Go:** Uses `modernc.org/sqlite` — cross-compile cleanly for `linux/arm64` or `linux/arm` directly from macOS or Linux with `CGO_ENABLED=0` and zero cross-compiler toolchains.
- **Zero Runtime Dependencies:** No Docker, no Node.js, and no external database needed. The web UI is embedded directly into the single binary.
- **Real-Time Web Dashboard:** Instant live wattage updates via Server-Sent Events (SSE), per-socket relay toggle switches, and historical consumption graphs.
- **Dual Mode (Hardware & Simulator):** Built-in realistic mock driver (fridge, TV, kettle, workstation curves) for development before physical sockets arrive.

---

## Architecture

```
                      ┌───────────────────────────────────────┐
                      │        Tapo P115 Plugs (LAN)          │
                      │  (or Simulated Mocks for local dev)   │
                      └───────────────────┬───────────────────┘
                                          │ Poll (e.g. every 10-30s)
                                          ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│ Collector Component (`internal/collector`)                                      │
│  - Driver Interface: KlapDriver (hardware) vs MockDriver (development)          │
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

For in-depth architectural details and design rationale, see:
- [`ARCHITECTURE.md`](file:///Users/gerard/git-repos/etapo/ARCHITECTURE.md)
- [`DECISIONS.md`](file:///Users/gerard/git-repos/etapo/DECISIONS.md)
- [`AGENTS.md`](file:///Users/gerard/git-repos/etapo/AGENTS.md)

---

## Pre-Built Binaries (Ready to Run)

Pre-compiled standalone binaries are published for every release on GitHub. You do **not** need Go, Docker, or Node.js installed to run Etapo.

👉 **Download latest binaries from [GitHub Releases](https://github.com/prolane/etapo/releases)**

Available packages for each release:
| Target Platform | Architecture | Archive |
|---|---|---|
| **Raspberry Pi 3 / 4 / 5 (64-bit)** | `linux-arm64` | `etapo-linux-arm64-v*.tar.gz` |
| **Raspberry Pi (32-bit)** | `linux-armv7` | `etapo-linux-armv7-v*.tar.gz` |
| **Synology NAS / Linux Server** | `linux-amd64` | `etapo-linux-amd64-v*.tar.gz` |
| **macOS Apple Silicon (M1/M2/M3/M4)** | `darwin-arm64` | `etapo-darwin-arm64-v*.tar.gz` |
| **macOS Intel** | `darwin-amd64` | `etapo-darwin-amd64-v*.tar.gz` |
| **Windows PC** | `windows-amd64` | `etapo-windows-amd64-v*.zip` |

### Quick Install (e.g. Raspberry Pi or Linux)
```bash
# 1. Download and extract the archive for your architecture
tar -xzf etapo-linux-arm64-*.tar.gz

# 2. Copy and customize configuration
cp config.example.yaml config.yaml

# 3. Run etapo
./etapo --config config.yaml
```

---

## Quickstart (Building from Source)

Run the application with the included mock profile configuration:

```bash
# 1. Run directly with Go
go run ./cmd/etapo --config config.example.yaml

# 2. Open dashboard in browser
open http://localhost:8080
```

---

## Running Modes

Flags must come before the mode, because Go stops parsing flags at the first positional argument. Usage: `etapo [--config FILE] [run|collect|serve]`

| Mode | Command | What it runs |
|---|---|---|
| `run` (default) | `etapo --config config.yaml` | Collector, storage, API, and web UI in one process |
| `collect` | `etapo --config config.yaml collect` | Collector and storage only (use when the API runs separately) |
| `serve` | `etapo --config config.yaml serve` | Storage, API, and web UI only (reads data written by `collect`) |

If `--config` points to a missing file, etapo falls back to `config.example.yaml` (mock devices) and prints a warning.

---

## Configuring Real Tapo P115 Plugs

1. In the Tapo app, signed in with the same account as in step 3, enable third-party access:
   **Me → Third-Party Services → Third-Party Compatibility** → switch on.
   Without this, the plugs reject Etapo's login with `handshake1 returned HTTP 403`.
   The location of the **Me** tab depends on the app version (bottom right or top left).
2. Copy the example configuration:
   ```bash
   cp config.example.yaml config.yaml
   ```
3. Open `config.yaml` and set your TP-Link account credentials:
   ```yaml
   tapo_auth:
     username: "your-tplink-email@example.com"
     password: "your-tplink-password"
   ```
4. Set `type: "tapo"` and enter each plug's local LAN IP:
   ```yaml
   plugs:
     - id: "living-room-tv"
       name: "Living Room TV"
       model: "P115"
       type: "tapo"
       ip: "192.168.1.50"
   ```

---

## Cross-Compiling for Raspberry Pi 3

Compile directly from your Mac or PC to a single static binary:

### 64-bit Raspberry Pi OS (Recommended)
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/etapo-linux-arm64 ./cmd/etapo
```

### 32-bit Raspberry Pi OS
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o dist/etapo-linux-arm ./cmd/etapo
```

Then copy the binary and your `config.yaml` to the Raspberry Pi:
```bash
scp dist/etapo-linux-arm64 pi@raspberrypi.local:/home/pi/etapo
scp config.yaml pi@raspberrypi.local:/home/pi/config.yaml
```

---

## Running as a systemd Service on Raspberry Pi

Create `/etc/systemd/system/etapo.service`:

```ini
[Unit]
Description=Etapo Tapo Energy Monitor
After=network.target

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi
ExecStart=/home/pi/etapo --config /home/pi/config.yaml
Restart=always
RestartSec=5
LimitNOFILE=4096

[Install]
WantedBy=multi-user.target
```

Enable and start the service:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now etapo
sudo systemctl status etapo
```

---

## API Reference

| Endpoint | Method | Description |
|---|---|---|
| `/health` | `GET` | Health check endpoint |
| `/api/summary` | `GET` | Aggregated total live Watts, today's kWh, and estimated cost |
| `/api/plugs` | `GET` | List all plugs with latest telemetry and on/off status |
| `/api/plugs/:id` | `GET` | Details for a specific plug |
| `/api/plugs/:id/history?range=1h\|24h\|7d` | `GET` | Time-series data points for charting |
| `/api/plugs/:id/power` | `POST` | Toggle plug relay: `{"on": true\|false}` |
| `/api/events` | `GET` | Server-Sent Events (SSE) real-time stream of socket readings |

---

## Running Tests

```bash
go test -v ./...
```
