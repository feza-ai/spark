#!/usr/bin/env bash
set -euo pipefail

# Usage: deploy/install.sh [user@host]
#
# Builds the spark binary for linux/arm64, copies it and systemd units
# to the target DGX host, and enables the services.
#
# Arguments:
#   user@host   SSH destination (default: ndungu@192.168.86.250)
#
# Prerequisites:
#   - Go toolchain installed locally
#   - SSH access to the target host
#   - sudo privileges on the target host
#
# This script is idempotent and safe to run multiple times.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TARGET="${1:-ndungu@192.168.86.250}"
BINARY="spark"
REMOTE_BIN="/usr/local/bin/${BINARY}"
REMOTE_SYSTEMD="/etc/systemd/system"
REMOTE_CONF="/etc/spark"

echo "==> Building ${BINARY} for linux/arm64"
cd "${PROJECT_ROOT}"
GOOS=linux GOARCH=arm64 go build -o "${BINARY}" ./cmd/spark

echo "==> Copying binary to ${TARGET}:${REMOTE_BIN}"
scp "${BINARY}" "${TARGET}:/tmp/${BINARY}"
ssh "${TARGET}" "sudo mv /tmp/${BINARY} ${REMOTE_BIN} && sudo chmod 755 ${REMOTE_BIN}"

echo "==> Copying systemd units"
scp "${SCRIPT_DIR}/spark.service" "${TARGET}:/tmp/spark.service"
scp "${SCRIPT_DIR}/registry.service" "${TARGET}:/tmp/registry.service"
ssh "${TARGET}" "sudo mv /tmp/spark.service ${REMOTE_SYSTEMD}/spark.service && sudo mv /tmp/registry.service ${REMOTE_SYSTEMD}/registry.service"

echo "==> Ensuring config directory and env file exist"
ssh "${TARGET}" "sudo mkdir -p ${REMOTE_CONF}/manifests && { [ -f ${REMOTE_CONF}/spark.env ] || sudo cp /dev/stdin ${REMOTE_CONF}/spark.env; }" < "${SCRIPT_DIR}/spark.env"

echo "==> Reloading systemd daemon"
ssh "${TARGET}" "sudo systemctl daemon-reload"

echo "==> Enabling and starting services"
ssh "${TARGET}" "sudo systemctl enable --now registry.service"
ssh "${TARGET}" "sudo systemctl enable --now spark.service"

echo "==> Verifying services"
ssh "${TARGET}" "systemctl is-active spark.service && systemctl is-active registry.service"

rm -f "${PROJECT_ROOT}/${BINARY}"

echo "==> Deploy complete to ${TARGET}"
