#!/usr/bin/env bash
# ZTAPI CLI launcher. Credentials exist only in this process and its child.
set +x
set -euo pipefail

if ! command -v codex >/dev/null 2>&1; then
  printf 'Codex CLI was not found. Install Codex first, then run this launcher again.\n' >&2
  exit 127
fi
if [[ -z "${ZTAPI_API_KEY:-}" ]]; then
  if [[ ! -t 0 ]]; then
    printf 'Run in an interactive terminal to enter your ZTAPI key securely.\n' >&2
    exit 2
  fi
  if ! read -r -s -p 'ZTAPI API Key (hidden): ' ZTAPI_API_KEY; then
    printf '\nKey entry cancelled.\n' >&2
    exit 2
  fi
  printf '\n'
fi
if [[ ! "$ZTAPI_API_KEY" =~ ^sk-[A-Za-z0-9_-]{8,512}$ ]]; then
  printf 'Invalid or empty ZTAPI key. Paste the key itself, not an environment-variable name.\n' >&2
  exit 2
fi
export ZTAPI_API_KEY
printf 'Starting Codex with ZTAPI. Key hidden; existing configuration files are unchanged.\n'
exec codex "$@" \
  -c 'model_provider="ztapi"' \
  -c 'model="zt-claude-opus-5"' \
  -c 'web_search="live"' \
  -c 'model_providers.ztapi.name="ZTAPI"' \
  -c 'model_providers.ztapi.base_url="https://ztapi.vip/v1"' \
  -c 'model_providers.ztapi.wire_api="responses"' \
  -c 'model_providers.ztapi.env_key="ZTAPI_API_KEY"' \
  -c 'model_providers.ztapi.requires_openai_auth=false' \
  -c 'model_providers.ztapi.model_catalog_url="https://ztapi.vip/codex/models"'
