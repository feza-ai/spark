#!/bin/sh
set -e

systemctl stop spark.service || true
systemctl stop registry.service || true
systemctl disable spark.service || true
systemctl disable registry.service || true
