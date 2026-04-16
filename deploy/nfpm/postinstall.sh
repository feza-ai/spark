#!/bin/sh
set -e

systemctl daemon-reload

systemctl enable spark.service || true
systemctl enable registry.service || true
systemctl enable --now spark-auto-upgrade.timer || true
