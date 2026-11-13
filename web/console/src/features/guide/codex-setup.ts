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
  'if ([string]::IsNullOrWhiteSpace($env:ZTAPI_API_KEY)) {',
  '  Write-Error "No key entered; Codex was not started."',
  '} else {',
  '  codex',
  '}',
].join('\n');

export const codexPosixKey = [
  'printf "ZTAPI API Key: "',
  'read -r -s ZTAPI_API_KEY',
  'printf "\\n"',
  'if [ -n "$ZTAPI_API_KEY" ]; then',
  '  export ZTAPI_API_KEY',
  '  codex',
  'else',
  '  printf "No key entered; Codex was not started.\\n" >&2',
  'fi',
].join('\n');

export const codexPosixLauncher = [
  'setup_dir="$(mktemp -d)" &&',
  'curl --fail --show-error --location --proto "=https" --proto-redir "=https" --tlsv1.2 \\',
  '  https://ztapi.vip/assets/ztapi-codex-20261104.sh \\',
  '  -o "$setup_dir/ztapi-codex.sh" &&',
  'bash "$setup_dir/ztapi-codex.sh"',
].join('\n');

export const codexWindowsLauncher = [
  '& {',
  '  $setup = Join-Path ([IO.Path]::GetTempPath()) ("ztapi-codex-" + [guid]::NewGuid().ToString("N") + ".ps1")',
  '  Invoke-WebRequest -Uri "https://ztapi.vip/assets/ztapi-codex-20261104.ps1" -OutFile $setup -ErrorAction Stop',
  '  & $setup',
  '}',
].join('\n');

export const codexWindowsDesktopKey = '[Environment]::SetEnvironmentVariable("ZTAPI_API_KEY", $env:ZTAPI_API_KEY, "User")';
export const codexMacDesktopKey = 'launchctl setenv ZTAPI_API_KEY "$ZTAPI_API_KEY"';
