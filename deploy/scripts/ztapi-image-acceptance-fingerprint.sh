#!/usr/bin/env bash
set -euo pipefail

root=${1:?usage: ztapi-image-acceptance-fingerprint.sh RELEASE_ROOT}

# Only production surfaces that can change GPT Image 2 routing, protocol,
# metering, settlement, publication, quotation, or deployment acceptance belong
# here. Test-only, text-only, and UI-only changes must not spend upstream image
# credits during deployment.
files=(
  '.github/workflows/ztapi-deploy.yml'
  'deploy/scripts/ztapi-image-acceptance-fingerprint.sh'
  'server/controller/ztapi_media_contract.go'
  'server/controller/ztapi_model.go'
  'server/controller/ztapi_model_evidence.go'
  'server/dto/openai_response.go'
  'server/middleware/ztapi_request_protocol.go'
  'server/model/pricing.go'
  'server/model/ztapi_modality.go'
  'server/model/ztapi_model_config.go'
  'server/model/ztapi_model_evidence.go'
  'server/model/ztapi_publication_gate.go'
  'server/model/ztapi_quotation.go'
  'server/model/ztapi_quotation_v1.json'
  'server/relay/channel/api_request.go'
  'server/relay/channel/openai/relay_image.go'
  'server/relay/common/relay_info.go'
  'server/relay/common/ztapi_media_usage.go'
  'server/relay/common/ztapi_usage_dimensions.go'
  'server/relay/helper/valid_request.go'
  'server/relay/image_handler.go'
  'server/relay/ztapi_image_contract.go'
  'server/relay/ztapi_image_diagnostic.go'
  'server/router/relay-router.go'
  'server/service/text_quota.go'
  'server/service/tiered_settle.go'
  'server/service/ztapi_durable_billing.go'
  'server/service/ztapi_media_billing.go'
  'server/service/ztapi_model_verifier.go'
  'server/types/request_meta.go'
  'server/types/ztapi_image_contract.go'
  'server/types/ztapi_media_pricing.go'
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
