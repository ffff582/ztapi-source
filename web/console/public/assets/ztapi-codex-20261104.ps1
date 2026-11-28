# ZTAPI CLI launcher. No credential files or persistent environment changes.
param()
$ErrorActionPreference = 'Stop'
$command = Get-Command codex -CommandType Application,ExternalScript -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $command) { throw 'Codex CLI was not found. Install Codex first.' }
$previous = [Environment]::GetEnvironmentVariable('ZTAPI_API_KEY', 'Process')
$key = $previous
$secure = $null
$result = 1
try {
    if ([string]::IsNullOrWhiteSpace($key)) {
        $secure = Read-Host 'ZTAPI API Key (hidden)' -AsSecureString
        $key = [System.Net.NetworkCredential]::new('', $secure).Password
    }
    if ($key -cnotmatch '^sk-[A-Za-z0-9_-]{8,512}$') { throw 'Invalid or empty ZTAPI key. Paste the key itself, not an environment-variable name.' }
    $env:ZTAPI_API_KEY = $key
    $overrides = @(
        '-c', 'model_provider="ztapi"',
        '-c', 'model="zt-claude-opus-5"',
        '-c', 'web_search="live"',
        '-c', 'model_providers.ztapi.name="ZTAPI"',
        '-c', 'model_providers.ztapi.base_url="https://ztapi.vip/v1"',
        '-c', 'model_providers.ztapi.wire_api="responses"',
        '-c', 'model_providers.ztapi.env_key="ZTAPI_API_KEY"',
        '-c', 'model_providers.ztapi.requires_openai_auth=false',
        '-c', 'model_providers.ztapi.model_catalog_url="https://ztapi.vip/codex/models"'
    )
    Write-Host 'Starting Codex with ZTAPI. Key hidden; existing configuration files are unchanged.'
    & $command.Source @args @overrides
    $result = $LASTEXITCODE
}
finally {
    [Environment]::SetEnvironmentVariable('ZTAPI_API_KEY', $previous, 'Process')
    if ($null -ne $secure) { $secure.Dispose() }
    $key = $null
}
exit $result
