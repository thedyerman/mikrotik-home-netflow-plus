#!/bin/sh
# Run the collector locally against a throwaway data directory.
#   scripts/dev-run.sh [--fresh]
# Without .devdata/dev.env it runs from flow records only on 127.0.0.1:2055
# (feed it with `.devdata/nfp replay testdata/captures/routeros7-ipfix-sample.bin`). Put NFP_* settings in
# .devdata/dev.env to point it at a real router instead.
set -e
cd "$(dirname "$0")/.."
mkdir -p .devdata
[ "$1" = "--fresh" ] && rm -f .devdata/netflow.db .devdata/netflow.db-wal .devdata/netflow.db-shm .devdata/templates.json
go build -o .devdata/nfp ./cmd/mikrotik-home-netflow-plus
export NFP_DATA_DIR=.devdata NFP_HTTP_LISTEN=127.0.0.1:8080 NFP_LOG_LEVEL=debug
if [ -f .devdata/dev.env ]; then
  set -a; . ./.devdata/dev.env; set +a
else
  # These match the sample capture in testdata/captures/.
  export NFP_FLOW_LISTEN=127.0.0.1:2055 NFP_LOCAL_NETS=192.168.88.0/24,fe80::/10 NFP_SITES=office=192.168.99.0/24
fi
exec ./.devdata/nfp
