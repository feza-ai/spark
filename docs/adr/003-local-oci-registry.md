# ADR 003: Local OCI Registry for Image Distribution

## Status
Accepted

## Date
2026-03-18

## Context
Spark executes container jobs on a GPU host (DGX Spark). Clients need to get container images onto the host. Options considered:

1. **Pull from remote registry (ghcr.io, etc.):** Simple but requires internet access and slows job start for large ML images (multi-GB).
2. **SCP/rsync tar archives:** Works but fragile, no deduplication, manual.
3. **Local OCI registry on the spark host:** Clients push images using standard tools (podman push, skopeo). Podman pulls from localhost -- fast, no internet needed.
4. **Spark acts as a custom image receiver over NATS:** Would require reimplementing image transfer, chunking, and integrity checking.

## Decision
Run a local OCI-compliant registry on the spark host using podman's built-in registry support or a lightweight registry container (e.g., `registry:2`). Expose on port 5000 on the local network.

Clients push images to `{spark-host}:5000/{name}:{tag}` using standard OCI tools. Job specs reference `localhost:5000/{name}:{tag}`. Spark also supports pulling from remote registries as a fallback for images not in the local registry.

## Consequences
- Positive: Uses standard OCI tooling -- no custom protocol needed.
- Positive: Image layers are deduplicated and cached.
- Positive: Works offline after initial push.
- Positive: CI/CD pipelines can push images as a build step.
- Negative: Adds a running service (registry container) to manage on the host. Mitigated by running it as a systemd-managed podman container.
- Negative: Port 5000 must be accessible on the local network. Security mitigated by network-level access control (home lab environment).
