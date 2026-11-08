---
trigger: always_on
description: Project context and operational constraints for Etapo, a low-power energy monitoring daemon
---

# Etapo Project Guidelines

When working in this repository:
1. **Target Architecture:** Small, low-power Linux boards such as Raspberry Pi 3 (1GB RAM, ARMv8 64-bit Cortex-A53) are the reference. The project also builds for macOS, Windows and amd64 Linux. Keep RAM < 30MB, CPU < 1%.
2. **Pure Go / Zero CGO:** Use `modernc.org/sqlite` instead of `mattn/go-sqlite3` so cross-compilation with `CGO_ENABLED=0` remains seamless without cross-compiler toolchains.
3. **Flash Storage Protection (SD cards and similar):** Ensure SQLite writes are batched and executed with WAL mode (`PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;`).
4. **Separation of Responsibilities:**
   - `internal/collector`: Ingestion and polling logic with `DeviceDriver` abstraction.
   - `internal/storage`: SQLite persistence and aggregation queries.
   - `internal/api`: REST and SSE endpoints.
   - `internal/web`: Embedded frontend assets.
5. **Drivers:** Always retain the `MockDriver` for zero-hardware development alongside the `KlapDriver` for real Tapo P115 sockets.
6. Check [`ARCHITECTURE.md`](../../ARCHITECTURE.md) and [`DECISIONS.md`](../../DECISIONS.md) before making structural changes.
