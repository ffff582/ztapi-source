import assert from 'node:assert/strict';
import {readFileSync,writeFileSync,mkdirSync,mkdtempSync,existsSync} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';
import test from 'node:test';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../../web/console/public/assets');
const ps=path.join(root,'ztapi-codex-20261104.ps1');
const bash=readFileSync(path.join(root,'ztapi-codex-20261104.sh'),'utf8');
const powershell=readFileSync(ps,'utf8');
const fakeKey='sk-zt-SYNTHETIC_ONLY_20261104';

test('launchers pin the provider without credential files, weaker permissions or automatic prompts',()=>{
 for(const source of [bash,powershell]){
  for(const required of ['https://ztapi.vip/v1','env_key="ZTAPI_API_KEY"','requires_openai_auth=false','wire_api="responses"'])assert.ok(source.includes(required));
  assert.doesNotMatch(source,/danger-full-access|dangerously-bypass|approval_policy|Set-ExecutionPolicy|setx\s|WriteAllText|Out-File|curl\s|Invoke-WebRequest/);
  assert.doesNotMatch(source,/echo\s+.*ZTAPI_API_KEY|Write-(?:Host|Output).*\$key/);
 }
 assert.ok(bash.includes('read -r -s'));
 assert.ok(powershell.includes('-AsSecureString'));
 assert.ok(powershell.includes("SetEnvironmentVariable('ZTAPI_API_KEY', $previous, 'Process')"));
});

test('PowerShell launcher forwards arguments, supplies key privately and preserves existing files',{skip:process.platform!=='win32'},()=>{
 const owned=mkdtempSync(path.join(os.tmpdir(),'ztapi-codex-launcher-'));
 const bin=path.join(owned,'bin');mkdirSync(bin);
 const config=path.join(owned,'config.toml'),auth=path.join(owned,'auth.json'),receipt=path.join(owned,'receipt.json');
 writeFileSync(config,'model_provider="ztapi"\n[model_providers.ztapi]\nenv_key="sk-not-a-variable-name"\n');
 writeFileSync(auth,'{"OPENAI_API_KEY":"not-a-real-login"}');
 writeFileSync(path.join(bin,'codex.ps1'),`param()\n[IO.File]::WriteAllText($env:ZTAPI_TEST_RECEIPT, (@{arguments=@($args);key_matches=($env:ZTAPI_API_KEY -eq $env:ZTAPI_TEST_EXPECTED)} | ConvertTo-Json -Depth 4))\nexit 0\n`);
 const env={...process.env,PATH:bin+path.delimiter+process.env.PATH,HOME:owned,USERPROFILE:owned,CODEX_HOME:owned,ZTAPI_API_KEY:fakeKey,ZTAPI_TEST_EXPECTED:fakeKey,ZTAPI_TEST_RECEIPT:receipt};
 const run=spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File',ps,'--model','zt-gpt-6.1-sol'],{env,encoding:'utf8',windowsHide:true,timeout:15000});
 assert.equal(run.status,0,run.stderr);
 assert.ok(existsSync(receipt));
 const result=JSON.parse(readFileSync(receipt,'utf8'));
 assert.equal(result.key_matches,true);
 assert.ok(result.arguments.includes('zt-gpt-6.1-sol'));
 assert.ok(result.arguments.includes('model_providers.ztapi.env_key="ZTAPI_API_KEY"'));
 assert.ok(!result.arguments.some(v=>v.includes(fakeKey)));
 assert.ok(!(run.stdout+run.stderr).includes(fakeKey));
 assert.ok(readFileSync(config,'utf8').includes('sk-not-a-variable-name'));
 assert.equal(readFileSync(auth,'utf8'),'{"OPENAI_API_KEY":"not-a-real-login"}');
});

test('PowerShell launcher rejects variable names used as credential values without spawning Codex',{skip:process.platform!=='win32'},()=>{
 const run=spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File',ps],{env:{...process.env,ZTAPI_API_KEY:'ZTAPI_API_KEY'},encoding:'utf8',windowsHide:true,timeout:15000});
 assert.notEqual(run.status,0);
 assert.ok(!run.stdout.includes('Starting Codex'));
});
