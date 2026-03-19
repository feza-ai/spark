# Spark: Ubuntu .deb Package Release

## Status: COMPLETE (2026-03-19)

## Context

### Problem Statement

Spark is a single-binary Go pod orchestrator deployed to the DGX Spark (Ubuntu arm64). Today, deployment is a manual shell script (`deploy/install.sh`) that builds, SCPs, and installs. There are no versioned artifacts, no package manager integration, no clean upgrade/rollback path, and no config preservation on upgrades.

The goal is to produce Ubuntu .deb packages as part of the existing release-please + GoReleaser pipeline so that Spark can be installed, upgraded, and removed via standard package management (`dpkg -i`, `apt install`).

### Objectives

1. Produce .deb packages for linux/arm64 and linux/amd64 on every GitHub release.
2. Package includes: binary, systemd units (spark.service, registry.service), default config (/etc/spark/spark.env), empty manifest directory.
3. Config files are marked as conffiles so apt preserves user edits on upgrade.
4. Post-install script reloads systemd and enables services. Pre-remove script stops services.
5. .deb artifacts are uploaded to GitHub Releases alongside existing tarballs.

### Non-Goals

- Public APT repository or PPA (future work).
- RPM packages (no current need).
- Full Debian policy compliance with debian/ directory (overkill for internal tool).
- Replacing the manual deploy/install.sh script (kept as fallback).

### Constraints

- Must integrate with existing release-please + GoReleaser pipeline (.github/workflows/release-please.yml).
- GoReleaser v2 syntax required (goreleaser-action@v6 uses v2).
- No new CI dependencies beyond what GoReleaser provides (nfpm is built-in).

### Success Metrics

- `dpkg -i spark_1.0.0_arm64.deb` installs spark, systemd units, and config on a clean Ubuntu host.
- `apt upgrade` preserves user-modified /etc/spark/spark.env.
- `dpkg -r spark` stops services and removes all installed files.
- GitHub Release for the next tag contains .deb files for arm64 and amd64.

## Discovery Summary

**Existing infrastructure:**
- release-please action creates GitHub releases with semver tags on merge to main.
- goreleaser-action@v6 runs on release creation, but no .goreleaser.yaml exists yet -- goreleaser would use defaults (tarball only, no packages).
- deploy/ contains systemd units (spark.service, registry.service), env file (spark.env), and install script.
- Binary entry point: cmd/spark/main.go. Module: github.com/feza-ai/spark.
- Target architecture: linux/arm64 (DGX Spark, NVIDIA Grace CPU). Secondary: linux/amd64.

**Use case impact:** No new use cases. This is infrastructure work -- packaging the existing binary for distribution. Existing use case manifest unchanged (15 use cases, all WIRED).

Decision rationale: docs/adr/007-ubuntu-deb-packaging.md

## Scope and Deliverables

### In Scope

| ID | Deliverable | Acceptance Criteria |
|----|-------------|---------------------|
| D1 | .goreleaser.yaml | Cross-compiles arm64+amd64, produces .deb via nfpm, includes systemd units and config |
| D2 | Post-install script | Runs systemd daemon-reload, enables spark.service and registry.service |
| D3 | Pre-remove script | Stops spark.service and registry.service before package removal |
| D4 | Verified .deb package | Local goreleaser build produces installable .deb with correct contents |

### Out of Scope

- APT repository hosting
- RPM packaging
- CI workflow structural changes (release-please.yml already triggers goreleaser)
- darwin/macOS builds (spark is a Linux server binary)

## Checkable Work Breakdown

### E18: Ubuntu .deb Package Release

- [x] T18.1 Add nfpm .deb configuration to .goreleaser.yml  Owner: done  Est: 30m  verifies: [infrastructure]  (2026-03-19)
  - Create `.goreleaser.yaml` at repo root.
  - `builds` section: single build, entry `./cmd/spark`, GOOS=linux, GOARCH=[arm64, amd64].
  - `nfpms` section: package name `spark`, maintainer `feza-ai`, description, license.
  - Contents: binary to `/usr/local/bin/spark`, `deploy/spark.service` to `/etc/systemd/system/spark.service`, `deploy/registry.service` to `/etc/systemd/system/registry.service`, `deploy/spark.env` to `/etc/spark/spark.env`.
  - Mark `/etc/spark/spark.env` as config file (nfpm `type: config`).
  - Create empty `/etc/spark/manifests/` directory via contents entry.
  - Set `scripts.postinstall` and `scripts.preremove` to packaging scripts.
  - `formats: [deb]`.
  - Acceptance: `goreleaser check` passes. YAML is valid GoReleaser v2 syntax.

