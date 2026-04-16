#!/usr/bin/env bash
# spark-auto-upgrade.sh -- check GitHub releases for a newer spark .deb and install it.
# Designed to run as a systemd timer (spark-auto-upgrade.timer).
# Logs to journald via systemd; no separate log file needed.
set -euo pipefail

REPO="feza-ai/spark"
ARCH="arm64"
DEB_PATTERN="spark_.*_linux_${ARCH}\\.deb"
TMP_DIR="/tmp/spark-upgrade"

# Determine installed version from dpkg. Exits cleanly if spark is not installed via .deb.
installed_version() {
    dpkg-query -W -f='${Version}' spark 2>/dev/null || echo "0.0.0"
}

# Query the GitHub API for the latest release tag (unauthenticated, 60 req/hr).
latest_release() {
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep -o '"tag_name": *"[^"]*"' \
        | head -1 \
        | sed 's/.*"v\?\([^"]*\)"/\1/'
}

# Compare two semver strings. Returns 0 if $1 > $2.
is_newer() {
    [ "$(printf '%s\n%s' "$1" "$2" | sort -V | tail -1)" = "$1" ] && [ "$1" != "$2" ]
}

echo "spark-auto-upgrade: checking for updates"

INSTALLED=$(installed_version)
LATEST=$(latest_release)

echo "installed=${INSTALLED} latest=${LATEST}"

if ! is_newer "${LATEST}" "${INSTALLED}"; then
    echo "spark-auto-upgrade: up to date (${INSTALLED})"
    exit 0
fi

echo "spark-auto-upgrade: upgrading ${INSTALLED} -> ${LATEST}"

mkdir -p "${TMP_DIR}"
trap 'rm -rf "${TMP_DIR}"' EXIT

# Find the arm64 .deb asset URL.
DEB_URL=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -o '"browser_download_url": *"[^"]*"' \
    | grep "${DEB_PATTERN}" \
    | head -1 \
    | sed 's/.*"\(https[^"]*\)"/\1/')

if [ -z "${DEB_URL}" ]; then
    echo "spark-auto-upgrade: ERROR no arm64 .deb found in release ${LATEST}" >&2
    exit 1
fi

DEB_FILE="${TMP_DIR}/spark_${LATEST}_linux_${ARCH}.deb"
echo "spark-auto-upgrade: downloading ${DEB_URL}"
curl -fsSL -o "${DEB_FILE}" "${DEB_URL}"

echo "spark-auto-upgrade: installing ${DEB_FILE}"
DEBIAN_FRONTEND=noninteractive dpkg -i --force-confold "${DEB_FILE}"

echo "spark-auto-upgrade: restarting spark.service"
systemctl restart spark.service

# Wait briefly and verify the service came back.
sleep 3
if systemctl is-active --quiet spark.service; then
    echo "spark-auto-upgrade: upgrade to ${LATEST} successful, spark.service active"
else
    echo "spark-auto-upgrade: WARNING spark.service not active after upgrade" >&2
    systemctl status spark.service --no-pager || true
    exit 1
fi
