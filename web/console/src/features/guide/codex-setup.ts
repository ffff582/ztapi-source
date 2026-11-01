export const codexQualifiedModel = 'zt-claude-opus-5';

export function codexConfig(model = codexQualifiedModel) {
  return [
    'model_provider = "ztapi"',
    `model = ${JSON.stringify(model)}`,
    ...(model === codexQualifiedModel ? ['web_search = "live"'] : []),
    '',
    '[model_providers.ztapi]',
    'name = "ZTAPI"',
    'base_url = "https://ztapi.vip/v1"',
    'wire_api = "responses"',
    'env_key = "ZTAPI_API_KEY"',
    'requires_openai_auth = false',
    'model_catalog_url = "https://ztapi.vip/codex/models"',
  ].join('\n');
}

export const codexWindowsKey = [
  '$key = Read-Host "ZTAPI API Key" -AsSecureString',
  '$env:ZTAPI_API_KEY = [System.Net.NetworkCredential]::new("", $key).Password',
  'Remove-Variable key',
  'codex',
].join('\n');

export const codexPosixKey = [
  'printf "ZTAPI API Key: "',
  'read -r -s ZTAPI_API_KEY',
  'printf "\\n"',
  'export ZTAPI_API_KEY',
  'codex',
].join('\n');

export const codexWindowsDesktopKey = '[Environment]::SetEnvironmentVariable("ZTAPI_API_KEY", $env:ZTAPI_API_KEY, "User")';
export const codexMacDesktopKey = 'launchctl setenv ZTAPI_API_KEY "$ZTAPI_API_KEY"';
