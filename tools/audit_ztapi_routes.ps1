param([Parameter(Mandatory=$true)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$secretBase = 'C:\Users\Administrator\.ztapi-deploy-secrets'
$username = (Get-Content -LiteralPath (Join-Path $secretBase 'ZTAPI_ADMIN_USERNAME.txt') -Raw).Trim()
$password = (Get-Content -LiteralPath (Join-Path $secretBase 'ZTAPI_ADMIN_PASSWORD.txt') -Raw).Trim()
$origin = 'https://admin.ztapi.vip'
$login = Invoke-RestMethod -Uri "$origin/api/auth/login" -Method Post -ContentType 'application/json' -Body (@{username=$username;password=$password}|ConvertTo-Json -Compress) -TimeoutSec 20
$headers = @{Authorization='Bearer '+$login.data.access_token;'New-Api-User'=[string]$login.data.user.id}
$models = @()
$page = 1
do {
    $result = Invoke-RestMethod -Uri "$origin/api/models/ztapi/?page=$page&page_size=100" -Headers $headers -TimeoutSec 30
    if (!$result.success) { throw 'catalog lookup failed' }
    $models += @($result.data.items)
    $page++
} while ($models.Count -lt $result.data.total)
$channels = @()
foreach ($id in @(1,2,3)) {
    try {
        $result = Invoke-RestMethod -Uri "$origin/api/channel/ztapi/$id" -Headers $headers -TimeoutSec 20
        if (!$result.success) { continue }
        $c = $result.data
        $discovery = Invoke-RestMethod -Uri "$origin/api/channel/ztapi/fetch_models/$id" -Headers $headers -TimeoutSec 45
        $modelIds = if ($discovery.data -is [array]) { @($discovery.data) } else { @($discovery.data.model_ids) }
        $channels += [ordered]@{id=$c.id;name=$c.name;status=$c.status;type=$c.type;models=$c.models;model_mapping=$c.model_mapping;priority=$c.priority;group=$c.group;discovery_success=$discovery.success;discovered_ids=$modelIds}
    } catch { Write-Warning "Channel $id metadata/discovery unavailable" }
}
$data = [ordered]@{captured_at=(Get-Date).ToUniversalTime().ToString('o');models=$models;channels=$channels}
[IO.File]::WriteAllText((Join-Path $OutputDirectory 'catalog-safe.json'), ($data|ConvertTo-Json -Depth 20), [Text.UTF8Encoding]::new($false))
$sql = @'
SELECT JSON_OBJECT('id',c.id,'source_model',c.source_model,'public_name',c.public_name,'published',c.published,'version',c.version,'snapshot_id',c.publication_snapshot_id,'allowed_channel_ids',s.allowed_channel_ids,'snapshot_model_version',s.model_version,'price_source_id',s.price_source_id,'price_policy',s.price_policy,'resource_type',p.resource_type,'quotation_grade',p.quotation_grade,'quotation_model_code',p.quotation_model_code,'quotation_cell',p.quotation_cell) FROM ztapi_model_configs c LEFT JOIN ztapi_model_publication_snapshots s ON s.id=c.publication_snapshot_id LEFT JOIN ztapi_model_price_sources p ON p.id=s.price_source_id ORDER BY c.id;
SELECT JSON_OBJECT('table','recent_health','model_id',model_id,'channel_id',channel_id,'source',source,'result',result,'http_status',http_status,'reason',reason,'latest_at',MAX(completed_at),'requests',COUNT(*)) FROM ztapi_health_events WHERE completed_at > UNIX_TIMESTAMP()-86400*30 GROUP BY model_id,channel_id,source,result,http_status,reason;
SELECT JSON_OBJECT('table','channel','id',id,'name',name,'status',status,'type',type,'models',models,'model_mapping',model_mapping,'group',`group`) FROM channels ORDER BY id;
SELECT JSON_OBJECT('table','verification','model_id',model_config_id,'channel_id',channel_id,'non_streaming_passed',non_streaming_passed,'streaming_required',streaming_required,'streaming_passed',streaming_passed,'status_category',status_category,'verified_at',verified_at) FROM ztapi_model_verifications WHERE verified_at > UNIX_TIMESTAMP()-86400*30 ORDER BY verified_at;
'@
$sql64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($sql))
$remote = 'printf %s '+$sql64+' | base64 -d | sudo -n docker exec -i ztapi-mysql-1 sh -c ''exec mysql --user="$MYSQL_USER" --password="$MYSQL_PASSWORD" "$MYSQL_DATABASE" --batch --skip-column-names --raw'''
$remote64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($remote))
$rows = & ssh -i (Join-Path $secretBase 'ztapi_deploy_key') -o BatchMode=yes -o StrictHostKeyChecking=yes -o ('UserKnownHostsFile='+ (Join-Path $secretBase 'known_hosts')) -o ConnectTimeout=8 ztapi-deploy@123.254.104.157 "printf %s $remote64 | base64 -d | bash"
if ($LASTEXITCODE -ne 0) { throw 'read-only SQL capture failed' }
$parsed = @($rows | Where-Object {$_ -match '^\{'} | ForEach-Object {$_ | ConvertFrom-Json})
[IO.File]::WriteAllText((Join-Path $OutputDirectory 'routes-safe.json'), ($parsed|ConvertTo-Json -Depth 20), [Text.UTF8Encoding]::new($false))
Write-Output "Captured $($models.Count) configurations; $($channels.Count) channel discovery results; $($parsed.Count) SQL projections. No inference requests were sent."
