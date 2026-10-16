#!/bin/sh
# Waits until the ledger answers its healthcheck (default http://127.0.0.1:3068, 60 s).
url="${1:-${WALLET_HOLDS_TEST_URL:-http://127.0.0.1:3068}}"
for i in $(seq 1 30); do
  if curl -fs "$url/_healthcheck" >/dev/null 2>&1; then
    echo "ledger up at $url"
    exit 0
  fi
  sleep 2
done
echo "ledger at $url did not come up" >&2
exit 1
