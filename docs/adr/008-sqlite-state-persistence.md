# ADR 008: SQLite State Persistence via modernc.org/sqlite

## Status
Accepted

## Date
2026-03-19

## Context
Spark v1.0.0 stores all pod state in memory. If spark crashes or restarts, all state is lost: pod records, event history, resource accounting, retry counts. Running pods survive in podman but become invisible to spark -- the scheduler cannot account for their resource usage, and the reconciler cannot manage their lifecycle.

State persistence requires a durable store. Options considered:

1. **JSON file**: Simple, stdlib-only. Write full state on every change. No crash safety -- partial writes corrupt the file. No concurrent read/write.
2. **GOB file**: Same trade-offs as JSON but binary format. Slightly faster serialization but same crash-safety problem.
3. **SQLite with modernc.org/sqlite**: Pure-Go SQLite implementation. WAL mode provides crash-safe writes with concurrent readers. Schema-based, queryable. Works with CGO_ENABLED=0.
4. **SQLite with mattn/go-sqlite3**: C-based SQLite binding via CGO. Faster than pure-Go but requires CGO_ENABLED=1, breaking the existing cross-compilation setup.

## Decision
Use `modernc.org/sqlite` as the SQLite driver with `database/sql` from the standard library.

- WAL journal mode for crash-safe writes without blocking readers.
- Schema: pods table (name PK, spec JSON, status, timestamps, restarts, retry_count), events table (pod_name FK, time, type, message).
- Write on every status change (UpdateStatus, AddEvent). Batch is unnecessary at spark's scale (dozens of pods, not thousands).
- Load all state on startup before registering NATS handlers.
- New flag `--state-db` defaulting to `/var/lib/spark/state.db`.

This adds a new dependency beyond nats.go. The stdlib-only constraint (ADR-001) is relaxed for infrastructure dependencies that cannot be reasonably implemented from scratch. SQLite is a proven, battle-tested storage engine.

## Consequences
- **Positive**: Spark survives restarts without losing state. Resource accounting is correct after recovery. Event history is preserved for debugging.
- **Positive**: Pure Go, CGO_ENABLED=0, cross-compiles for arm64 and amd64 without toolchain changes.
- **Negative**: Adds ~15MB to binary size (modernc.org/sqlite includes a Go translation of the C SQLite source).
- **Negative**: Slightly slower than C SQLite (mattn/go-sqlite3) but acceptable for spark's write volume.
- **Negative**: Relaxes the stdlib-only constraint. Future dependencies should still be scrutinized.
