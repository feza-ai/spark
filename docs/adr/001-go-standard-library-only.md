# ADR 001: Go Standard Library Only (Except NATS)

## Status
Accepted

## Date
2026-03-18

## Context
Spark is a single-binary agent that runs on GPU hosts. It needs to be simple to build, deploy, and audit. Third-party dependencies increase supply chain risk and complicate builds on constrained environments like the DGX Spark.

The only external runtime dependency is NATS for messaging, which is the standard bus across feza-ai projects (wolf already uses it for scheduler communication).

## Decision
Write spark in Go using only the standard library plus `github.com/nats-io/nats.go`. No CLI frameworks (cobra, urfave), no test frameworks (testify), no logging frameworks. Use `flag` for CLI, `testing` for tests, `log/slog` for structured logging.

Shell out to `podman` and `nvidia-smi` via `os/exec` rather than importing container or GPU libraries.

## Consequences
- Positive: Single static binary. Minimal attack surface. Fast builds. Easy to audit.
- Positive: Consistent with wolf's approach (wolf uses the same pattern).
- Negative: More boilerplate for things like table-driven test assertions.
- Negative: No programmatic podman API -- relies on CLI stability. Mitigated by pinning podman version in CI.
