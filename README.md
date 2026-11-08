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
- **Survives Address Changes:** When a Tapo plug gets a new IP address from the router, etapo finds it again by its MAC address, and the dashboard offers to update `config.yaml`. Plugs that stop answering are marked offline.

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

A missing config file is an error; etapo never falls back to the example config. Run `etapo --help` for the same summary.

---

## Configuration

All settings are in one YAML file (`config.yaml` by default). The complete reference, with the default for each key, the validation rules and what etapo writes to the file, is in **[docs/configuration.md](docs/configuration.md)**.

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

   Reserve each plug's address in your router, bound to its MAC address (a DHCP reservation), so the address does not change. If a plug still gets a new address, etapo finds it again by MAC and the dashboard offers to update the `ip` here. See [Plug addresses](docs/configuration.md#plug-addresses).

5. Restart etapo and check the log for `handshake1 returned HTTP 403` on any plug.

### Adding plugs later

Third-Party Compatibility is a setting on the Tapo account, not on each plug. It applies to every plug linked to that account.

- A newly added plug can show `handshake1 returned HTTP 403` while the existing plugs work. The setting is not applied to new plugs automatically. To fix this, switch **Third-Party Compatibility** off and back on in the Tapo app. This pushes the setting to all plugs on the account.
- Switching the setting off stops every plug on the account from connecting, including the ones that worked before. Switching it back on restores them. You do not need to change any plug or restart etapo; the next poll reconnects, which may take up to one poll interval.

---

## Standby Baseline

The standby (vampire load) figure for each socket is its average power inside a time window, using the server's local time zone. The dashboard opens on the last night; the "Last 7 nights" button shows the average over the last seven nights instead. The default window is 02:00 to 05:00.

To change the window, add a `standby` section to `config.yaml` and restart etapo (times are `HH:MM`, 24-hour, local time):

```yaml
standby:
  start: "02:00"
  end: "05:00"
```

An end earlier than the start wraps past midnight (for example `start: "23:00"`, `end: "05:00"`). Until there are readings inside the window, the dashboard shows "No data" instead of a value.

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

## Updating etapo

etapo can replace its own binary with a release from GitHub:

```bash
etapo --update               # latest release
etapo --update-to v1.5.0     # a specific release (also used to go back to an older one)
```

- Before anything is replaced, the release's `checksums.txt` must carry a signature from one of etapo's release keys, the archive's SHA-256 must match it, and the new binary must report the expected version. No network service other than GitHub is needed, and no GitHub login or token.
- Releases published before the first signed release (v1.8.0) are not signed, so `--update-to` cannot install them. How releases are signed, and how the keys are rotated, is described in [docs/release-signing.md](docs/release-signing.md).
- The previous binary is kept as `etapo.prev` next to it. To go back, stop etapo and run `mv etapo.prev etapo`.
- `config.yaml` and the database are not changed, and the service is not restarted. Restart it afterwards to run the new version: `sudo systemctl restart etapo`.
- The directory that holds the binary must be writable by the user running the command. If you installed etapo in `/usr/local/bin`, run it with `sudo`.

---

## API Reference

| Endpoint | Method | Description |
|---|---|---|
| `/health` | `GET` | Health check endpoint |
| `/api/summary?nights=N` | `GET` | Aggregated total live Watts (offline plugs left out), today's kWh, estimated cost and standby. `nights` (1-365, default 7) sets how many nights the standby average covers. `offline_after_sec` is how old a reading may be before its plug counts as offline |
| `/api/plugs?nights=N` | `GET` | List all plugs with latest telemetry, on/off status and standby. Each plug also has `online`, `config_ip` (its address in `config.yaml`, for Tapo plugs) and `mac`. `nights` as for `/api/summary` |
| `/api/plugs/:id` | `GET` | Details for a specific plug |
| `/api/plugs/:id/history?range=1h\|24h\|day\|7d\|30d\|month` | `GET` | Average power per bucket for the detail chart: `day` (a local calendar day, `date=YYYY-MM-DD`, default today) and `24h` in 5 minute buckets, `7d` in 1 hour buckets, `month` (a calendar month, `month=YYYY-MM`, default current) in 4 hour buckets. Buckets without readings are `null`. `today` (same as `day`), `1h` (5 minute buckets) and `30d` (4 hours) are also accepted |
| `/api/breakdown?range=today\|month&by=plug` | `GET` | Consumption per hour (today) or per day (month) for the stacked chart. Pick a past period with `date=YYYY-MM-DD` (day) or `month=YYYY-MM` (month). Future periods return 400 |
| `/api/leaderboard/day?date=YYYY-MM-DD` | `GET` | Each plug's peak power, kWh and estimated cost for a local calendar day. Defaults to today; future days return 400 |
| `/api/leaderboard/month?month=YYYY-MM` | `GET` | Each plug's peak power, kWh and estimated cost for a calendar month. Defaults to the current month; future months return 400 |
| `/api/plugs/:id/power` | `POST` | Toggle plug relay: `{"on": true\|false}` |
| `/api/plugs/:id/config-ip` | `POST` | Writes the address a Tapo plug was found at into `config.yaml`: `{"ip": "x.x.x.x"}`. Only that plug's `ip` line changes, and a backup is kept. Returns 409 if the plug is not at that address or the file already has it. See [docs/configuration.md](docs/configuration.md#what-etapo-writes-to-the-file) |
| `/api/events` | `GET` | Server-Sent Events (SSE) real-time stream of socket readings (`reading`) and of plugs found at a new address (`plug_moved`) |

Every parameter, response field and known issue is described in **[docs/api.md](docs/api.md)**. The Tapo responses etapo reads are in **[docs/tapo-api.md](docs/tapo-api.md)**.

---

## Running Tests

```bash
go test -v ./...
```

The end-to-end tests build the binary and run it against mock plugs, checking the API, dashboard, relay control, event stream, persistence across a restart and CLI handling:

```bash
make e2e
```
