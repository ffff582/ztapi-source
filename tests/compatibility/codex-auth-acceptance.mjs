import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {spawn} from 'node:child_process';
import {mkdtempSync,mkdirSync,writeFileSync,readFileSync,chmodSync} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const binary=process.env.ZTAPI_TEST_REAL_BIN;
assert.ok(binary&&path.isAbsolute(binary),'Set ZTAPI_TEST_REAL_BIN to an isolated test binary');
const root=mkdtempSync(path.join(os.tmpdir(),'ztapi-real-codex-auth-'));
for(const dir of ['home','codex','tmp','appdata','localappdata','bin'])mkdirSync(path.join(root,dir));
const scriptDir=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../../web/console/public/assets');
const key='sk-zt-SYNTHETIC_AUTH_ACCEPTANCE_20261104';
const requests=[];
const sockets=new Set();
const server=createServer(async(req,res)=>{
 if(req.method==='GET'){res.writeHead(200,{'Content-Type':'application/json'});res.end('{"models":[]}');return;}
 const parts=[];for await(const chunk of req)parts.push(chunk);
 const body=JSON.parse(Buffer.concat(parts));
 requests.push({path:req.url,auth_matches:req.headers.authorization==='Bearer '+key,model:body.model});
 assert.ok(req.url.startsWith('/v1/responses'));
 const item={id:'msg_local',type:'message',role:'assistant',status:'completed',content:[{type:'output_text',text:'OK',annotations:[]}]};
 const response={id:'resp_local_auth',object:'response',created_at:Math.floor(Date.now()/1000),status:'completed',model:body.model,output:[item],usage:{input_tokens:1,output_tokens:1,total_tokens:2,input_tokens_details:{cached_tokens:0}}};
 res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-cache'});
 for(const event of [
  {type:'response.created',response:{...response,status:'in_progress',output:[]}},
  {type:'response.output_item.added',output_index:0,item:{...item,status:'in_progress',content:[]}},
  {type:'response.content_part.added',item_id:item.id,output_index:0,content_index:0,part:{type:'output_text',text:'',annotations:[]}},
  {type:'response.output_text.delta',item_id:item.id,output_index:0,content_index:0,delta:'OK'},
  {type:'response.output_text.done',item_id:item.id,output_index:0,content_index:0,text:'OK'},
  {type:'response.output_item.done',output_index:0,item},
  {type:'response.completed',response},
 ])res.write('event: '+event.type+'\ndata: '+JSON.stringify(event)+'\n\n');
 res.end();
});
server.on('connection',socket=>{sockets.add(socket);socket.on('close',()=>sockets.delete(socket));});
server.on('upgrade',(req,socket)=>{socket.end('HTTP/1.1 426 Upgrade Required\r\nConnection: close\r\nContent-Length: 0\r\n\r\n');});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const url='http://127.0.0.1:'+server.address().port;
const env={};
for(const [name,value] of Object.entries(process.env))if(['PATH','SYSTEMROOT','WINDIR','COMSPEC','PATHEXT','SYSTEMDRIVE'].includes(name.toUpperCase()))env[name.toUpperCase()]=value;
Object.assign(env,{HOME:path.join(root,'home'),USERPROFILE:path.join(root,'home'),CODEX_HOME:path.join(root,'codex'),TMP:path.join(root,'tmp'),TEMP:path.join(root,'tmp'),APPDATA:path.join(root,'appdata'),LOCALAPPDATA:path.join(root,'localappdata'),NO_PROXY:'127.0.0.1,localhost',no_proxy:'127.0.0.1,localhost'});
const promptArgs=['exec','--skip-git-repo-check','--sandbox','read-only','--model','zt-gpt-6.1-sol','Reply with OK'];
function run(executable,args,variables){return new Promise((resolve,reject)=>{
 const child=spawn(executable,args,{env:variables,cwd:path.join(root,'home'),windowsHide:true,stdio:['ignore','pipe','pipe']});let output='';
 const timer=setTimeout(()=>{child.kill();reject(new Error('owned_test_process_timeout'));},45000);
 child.stdout.on('data',b=>{output+=b.toString();});child.stderr.on('data',b=>{output+=b.toString();});
 child.on('error',e=>{clearTimeout(timer);reject(e);});
 child.on('exit',()=>{setTimeout(()=>{child.stdout.destroy();child.stderr.destroy();},1000).unref();});
 child.on('close',code=>{clearTimeout(timer);resolve({code,output});});
});}
const configPath=path.join(env.CODEX_HOME,'config.toml');
function config(variable){return 'model_provider="ztapi"\nmodel="zt-claude-opus-5"\n[model_providers.ztapi]\nname="ZTAPI"\nwire_api="responses"\nrequires_openai_auth=false\nbase_url='+JSON.stringify(url+'/v1')+'\nenv_key='+JSON.stringify(variable)+'\n';}
try{
 writeFileSync(configPath,config('ZTAPI_API_KEY'));
 const missing=await run(binary,promptArgs,env);
 assert.notEqual(missing.code,0);assert.ok(missing.output.includes('Missing environment variable'));assert.equal(requests.length,0);
 writeFileSync(configPath,config(key));
 const wrong=await run(binary,promptArgs,{...env,ZTAPI_API_KEY:key});
 assert.notEqual(wrong.code,0);assert.ok(wrong.output.includes('Missing environment variable'));assert.equal(requests.length,0);
 const before=readFileSync(configPath,'utf8');
 writeFileSync(path.join(env.CODEX_HOME,'auth.json'),'{"OPENAI_API_KEY":"not-a-real-login-canary"}');
 const shim=path.join(root,'bin');
 const transport=path.join(root,'transport.mjs');
 writeFileSync(transport,`import {spawn} from 'node:child_process';const args=process.argv.slice(2).map(a=>a.replaceAll('https://ztapi.vip/v1',process.env.ZTAPI_TEST_MOCK+'/v1').replaceAll('https://ztapi.vip/codex/models',process.env.ZTAPI_TEST_MOCK+'/codex/models'));const c=spawn(process.env.ZTAPI_TEST_REAL_BIN,args,{env:process.env,stdio:'inherit'});c.on('exit',n=>process.exit(n??1));`);
 const variables={...env,ZTAPI_API_KEY:key,ZTAPI_TEST_REAL_BIN:binary,ZTAPI_TEST_MOCK:url,PATH:shim+path.delimiter+env.PATH};
 let success;
 if(process.platform==='win32'){
  writeFileSync(path.join(shim,'codex.ps1'),`param()\n& '${process.execPath.replaceAll("'","''")}' '${transport.replaceAll("'","''")}' @args\nexit $LASTEXITCODE\n`);
  success=await run('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File',path.join(scriptDir,'ztapi-codex-20261104.ps1'),...promptArgs],variables);
 }else{
  const fake=path.join(shim,'codex');writeFileSync(fake,'#!/bin/sh\nexec '+JSON.stringify(process.execPath)+' '+JSON.stringify(transport)+' "$@"\n');chmodSync(fake,0o700);
  success=await run('/bin/bash',[path.join(scriptDir,'ztapi-codex-20261104.sh'),...promptArgs],variables);
 }
 assert.equal(success.code,0,success.output.replaceAll(key,'[synthetic key]'));
 assert.ok(success.output.includes('OK'));assert.ok(!success.output.includes(key));
 assert.ok(requests.length>=1&&requests.every(r=>r.auth_matches));
 assert.ok(requests.every(r=>r.model==='zt-gpt-6.1-sol'),'launcher changed the explicitly selected model');
 assert.equal(readFileSync(configPath,'utf8'),before);
 assert.equal(readFileSync(path.join(env.CODEX_HOME,'auth.json'),'utf8'),'{"OPENAI_API_KEY":"not-a-real-login-canary"}');
 console.log(JSON.stringify({passed:true,platform:process.platform,missing_env_reproduced:true,key_as_variable_name_reproduced:true,failed_attempts_sent_no_model_requests:true,real_cli_mock_requests:requests.length,authorization_matches:true,explicit_model_preserved:true,output_ok:true,existing_config_and_auth_unchanged:true,secret_not_in_output:true,loopback_only:true,paid_model_calls:0}));
}finally{for(const socket of sockets)socket.destroy();await new Promise(resolve=>server.close(resolve));}
