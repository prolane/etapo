# Architectural Decision Records (ADRs)

This document tracks all foundational decisions for the `etapo` project, recording the context, options considered, and reasoning.

---

### ADR-001: Language Selection - Golang

- **Status:** Accepted
- **Context:** The application is intended to run continuously on a Raspberry Pi 3 (1GB RAM) with minimal CPU and RAM usage.
- **Alternatives Considered:**
  - *Python (FastAPI + python-kasa):* Mature Tapo libraries, but runtime baseline is 80–150 MB RAM and higher CPU overhead under Python interpreter.
  - *Node.js:* Memory footprint typically 60–120 MB RAM; requires Node runtime on the Pi.
  - *Rust:* Ultra-low memory, but longer build times and steeper contributor barrier.
- **Decision:** Use **Go (Golang)**.
- **Consequences:** 
  - Produces a single static binary.
  - Typical memory usage is under 15–25 MB.
  - Negligible CPU usage.
  - Cross-compilation from macOS to `linux/arm64` or `linux/arm` is seamless with `GOOS=linux`.

---

### ADR-002: Storage Engine - SQLite with WAL and Batching for SD Card Protection

- **Status:** Accepted
- **Context:** Raspberry Pi 3 boots and runs from a microSD card. Continuous unbuffered random writes can rapidly wear out flash memory and cause corruption on sudden power loss.
- **Alternatives Considered:**
  - *TimescaleDB / InfluxDB:* Far too heavy for a 1GB Raspberry Pi 3.
  - *Flat JSON/CSV files:* Lacks indexing, concurrency safety, and query capability for time-series charts.
  - *CGO SQLite (`mattn/go-sqlite3`):* Requires cross-compiler C toolchain (arm-linux-gnueabihf) when building from macOS.
- **Decision:** Use pure-Go SQLite (`modernc.org/sqlite`) configured with:
  1. `PRAGMA journal_mode = WAL;` (Write-Ahead Logging prevents random file lockouts and reduces write amplification).
  2. `PRAGMA synchronous = NORMAL;` (Eliminates excessive `fsync` calls while maintaining safety in WAL mode).
  3. In-memory buffering: Metrics collected every $N$ seconds are queued in memory and committed in batches (e.g., every 30–60 seconds).
- **Consequences:** Safe, wear-leveling friendly, zero CGO cross-compilation headaches.

---

### ADR-003: Architecture Separation of Responsibilities

- **Status:** Accepted
- **Context:** Need clean separation between data ingestion (polling Tapo sockets), persistence, API exposure, and frontend visualization.
- **Decision:** Decouple into distinct packages:
  - `internal/collector`: Polls hardware or mock devices; produces readings.
  - `internal/storage`: Manages SQLite connection, batch insertion, and time-series aggregation queries.
  - `internal/api`: Exposes REST endpoints (`/api/plugs`, `/api/summary`, etc.) and Server-Sent Events (SSE).
  - `internal/web`: Embedded static assets (`go:embed`) providing a zero-dependency web dashboard.
- **Consequences:** Each part can be tested and modified independently. The application can run as a single unified process (`etapo run`) or distinct processes (`etapo collect`, `etapo serve`).

---

### ADR-004: Hardware vs Simulator Dual Driver Interface

- **Status:** Accepted
- **Context:** Development and UI verification must proceed before physical Tapo P115 sockets arrive.
- **Decision:** Define a `DeviceDriver` interface:
  ```go
  type DeviceDriver interface {
      GetInfo(ctx context.Context) (*DeviceInfo, error)
      GetEnergyUsage(ctx context.Context) (*EnergyUsage, error)
      SetPower(ctx context.Context, on bool) error
  }
  ```
  Implement both `MockDriver` (generates realistic power curves for e.g. fridge, kettle, TV, workstation) and `KlapDriver` (physical Tapo P115 via KLAP protocol).
- **Consequences:** Users can test the dashboard immediately. When hardware arrives, only configuration changes (switch mode from `mock` to `tapo`) are needed.

---

### ADR-005: Embedded Zero-Build Web UI

- **Status:** Accepted
- **Context:** A full React/Vue Node.js build pipeline adds complexity and deployment friction on an embedded system.
- **Decision:** Use a clean, single-page UI built with vanilla JS and Tailwind (or minimal CSS) and Chart.js, embedded directly into the Go binary via `embed.FS`.
- **Consequences:** Zero CPU overhead on the Pi for UI serving; visiting the web page downloads a lightweight single bundle (<100KB) and renders on the client browser. No Node.js required.

