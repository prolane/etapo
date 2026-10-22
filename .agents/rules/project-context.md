---
trigger: always_on
description: Project context and operational constraints for Etapo on Raspberry Pi 3
---

# Etapo Project Guidelines

When working in this repository:
1. **Target Architecture:** Raspberry Pi 3 (1GB RAM, ARMv8 64-bit Cortex-A53). Keep RAM < 30MB, CPU < 1%.
2. **Pure Go / Zero CGO:** Use `modernc.org/sqlite` instead of `mattn/go-sqlite3` so cross-compilation with `CGO_ENABLED=0` remains seamless without cross-compiler toolchains.
3. **SD Card Protection:** Ensure SQLite writes are batched and executed with WAL mode (`PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;`).
4. **Separation of Responsibilities:**
   - `internal/collector`: Ingestion and polling logic with `DeviceDriver` abstraction.
   - `internal/storage`: SQLite persistence and aggregation queries.
   - `internal/api`: REST and SSE endpoints.
   - `internal/web`: Embedded frontend assets.
5. **Drivers:** Always retain the `MockDriver` for zero-hardware development alongside the `KlapDriver` for real Tapo P115 sockets.
6. Check [`ARCHITECTURE.md`](file:///Users/gerard/git-repos/etapo/ARCHITECTURE.md) and [`DECISIONS.md`](file:///Users/gerard/git-repos/etapo/DECISIONS.md) before making structural changes.
