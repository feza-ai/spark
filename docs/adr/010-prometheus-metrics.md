# ADR 010: Prometheus Metrics via stdlib

## Status
Accepted

## Date
2026-03-19

## Context
Spark needs a /metrics endpoint for Prometheus scraping to enable Grafana dashboards and alerting on GPU utilization, pod failures, and scheduling behavior. The project's stdlib-only constraint (ADR-001) prohibits the official Prometheus client_golang library.

## Decision
Implement Prometheus text exposition format (version 0.0.4) using only the Go standard library. The format is a simple line-based text protocol: each metric is a line with the metric name, optional labels in curly braces, a space, and the numeric value. TYPE and HELP comment lines precede each metric family.

Metrics will be collected at scrape time (pull model) by reading current state from the PodStore, ResourceTracker, and GPU detector. No in-process metric registry or histogram bucketing -- counters use atomic integers, gauges read live state.

Expose on the existing HTTP server at GET /metrics (same --http-addr).

## Consequences
- Positive: Zero new dependencies. Prometheus/Grafana integration without client_golang.
- Positive: Scrape-time collection means no background goroutine for metrics.
- Negative: No histogram support (would need bucketing logic). Use summary-style gauges for latency if needed later.
- Negative: Must maintain text format compliance manually. Mitigated by comprehensive test coverage.
