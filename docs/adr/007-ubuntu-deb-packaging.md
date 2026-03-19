# ADR 007: Ubuntu .deb Packaging via GoReleaser and nfpm

## Status
Accepted

## Date
2026-03-19

## Context
Spark is deployed to the DGX Spark (Ubuntu, arm64) via a manual shell script (`deploy/install.sh`) that builds the binary locally, SCPs it to the target, and installs systemd units. This approach has several drawbacks:

1. No versioned artifacts -- the binary is built ad hoc from whatever commit is checked out.
2. No package manager integration -- no `apt install`, no dependency tracking, no clean upgrades or rollbacks.
3. Manual config management -- overwriting `/etc/spark/spark.env` on every deploy risks losing user customizations.
4. No standard uninstall path -- removing spark requires manually deleting files and systemd units.

Ubuntu .deb packages solve all of these. The question is how to build them.

Options considered:
- **dpkg-buildpackage**: Full Debian packaging with debian/ directory. Heavyweight, requires Debian toolchain, complex for a single Go binary.
- **GoReleaser + nfpm**: GoReleaser already handles cross-compilation and GitHub releases. nfpm is its built-in packager for .deb/.rpm. Minimal config, integrates with the existing release-please pipeline.
- **Standalone nfpm**: Would work, but GoReleaser already orchestrates the build, so using nfpm standalone adds an unnecessary step.

## Decision
Use GoReleaser with its built-in nfpm integration to produce .deb packages as part of the existing release pipeline.

- GoReleaser cross-compiles for linux/arm64 (DGX) and linux/amd64.
- nfpm section in `.goreleaser.yaml` defines the .deb contents: binary, systemd units, env file, config directory.
- Config files under `/etc/spark/` are marked as conffiles so apt preserves user edits on upgrade.
- Post-install script reloads systemd and enables services.
- Pre-remove script stops services before uninstall.
- .deb artifacts are uploaded to GitHub Releases alongside the tarballs.

## Consequences
- **Positive**: Users install with `dpkg -i spark_*.deb` or from a future APT repository. Upgrades preserve config. Uninstalls are clean. The existing release-please + goreleaser pipeline requires minimal changes.
- **Positive**: arm64 and amd64 are both covered, supporting the DGX and general Ubuntu hosts.
- **Negative**: nfpm .deb packages are simpler than full Debian packages -- no support for complex maintainer scripts, triggers, or Debian policy compliance. This is acceptable for an internal tool.
- **Future**: If Spark is published to a public APT repository, a full debian/ packaging may be warranted. For now, GitHub Releases suffice.
