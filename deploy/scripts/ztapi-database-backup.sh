#!/usr/bin/env bash
set -Eeuo pipefail

readonly APP_ROOT=/opt/ztapi
readonly BACKUP_DIR="$APP_ROOT/backups"
readonly COMPOSE_FILE="$APP_ROOT/deploy/docker/docker-compose.prod.yml"
readonly ENV_FILE="$APP_ROOT/.env"
readonly HEALTH_ENV=/etc/ztapi/health.env
readonly RETENTION_DAYS=30

send_failure_alert() {
  local message=$1
  if [[ ! -r "$HEALTH_ENV" ]]; then
    printf 'backup alert skipped: health environment is unavailable\n' >&2
    return 1
  fi

  local bot_token='' chat_id='' response=''
  set +u
  # The file is root-owned, mode 0600, and is the existing watchdog secret source.
  source "$HEALTH_ENV"
  bot_token="${ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN:-}"
  chat_id="${ZTAPI_HEALTH_TELEGRAM_CHAT_ID:-}"
  set -u
  if [[ -z "$bot_token" || -z "$chat_id" ]]; then
    printf 'backup alert skipped: Telegram recipient is not configured\n' >&2
    return 1
  fi

  response=$(curl --fail --silent --show-error --connect-timeout 10 --max-time 30 \
    --data-urlencode "chat_id=$chat_id" \
    --data-urlencode "text=$message" \
    "https://api.telegram.org/bot${bot_token}/sendMessage")
  python3 -c 'import json, sys; payload = json.load(sys.stdin); raise SystemExit(0 if payload.get("ok") is True else 1)' <<< "$response"
}

run_backup() {
  install -d -m 0700 "$BACKUP_DIR"
  local stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  local final="$BACKUP_DIR/database-$stamp.sql.gz"
  local tmp="$final.tmp.$$"
  local checksum="$final.sha256"
  local checksum_tmp="$checksum.tmp.$$"
  local stat_file="$final.stat"
  local stat_tmp="$stat_file.tmp.$$"

  trap 'rm -f -- "$tmp" "$checksum_tmp" "$stat_tmp"' RETURN

  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" up -d mysql --wait --wait-timeout 120 >/dev/null
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" exec -T mysql sh -c \
    'exec mysqldump --single-transaction --quick --lock-tables=false --no-tablespaces --user="$MYSQL_USER" --password="$MYSQL_PASSWORD" "$MYSQL_DATABASE"' |
    gzip -c > "$tmp"
  test -s "$tmp"
  gunzip -t "$tmp"
  chmod 0600 "$tmp"
  mv -f -- "$tmp" "$final"
  sha256sum "$final" > "$checksum_tmp"
  sha256sum -c "$checksum_tmp" >/dev/null
  stat -c '%U:%G %a %s' "$final" > "$stat_tmp"
  chmod 0600 "$final" "$checksum_tmp" "$stat_tmp"
  mv -f -- "$checksum_tmp" "$checksum"
  mv -f -- "$stat_tmp" "$stat_file"

  find "$BACKUP_DIR" -maxdepth 1 -type f -name 'database-*.sql.gz' -mtime +"$RETENTION_DAYS" -delete
  find "$BACKUP_DIR" -maxdepth 1 -type f -name 'database-*.sql.gz.sha256' -mtime +"$RETENTION_DAYS" -delete
  find "$BACKUP_DIR" -maxdepth 1 -type f -name 'database-*.sql.gz.stat' -mtime +"$RETENTION_DAYS" -delete
  printf 'database backup complete: %s\n' "$final"
}

exec 9>/var/lock/ztapi-database-backup.lock
if ! flock -n 9; then
  printf 'database backup skipped: another run is active\n'
  exit 0
fi

set +e
(set -e; run_backup)
rc=$?
set -e
if (( rc != 0 )); then
  message=$(printf '[ZTAPI] 每日数据库备份失败\n主机: %s\n时间: %s\n错误: backup command failed (exit %s)' \
    "$(hostname)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$rc")
  send_failure_alert "$message" || true
  exit "$rc"
fi
