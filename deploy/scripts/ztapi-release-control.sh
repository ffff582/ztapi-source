#!/usr/bin/env bash
set -euo pipefail
umask 077

action=${1:?usage: ztapi-release-control.sh finalize|rollback RELEASE_COMMIT RELEASE_EXECUTION_ID}
release_commit=${2:?usage: ztapi-release-control.sh finalize|rollback RELEASE_COMMIT RELEASE_EXECUTION_ID}
release_execution_id=${3:?usage: ztapi-release-control.sh finalize|rollback RELEASE_COMMIT RELEASE_EXECUTION_ID}
[[ "$release_commit" =~ ^[0-9a-f]{40}$ ]]
[[ "$release_execution_id" =~ ^[0-9]+-[0-9]+$ ]]
receipt_dir="/opt/ztapi/release-receipts/$release_commit/$release_execution_id"
state_file="$receipt_dir/rollback-state.env"
watchdog_unit="ztapi-release-watchdog-${release_commit:0:12}-$release_execution_id"
watchdog_timer="$watchdog_unit.timer"
lock_file="/opt/ztapi/release-receipts/release-control.lock"
active_execution_file="/opt/ztapi/release-receipts/active-execution.env"
mkdir -p "$receipt_dir"
exec 9>"$lock_file"
flock 9
if [ "$action" = finalize ] && [ -s "$receipt_dir/rollback.receipt" ]; then
  echo "release $release_commit was already rolled back; refusing to finalize" >&2
  exit 1
fi
if [ "$action" = finalize ] && [ -s "$receipt_dir/completed.receipt" ]; then
  exit 0
fi
if [ "$action" = rollback ] && \
  { [ -s "$receipt_dir/rollback.receipt" ] || [ -s "$receipt_dir/completed.receipt" ]; }; then
  exit 0
fi
if [ ! -s "$active_execution_file" ] ||
  ! grep -Fxq "commit=$release_commit" "$active_execution_file" ||
  ! grep -Fxq "execution_id=$release_execution_id" "$active_execution_file"; then
  if [ "$action" = rollback ]; then
    echo "release execution $release_execution_id is no longer active; skipping stale rollback" >&2
    exit 0
  fi
  echo "release execution $release_execution_id is no longer active; refusing to finalize" >&2
  exit 1
fi
if [ ! -s "$state_file" ]; then
  echo "release control state is missing for $release_commit" >&2
  exit 1
fi
# shellcheck disable=SC1090
. "$state_file"
test "$ZTAPI_RELEASE_VERSION" = "$release_commit"
test "$ZTAPI_RELEASE_EXECUTION_ID" = "$release_execution_id"

write_receipt() {
  phase=$1
  detail=${2:-}
  receipt_tmp="$receipt_dir/$phase.receipt.tmp"
  {
    printf 'commit=%s\n' "$release_commit"
    printf 'phase=%s\n' "$phase"
    printf 'at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'detail=%s\n' "$detail"
  } > "$receipt_tmp"
  chmod 0600 "$receipt_tmp"
  mv "$receipt_tmp" "$receipt_dir/$phase.receipt"
}

remove_rollback_tags() {
  [ -z "${rollback_server_image:-}" ] || docker image rm "$rollback_server_image" >/dev/null 2>&1 || true
  [ -z "${rollback_nginx_image:-}" ] || docker image rm "$rollback_nginx_image" >/dev/null 2>&1 || true
}

stop_watchdog() {
  systemctl stop "$watchdog_timer" >/dev/null 2>&1 || true
  systemctl reset-failed "$watchdog_timer" "$watchdog_unit.service" >/dev/null 2>&1 || true
}

restore_registration_options() {
  test "${previous_register_enabled:-}" = true || test "$previous_register_enabled" = false
  test "${previous_password_register_enabled:-}" = true || test "$previous_password_register_enabled" = false
  printf "INSERT INTO options (\`key\`, \`value\`) VALUES ('RegisterEnabled','%s'),('PasswordRegisterEnabled','%s') ON DUPLICATE KEY UPDATE \`value\`=VALUES(\`value\`);\n" \
    "$previous_register_enabled" "$previous_password_register_enabled" |
    docker compose --env-file /opt/ztapi/.env \
      -f /opt/ztapi/deploy/docker/docker-compose.prod.yml \
      exec -T mysql sh -c \
        'exec mysql --user="$MYSQL_USER" --password="$MYSQL_PASSWORD" "$MYSQL_DATABASE"'
}

verify_runtime() {
  for container in ztapi-server-1 ztapi-nginx-1 ztapi-mysql-1 ztapi-redis-1; do
    test "$(docker inspect --format '{{.State.Status}}' "$container")" = running
    test "$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container")" = healthy
  done
}

case "$action" in
  finalize)
    stop_watchdog
    write_receipt completed "external_acceptance=passed"
    remove_rollback_tags
    rm -f "$state_file"
    rm -f "$active_execution_file"
    ;;
  rollback)
    stop_watchdog
    rollback_status=0
    if [ "${had_previous_release:-false}" = true ]; then
      test -s "$release_backup"
      test -s "$env_backup"
      rollback_dir=$(mktemp -d /tmp/ztapi-external-rollback.XXXXXX)
      tar -xzf "$release_backup" -C "$rollback_dir"
      rsync -a --chown=root:root --delete \
        --exclude='.env' --exclude='tls/' --exclude='backups/' --exclude='release-receipts/' \
        "$rollback_dir/" /opt/ztapi/
      install -m 0600 "$env_backup" /opt/ztapi/.env
      rm -rf "$rollback_dir"
      compose=(docker compose --env-file /opt/ztapi/.env -f /opt/ztapi/deploy/docker/docker-compose.prod.yml)
      "${compose[@]}" up -d mysql redis --wait --wait-timeout 180
      restore_registration_options
      "${compose[@]}" up -d --force-recreate server --wait --wait-timeout 180
      "${compose[@]}" up -d --force-recreate nginx --wait --wait-timeout 120
      verify_runtime
    else
      # The first unlock still mutates the persistent options table. Restore it
      # while the new MySQL stack is available, then remove the unaccepted stack.
      restore_registration_options || rollback_status=$?
      docker compose --env-file /opt/ztapi/.env \
        -f /opt/ztapi/deploy/docker/docker-compose.prod.yml down --remove-orphans || rollback_status=$?
      if [ "${host_nginx_was_active:-false}" = true ]; then
        systemctl enable --now nginx || rollback_status=$?
      fi
    fi
    if [ -s "${unrelated_before:-}" ]; then
      unrelated_after="$receipt_dir/unrelated-containers.external-rollback.json"
      docker inspect deploy-api-1 deploy-database-1 kuaiyi-database-1 |
        jq -S -c '[.[] | {id:.Id,name:.Name,image:.Image,state:.State.Status,health:(.State.Health.Status // "none"),started_at:.State.StartedAt,restart_count:.RestartCount}] | sort_by(.name)' \
        > "$unrelated_after" || rollback_status=$?
      chmod 0600 "$unrelated_after"
      cmp -s "$unrelated_before" "$unrelated_after" || rollback_status=$?
    fi
    write_receipt rollback "external_acceptance=failed restore_status=$rollback_status"
    test "$rollback_status" -eq 0
    remove_rollback_tags
    rm -f "$state_file"
    rm -f "$active_execution_file"
    ;;
  *)
    echo "unknown release-control action: $action" >&2
    exit 2
    ;;
esac
