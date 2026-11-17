#!/bin/sh
set -eu

usage=$(df -P / | awk 'NR == 2 { gsub(/%/, "", $5); print $5 }')
case "$usage" in
  ''|*[!0-9]*)
    logger -p user.err -t ztapi-disk-cache 'Unable to read root disk usage'
    exit 1
    ;;
esac

if [ "$usage" -lt 80 ]; then
  exit 0
fi

logger -t ztapi-disk-cache "Root disk at ${usage}%; pruning Docker build cache older than 48 hours"
if ! docker builder prune --all --force --filter until=48h >/dev/null; then
  logger -p user.err -t ztapi-disk-cache 'Docker build cache prune failed'
  exit 1
fi

remaining=$(df -P / | awk 'NR == 2 { print $5 }')
logger -t ztapi-disk-cache "Root disk after prune: ${remaining}"
