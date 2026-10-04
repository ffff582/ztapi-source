#!/usr/bin/env bash
set -euo pipefail

root=${1:?usage: ztapi-video-acceptance-fingerprint.sh RELEASE_ROOT}

# Only surfaces that can change Seedance routing, task polling, metering,
# settlement, publication or deployment acceptance belong here.
files=(
  '.github/workflows/ztapi-deploy.yml'
  'deploy/scripts/ztapi-video-acceptance-fingerprint.sh'
  'server/controller/ztapi_media_contract.go'
  'server/controller/ztapi_model.go'
  'server/controller/ztapi_model_evidence.go'
  'server/middleware/ztapi_request_protocol.go'
  'server/model/ztapi_media_task.go'
  'server/model/ztapi_modality.go'
  'server/model/ztapi_model_config.go'
  'server/model/ztapi_model_evidence.go'
  'server/model/ztapi_publication_gate.go'
  'server/model/ztapi_quotation.go'
  'server/model/ztapi_quotation_v1.json'
  'server/relay/channel/task/aihub/seedance.go'
  'server/relay/relay_adaptor.go'
  'server/relay/relay_task.go'
  'server/relay/ztapi_video_contract.go'
  'server/router/video-router.go'
  'server/service/task_polling.go'
  'server/service/ztapi_durable_billing.go'
  'server/service/ztapi_media_task.go'
  'server/service/ztapi_model_verifier.go'
  'server/types/ztapi_media_pricing.go'
  'server/types/ztapi_seedance_contract.go'
  'server/types/ztapi_video_contract.go'
)

for relative_path in "${files[@]}"; do
  absolute_path="$root/$relative_path"
  if [ -f "$absolute_path" ]; then
    printf 'file\t%s\t' "$relative_path"
    sha256sum "$absolute_path" | cut -d ' ' -f 1
  else
    printf 'missing\t%s\n' "$relative_path"
  fi
done | sha256sum | cut -d ' ' -f 1