---

### ADR-006: SHA-1 Usage in the Tapo KLAP Driver (Protocol Requirement)

- **Status:** Accepted
- **Context:** The Tapo KLAP v2 handshake (`internal/collector/tapo/klap.go`) derives the authentication hash as `SHA-256(SHA-1(username) || SHA-1(password))`. Tapo devices verify this exact value during `handshake1` and `handshake2`. CodeQL flags the `sha1` call as a weak hashing algorithm (alert #4).
- **Alternatives Considered:**
  - *Use SHA-256 for the inner hashes:* Breaks login with every device that uses KLAP v2, since the device computes the hash with SHA-1.
  - *Suppress the warning in code:* Hides the reason for the SHA-1 call from future readers. We document it here instead.
- **Decision:** Keep SHA-1 for the inner username and password hashes, because the protocol requires it. SHA-1 is used only to build the protocol's login value. It is not used for storing or signing data, and the session keys are derived with SHA-256. The CodeQL alert is dismissed as a false positive with a reference to this ADR.
- **Consequences:**
  - Any change to the KLAP hashing must be checked against a real device, since the mock driver cannot verify it.
  - If Tapo introduces a protocol version that does not need SHA-1, revisit this decision.

---

### ADR-007: Identify Tapo Plugs by MAC Address

- **Status:** Accepted
- **Context:** The router can give a Tapo plug a new IP address when it reconnects, and the configured address then points at nothing or at another device. All plugs of one TP-Link account share the same login, so a device at an old address can accept the login and receive commands meant for the configured plug. The `plugs` table already had a `mac` column, but nothing filled it.
- **Alternatives Considered:**
  - *DHCP reservation only:* the recommended setup, but it does not protect against a reservation that is lost or a plug that moves to another network, and it does not stop a different plug answering at the configured address.
  - *ARP table lookup:* lists only devices that already answered at their current address, so it cannot find a plug that has moved.
  - *Subnet broadcast of the discovery probe:* the Raspberry Pi's address is on a different subnet from the plugs, so a broadcast from it would not reach them. Unicast to each host of the /24 does reach them, and needs no socket option that differs per operating system.
  - *Writing the new address into `config.yaml` automatically:* changes the production configuration without the user's consent, and a YAML rewrite drops comments. See ADR-008.
- **Decision:** Store the MAC address the device reports on the first successful poll. Before opening a session, ask the device at the configured address for its MAC address with the discovery probe, and refuse a different MAC address. Check the MAC address again after the login. After three failed polls, search the /24 network of the last known address for the stored MAC address, at most once a minute, and move the plug to the address where it is found. The configuration file is not changed.
- **Consequences:**
  - A plug that moves to another /24 network is not found. A plug that has never connected cannot be searched for, because it has no stored MAC address.
  - Every new session is preceded by one discovery exchange. If the device does not answer it, the login still checks the MAC address.
  - The probe and the reply format were checked against eight P115 (EU) plugs on the live network. Any change to the probe must be checked against a real device, since the mock driver cannot answer it.

---

### ADR-008: Write config.yaml Only on Request

- **Status:** Accepted
- **Context:** The address of a moved plug is known at runtime, but the configuration file is the source of truth for the next start, and it also holds credentials and comments the user wrote.
- **Decision:** The dashboard offers to write the detected address of a plug to `config.yaml`, and etapo writes it only when the user confirms. The endpoint requires the address the client expects, so a plug that has moved again is not written with an address nobody confirmed. Only the `ip:` line of that plug is changed, the other lines are left as they are, and the file is replaced atomically. Before each write, the previous file is copied to a timestamped backup, and only the two newest backups are kept.
- **Consequences:**
  - The file keeps its comments, quoting and layout. The change is refused if the value is not on the same line as its key.
  - Backups and the file contain the TP-Link password, so their permissions must stay restrictive. Backups keep the permissions of the original file.
  - Other settings in the file are read at the next start; the running process only updates the address of the plug that was written.

---

### ADR-009: Enforce Data Retention with an In-Process Daily Purge, No VACUUM

- **Status:** Accepted (implemented in `internal/storage/sqlite.go`, `StartRetention` and `PurgeReadingsBefore`)
- **Context:** `storage.retention_days` was validated and documented, but nothing deleted old readings, so the database grew without limit on the SD card. The purge has to run without any setup on the Raspberry Pi, and it must not add large writes to the SD card.
- **Alternatives Considered:**
  - *External cron job or systemd timer:* an extra piece to install and keep in sync with the binary and the config. Rejected because etapo must run with no setup beyond its binary and config.
  - *Purge on every flush:* deletes a little more often, but runs a delete on the hot write path for no real gain, since data only ages by the day.
  - *`VACUUM` or `PRAGMA incremental_vacuum` after a purge:* returns space to the filesystem, but a full `VACUUM` rewrites the whole database, which is slow on a Raspberry Pi 3 and causes heavy SD card writes. Rejected. Freed pages stay in the file and SQLite reuses them for new readings, so the file size stays roughly constant once it reaches its steady state.
- **Decision:**
  1. A goroutine with a `time.Ticker` in the process that writes readings (`etapo run` and `etapo collect`) deletes readings older than `retention_days` once a day. The purge also runs once at startup so a long-stopped Pi catches up on its first start.
  2. Deletes are made in batches (for example, by timestamp ranges or a bounded `LIMIT`) so that the first purge after a long time does not hold the write lock for long.
  3. The database is not shrunk. No `VACUUM` is run. The file size stays at its high-water mark and SQLite reuses the freed pages.
  4. `etapo serve` does not purge. It only reads data, and a single process doing the purge keeps deletes from competing.
- **Consequences:**
  - The database file does not shrink after a purge. Disk use is bounded by the peak amount of data kept within the retention window, not by the total ever recorded.
  - Retention is applied at most one day late. A process that is not running cannot purge, so data is kept longer until the next start.
  - Changing `retention_days` takes effect at the next start, and the next daily run removes data that is now outside the window.

---

### ADR-010: Self-Update from GitHub Releases with Signed Checksums

- **Status:** Accepted
- **Context:** Updating the Raspberry Pi meant copying a binary by hand. The Pi is reachable only on the LAN, so CI cannot push to it. The updater must not restart the service or overwrite files without the user starting it (see ADR-009 for the same reasoning about external timers). The update must verify that a release came from this project, and that check must not depend on an external service. An attempt to use GitHub attestations (SLSA provenance via Sigstore) failed when the public Sigstore transparency log returned HTTP 500 for over an hour during a database upgrade, which blocked the release job.
- **Alternatives Considered:**
  - *GitHub attestations, verified with `gh attestation verify --bundle`:* needs a Sigstore transparency log at release time and at update time, so a Sigstore outage blocks releases.
  - *Verify the attestation in Go with `sigstore-go`:* no external tool needed, but about 350 more packages. The binary grew from 11.8 MB to 23.5 MB (linux/arm64) and daemon resident memory from about 18 MB to about 28 MB on macOS. That is too much for a check that runs once per update.
  - *Checksum only:* proves that a download matches `checksums.txt`, but not that the release came from this project, because an attacker who can replace a release can replace its checksums too.
  - *Automatic updater timer:* rejected for the reasons in ADR-009 and the restart rule for the Pi.
- **Decision:**
  1. `etapo --update` installs the latest release and `etapo --update-to VERSION` installs a given one. Both replace only the binary and exit. Neither restarts the service nor touches the config or database.
  2. The release workflow signs `checksums.txt` with Ed25519 keys held in the `RELEASE_SIGNING_KEYS` secret and writes `checksums.txt.sig`. The public keys are built into etapo (`builtinReleaseKeys`). A release is installed only when a signature from one of them verifies.
  3. The archive's SHA-256 must match `checksums.txt`, and the new binary must report the expected version, before anything is replaced.
  4. Key rotation is supported without a gap: a release can carry several signatures, and a binary trusts several keys. The procedure is in `docs/release-signing.md`.
  5. The previous binary is kept as `etapo.prev`.
- **Consequences:**
  - No external service, GitHub login or token is needed at release time or update time, beyond GitHub itself.
  - The binary links no Sigstore code. The only new dependency is the Go standard library's `crypto/ed25519`.
  - The release workflow holds the signing key. Anyone who can change the release workflow on `main` can therefore sign a release, which is the same trust as publishing a release today. The key must be rotated if it may have leaked.
  - A binary cannot be protected against a leaked key by the updater alone. The response is to rotate the key and install the replacement release by hand (`docs/release-signing.md`).
  - Releases published before the first signed release cannot be installed with `--update-to`.