- [x] T18.2 Create post-install and pre-remove packaging scripts  Owner: done  Est: 15m  verifies: [infrastructure]  (2026-03-19)
  - Create `deploy/nfpm/postinstall.sh`:
    - `systemctl daemon-reload`
    - `systemctl enable spark.service`
    - `systemctl enable registry.service`
    - Do NOT start services automatically (user should configure spark.env first on fresh install).
  - Create `deploy/nfpm/preremove.sh`:
    - `systemctl stop spark.service || true`
    - `systemctl stop registry.service || true`
    - `systemctl disable spark.service || true`
    - `systemctl disable registry.service || true`
  - Both scripts must be `#!/bin/sh`, POSIX-compliant, idempotent.
  - Acceptance: Scripts are executable. shellcheck passes (if available).

- [x] T18.3 Verify local goreleaser snapshot build  Owner: done  Est: 15m  verifies: [infrastructure]  (2026-03-19)
  - Depends on: T18.1, T18.2.
  - Run `goreleaser build --snapshot --clean` to verify the build configuration works.
  - Run `goreleaser release --snapshot --clean` to produce .deb artifacts locally.
  - Verify .deb contents: `dpkg-deb -c dist/spark_*.deb` shows binary, systemd units, env file, manifests dir.
  - Verify conffiles: `dpkg-deb --info dist/spark_*.deb` shows /etc/spark/spark.env as conffile.
  - Verify both arm64 and amd64 .deb files are produced.
  - Acceptance: Two .deb files exist in dist/. Contents match expected file layout. `goreleaser check` passes.

- [x] T18.4 Run lint and vet on any code changes  Owner: done  Est: 10m  verifies: [infrastructure]  (2026-03-19)
  - Depends on: T18.1, T18.2.
  - Run `go vet ./...` and `go build ./...` to confirm no regressions.
  - Run `staticcheck ./...` if available.
  - Acceptance: Zero warnings, zero errors.

## Parallel Work

### Tracks

| Track | Tasks | Description |
|-------|-------|-------------|
| A: GoReleaser Config | T18.1 | .goreleaser.yaml creation |
| B: Packaging Scripts | T18.2 | postinstall and preremove scripts |

Sync point: T18.3 and T18.4 require both A and B to complete.

### Waves

### Wave 1: Configuration (2 agents)
- [x] T18.1 Add nfpm .deb configuration to .goreleaser.yml  verifies: [infrastructure]
- [x] T18.2 Create post-install and pre-remove packaging scripts  verifies: [infrastructure]

### Wave 2: Verification (2 agents)
- [x] T18.3 Verify local goreleaser snapshot build  verifies: [infrastructure]
- [x] T18.4 Run lint and vet on any code changes  verifies: [infrastructure]

## Timeline and Milestones

| ID | Milestone | Dependencies | Exit Criteria |
|----|-----------|--------------|---------------|
| M1 | Config complete | T18.1, T18.2 | .goreleaser.yaml and packaging scripts committed |
| M2 | Verified .deb | T18.3, T18.4 | Local snapshot produces correct .deb packages |

## Risk Register

| ID | Risk | Impact | Likelihood | Mitigation |
|----|------|--------|------------|------------|
| R1 | goreleaser not installed locally | Medium | Medium | T18.3 instructions include install step. CI has it via goreleaser-action. |
| R2 | nfpm conffiles syntax incorrect | Low | Low | Verify with dpkg-deb --info after build. |
| R3 | arm64 cross-compilation fails on amd64 CI runner | Medium | Low | GoReleaser handles cross-compilation via Go toolchain. No CGO needed. |

## Operating Procedure

### Definition of Done
- `goreleaser check` passes.
- `goreleaser release --snapshot --clean` produces .deb files for arm64 and amd64.
- .deb contents match expected layout (binary, systemd units, config, manifests dir).
- Config files are marked as conffiles.
- Packaging scripts are POSIX-compliant and idempotent.
- All existing tests still pass (`go test ./... -race -timeout 120s`).
- Zero warnings from `go vet`.

### Review and QA
- Rebase and merge (not squash, not merge commits).
- Each commit is a small, logical unit touching one directory.

## Progress Log

### 2026-03-19: Plan created
- Created plan for Ubuntu .deb packaging via GoReleaser + nfpm.
- Created ADR 007 (docs/adr/007-ubuntu-deb-packaging.md): documents decision to use GoReleaser + nfpm over dpkg-buildpackage.
- 4 tasks across 2 waves. Estimated total: ~70 minutes.

## Hand-off Notes

- Spark is a single-binary Go pod orchestrator. See docs/design.md for architecture.
- The existing CI pipeline (release-please + goreleaser) already triggers on release creation. Adding .goreleaser.yaml is all that is needed to produce .deb packages.
- DGX Spark target: `ssh ndungu@192.168.86.250` (Ubuntu arm64, NVIDIA Grace CPU).
- After this plan completes, the next release tag will automatically produce .deb files on GitHub Releases.
- To install on DGX: download .deb from GitHub releases, run `sudo dpkg -i spark_*_arm64.deb`, then edit `/etc/spark/spark.env` and `sudo systemctl start spark.service`.
- Manual deploy script (`deploy/install.sh`) remains as a fallback for development iteration.
