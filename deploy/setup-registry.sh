#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> Installing registry.service"
cp "${SCRIPT_DIR}/registry.service" /etc/systemd/system/registry.service

echo "==> Reloading systemd daemon"
systemctl daemon-reload

echo "==> Enabling and starting registry service"
systemctl enable --now registry.service

echo "==> Configuring podman for insecure registry on localhost:5000"
mkdir -p /etc/containers/registries.conf.d
cat > /etc/containers/registries.conf.d/spark-localhost.conf <<'TOML'
[[registry]]
location = "localhost:5000"
insecure = true
TOML

echo "==> Waiting for registry to become ready"
for i in $(seq 1 10); do
    if curl -sf http://localhost:5000/v2/ > /dev/null 2>&1; then
        break
    fi
    sleep 1
done

echo "==> Testing: pull, tag, push alpine image"
podman pull docker.io/library/alpine:latest
podman tag docker.io/library/alpine:latest localhost:5000/alpine:test
podman push localhost:5000/alpine:test

echo "==> Registry setup complete. localhost:5000 is ready."
