#!/bin/bash
set -euo pipefail

# Spark + NATS deployment script for DGX
# Usage:
#   1. On your Mac, build and copy files to the DGX:
#        cd /Users/dndungu/Code/feza-ai/spark
#        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o spark-linux-arm64 ./cmd/spark
#        scp spark-linux-arm64 ndungu@192.168.86.250:/tmp/spark
#        scp deploy/nats-server.service ndungu@192.168.86.250:/tmp/nats-server.service
#        scp deploy/spark.service ndungu@192.168.86.250:/tmp/spark.service
#        scp deploy/spark.env ndungu@192.168.86.250:/tmp/spark.env
#        scp deploy/setup-dgx.sh ndungu@192.168.86.250:/tmp/setup-dgx.sh
#
#   2. SSH into the DGX and run:
#        ssh ndungu@192.168.86.250
#        chmod +x /tmp/setup-dgx.sh
#        /tmp/setup-dgx.sh

echo "==> Installing NATS server..."
if ! command -v nats-server &>/dev/null; then
    curl -sf https://binaries.nats.dev/nats-io/nats-server/v2@latest | sh
    sudo mv nats-server /usr/local/bin/
    sudo chmod +x /usr/local/bin/nats-server
    echo "    NATS server installed."
else
    echo "    NATS server already installed: $(nats-server --version 2>&1 || true)"
fi

echo "==> Installing Spark binary..."
if [ -f /tmp/spark ]; then
    sudo mv /tmp/spark /usr/local/bin/spark
    sudo chmod +x /usr/local/bin/spark
    echo "    Spark binary installed."
else
    echo "    ERROR: /tmp/spark not found. Copy it first with scp."
    exit 1
fi

echo "==> Creating directories..."
sudo mkdir -p /etc/spark/manifests
sudo mkdir -p /var/lib/spark

echo "==> Installing systemd units..."
sudo cp /tmp/nats-server.service /etc/systemd/system/nats-server.service
sudo cp /tmp/spark.service /etc/systemd/system/spark.service

if [ -f /tmp/spark.env ]; then
    sudo cp /tmp/spark.env /etc/spark/spark.env
fi

echo "==> Reloading systemd..."
sudo systemctl daemon-reload

echo "==> Enabling and starting NATS server..."
sudo systemctl enable nats-server
sudo systemctl start nats-server
sleep 2

echo "==> Checking NATS server status..."
if systemctl is-active --quiet nats-server; then
    echo "    NATS server is running."
else
    echo "    ERROR: NATS server failed to start."
    sudo systemctl status nats-server --no-pager
    exit 1
fi

echo "==> Enabling and starting Spark..."
sudo systemctl enable spark
sudo systemctl start spark
sleep 3

echo "==> Checking Spark status..."
if systemctl is-active --quiet spark; then
    echo "    Spark is running."
else
    echo "    WARNING: Spark may not be running yet."
    sudo systemctl status spark --no-pager
fi

echo "==> Verifying health endpoint..."
sleep 2
if curl -sf http://localhost:8080/healthz; then
    echo ""
    echo "    Health check passed."
else
    echo "    WARNING: Health check failed. Spark may still be starting up."
    echo "    Check logs with: sudo journalctl -u spark -f"
fi

echo ""
echo "==> Deployment complete!"
echo ""
echo "Useful commands:"
echo "  sudo systemctl status nats-server    # NATS status"
echo "  sudo systemctl status spark          # Spark status"
echo "  sudo journalctl -u spark -f          # Spark logs"
echo "  sudo journalctl -u nats-server -f    # NATS logs"
echo "  curl localhost:8080/healthz           # Health check"
echo "  curl localhost:8080/api/v1/resources  # Resource info"
echo "  curl localhost:8080/api/v1/pods       # Pod list"
