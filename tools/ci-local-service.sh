#!/usr/bin/env bash
set -euo pipefail

action=${1:?start or stop required}
kind=${2:?mysql or redis required}
run_id=${GITHUB_RUN_ID:?GitHub run ID required}
attempt=${GITHUB_RUN_ATTEMPT:?GitHub run attempt required}
job=${GITHUB_JOB:?GitHub job required}
name="ztapi-ci-${kind}-${run_id}-${attempt}-${job}"

if [[ "$action" == stop ]]; then
  docker rm --force "$name" >/dev/null 2>&1 || true
  exit 0
fi
if [[ "$action" != start ]]; then
  echo "unsupported action: $action" >&2
  exit 1
fi

password=${3:?isolated CI password required}
case "$kind" in
  mysql)
    database=${4:?isolated CI database required}
    image=mysql:8.4
    docker image inspect "$image" >/dev/null
    docker run --pull=never --rm -d --name "$name" \
      --label ztapi-ci-service=true \
      -e "MYSQL_ROOT_PASSWORD=$password" -e "MYSQL_DATABASE=$database" \
      -p 127.0.0.1:3306:3306 \
      --health-cmd='mysqladmin ping -h 127.0.0.1 -uroot -p"$MYSQL_ROOT_PASSWORD"' \
      --health-interval=5s --health-timeout=5s --health-retries=20 \
      "$image" >/dev/null
    ;;
  redis)
    image=redis:7.4-alpine
    docker image inspect "$image" >/dev/null
    docker run --pull=never --rm -d --name "$name" \
      --label ztapi-ci-service=true \
      -e "REDIS_PASSWORD=$password" -p 127.0.0.1:6379:6379 \
      --health-cmd='REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping' \
      --health-interval=5s --health-timeout=5s --health-retries=20 \
      "$image" redis-server --requirepass "$password" >/dev/null
    ;;
  *)
    echo "unsupported service: $kind" >&2
    exit 1
    ;;
esac

for _ in {1..60}; do
  status=$(docker inspect --format '{{.State.Health.Status}}' "$name" 2>/dev/null || true)
  if [[ "$status" == healthy ]]; then
    echo "$kind is healthy"
    exit 0
  fi
  if [[ "$status" == unhealthy ]]; then
    break
  fi
  sleep 2
done
echo "$kind did not become healthy" >&2
docker logs --tail 20 "$name" >&2
exit 1
