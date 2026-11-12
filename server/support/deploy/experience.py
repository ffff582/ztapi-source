#!/usr/bin/env python3
"""Support-only upgrade: shadow acceptance, immutable receipts, reversible rollout."""
import base64
from contextlib import closing
import copy
import gzip
import hashlib
import hmac
import io
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import sqlite3
import subprocess
import sys
import time
from urllib import request

import release as r

STAGE=Path(__file__).resolve().parent
r.STAGE=STAGE
OLD_SITE='ztapi-support-20261102'
OLD_BOT='ztapi-telegram-support-20261102'
FINANCE='ztapi-finance-support-20261102-live'
NEW_SITE='ztapi-support-experience-20261102'
NEW_BOT='ztapi-telegram-support-experience-20261102'
CANDIDATE_SITE=NEW_SITE+'-candidate'
CANDIDATE_BOT=NEW_BOT+'-candidate'
ROOT=Path('/opt/ztapi-support/experience-20261102')
SITE_DATA=Path('/opt/ztapi-support/data')
BOT_DATA=Path('/opt/ztapi-telegram-support/data')


def nginx_candidate(original,commit):
    r.require(re.fullmatch('[a-f0-9]{40}',commit) is not None,'invalid_source_commit')
    old='http://'+OLD_SITE+':3012'
    r.require(original.count(old)==3 and original.count('server_name ztapi.vip www.ztapi.vip;')==1 and original.count('server_name admin.ztapi.vip;')==1,'unknown_support_route_baseline')
    result=original.replace(old,'http://'+NEW_SITE+':3012')
    result=re.sub(r'(add_header X-ZTAPI-UI-Source "https://github.com/ffff582/ztapi-source/tree/)[a-f0-9]{40}',lambda m:m.group(1)+commit,result)
    for server in ['server_name ztapi.vip www.ztapi.vip;','server_name admin.ztapi.vip;']:
        result=result.replace(server,server+'\n    location ^~ /internal/ { return 404; }')
    return result


def common_args(name,network='ztapi_app'):
    return ['create','--name',name,'--label','ztapi.support.experience=20261102','--network',network,'--restart','unless-stopped','--read-only','--cap-drop=ALL','--security-opt','no-new-privileges','--memory','256m','--cpus','0.5','--pids-limit','64','--tmpfs','/tmp:rw,noexec,nosuid,size=32m']


def remember_created(name,identifier):
    current=r.inspect(name)
    r.require(current['Id']==identifier.strip().decode() and current['Config']['Labels'].get('ztapi.support.experience')=='20261102','created_container_identity_mismatch')
    r.json_save('created-'+name+'.json',{'id':current['Id'],'image':current['Image']})


def stop_owned_container(name):
    proof=STAGE/('created-'+name+'.json')
    if not proof.exists():return False
    expected=json.loads(proof.read_text());current=r.inspect(name)
    r.require(current['Id']==expected['id'] and current['Image']==expected['image'] and current['Config']['Labels'].get('ztapi.support.experience')=='20261102','rollback_container_not_owned')
    r.docker('update','--restart=no',name);r.docker('stop','--time','75',name)
    return True


def bot_args(image,data,config,enabled,name=NEW_BOT):
    return common_args(name)+['--env','TELEGRAM_SUPPORT_ENABLED='+str(enabled).lower(),'--env','TELEGRAM_SUPPORT_STORE=/data/support.db','--env','TELEGRAM_SUPPORT_CONFIG=/run/secrets/config.json','--env','TELEGRAM_SUPPORT_CATALOG_URL=http://ztapi-server-1:3000/api/pricing','--env','TELEGRAM_SUPPORT_LISTEN=:3013','--env','GOMEMLIMIT=160MiB','--env','GOMAXPROCS=2','--mount','type=bind,src='+str(data)+',dst=/data','--mount','type=bind,src='+str(config)+',dst=/run/secrets/config.json,readonly',image]


def run_import(image,manifest,sha256,owner_id):
    r.require(not r.inspect(OLD_BOT)['State']['Running'],'receipt_import_requires_stopped_receiver')
    raw=r.docker('run','--rm','--network','none','--read-only','--cap-drop=ALL','--security-opt','no-new-privileges','--memory','256m','--pids-limit','64','--tmpfs','/tmp:rw,noexec,nosuid,size=16m','--env','SUPPORT_IMPORT_STORE=/data/support.db','--env','SUPPORT_IMPORT_MANIFEST=/run/import/manifest.json','--env','SUPPORT_IMPORT_SHA256='+sha256,'--env','SUPPORT_IMPORT_OPERATOR_ID='+str(owner_id),'--mount','type=bind,src='+str(BOT_DATA)+',dst=/data','--mount','type=bind,src='+str(manifest)+',dst=/run/import/manifest.json,readonly',image,timeout=60)
    return json.loads(raw)


def resume_old_bot():
    r.docker('start',OLD_BOT)


def load_payload():
    build,baseline=r.validate_payload()
    r.require(json.loads((STAGE/'source-published.json').read_text())['commit']==build['commit'],'source_release_mismatch')
    r.require(json.loads((STAGE/'browser-qa.json').read_text())['passed'],'browser_qa_not_passed')
    return build,baseline


def check_core(baseline,config=True,sidecars=False):
    names=['ztapi-server-1','ztapi-nginx-1','ztapi-finance',FINANCE]
    if sidecars:names += [OLD_SITE,OLD_BOT]
    for name in names:
        current=r.inspect(name);old=baseline['containers'][name]
        r.require(current['Id']==old['id'] and current['Image']==old['image'] and current['State']['StartedAt']==old['started_at'],'runtime_baseline_changed:'+name)
        if name in ['ztapi-server-1','ztapi-nginx-1',FINANCE]:r.require(current['State']['Running'],'core_service_stopped:'+name)
    if config:
        r.require(r.digest(r.docker('exec',r.NGINX,'cat',r.CONF))==baseline['nginx_sha256'],'nginx_baseline_changed')
        r.require(r.digest(r.docker('exec',r.NGINX,'cat','/usr/share/nginx/public/index.html'))==baseline['public_index_sha256'],'public_ui_baseline_changed')


def check_known_support(baseline):
    for name in [OLD_SITE,OLD_BOT]:
        current=r.inspect(name);old=baseline['containers'][name]
        r.require(current['Id']==old['id'] and current['Image']==old['image'] and current['State']['Running'],'support_baseline_changed:'+name)
        expected=old['started_at']
        if name==OLD_BOT and (STAGE/'cost-resume.json').exists():
            expected=json.loads((STAGE/'cost-resume.json').read_text())['StartedAt']
        r.require(current['State']['StartedAt']==expected,'support_restart_not_owned:'+name)


def snapshot_db(source,target):
    r.require(source.is_file() and not target.exists(),'invalid_private_snapshot')
    with closing(sqlite3.connect('file:'+str(source)+'?mode=ro',uri=True)) as src:
        with closing(sqlite3.connect(target)) as dst:
            src.backup(dst);r.require(dst.execute('PRAGMA integrity_check').fetchall()==[('ok',)],'private_snapshot_corrupt')
    os.chown(target,10001,10001);os.chmod(target,0o600)


def image_tag(kind,commit):return 'ztapi-'+kind+':experience-'+commit[:12]


def build_image(kind,commit):
    directory=STAGE/('image-'+kind);directory.mkdir(mode=0o700)
    binary='telegram-live.test' if kind=='support-experience-live' else 'ztapi-'+kind
    shutil.copyfile(STAGE/binary,directory/binary);os.chmod(directory/binary,0o755)
    cert='COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt\n' if kind!='support-receipt-import' else ''
    if cert:r.copy_public_ca('/etc/ssl/certs/ca-certificates.crt',directory/'ca-certificates.crt')
    (directory/'Dockerfile').write_text('FROM scratch\n'+cert+'COPY '+binary+' /'+binary+'\nUSER 10001:10001\nLABEL org.opencontainers.image.revision="'+commit+'" org.opencontainers.image.source="https://github.com/ffff582/ztapi-source" org.opencontainers.image.licenses="AGPL-3.0-or-later"\nENTRYPOINT ["/'+binary+'"]\n')
    r.docker('build','-q','-t',image_tag(kind,commit),str(directory),timeout=180)


def start_site(build,data,candidate):
    name=CANDIDATE_SITE if candidate else NEW_SITE
    env=ROOT/('site-candidate.env' if candidate else 'site-production.env')
    args=common_args(name)+['--env-file',str(env),'--mount','type=bind,src='+str(data)+',dst=/data','--mount','type=bind,src=/opt/ztapi-support/secrets/provider.json,dst=/run/secrets/provider.json,readonly','--mount','type=bind,src='+str(ROOT/'secrets/bridge.json')+',dst=/run/secrets/bridge.json,readonly',image_tag('support',build['commit'])]
    remember_created(name,r.docker(*args))
    for network in ['ztapi_data','ztapi_egress']:r.docker('network','connect',network,name)
    r.docker('start',name);r.wait_health(name,3012)


def start_bot(build,data,candidate):
    name=CANDIDATE_BOT if candidate else NEW_BOT
    config=ROOT/'secrets'/('bot-candidate.json' if candidate else 'bot-production.json')
    remember_created(name,r.docker(*bot_args(image_tag('telegram-support',build['commit']),data,config,not candidate,name)))
    r.docker('network','connect','ztapi_egress',name);r.docker('start',name);r.wait_health(name,3013)


def protected_config(path,value):
    r.save(path,json.dumps(value).encode(),0o400);os.chown(path,10001,10001)


def prepare():
    build,baseline=load_payload();check_core(baseline,sidecars=True)
    r.require(not ROOT.exists(),'experience_stage_already_exists')
    ROOT.mkdir(mode=0o700);(ROOT/'secrets').mkdir(mode=0o711)
    secret=secrets.token_hex(32)
    protected_config(ROOT/'secrets/bridge.json',{'secret':secret,'bot_username':'ztapi_bot'})
    old=r.envmap(r.inspect(OLD_SITE));old.update({'SUPPORT_TELEGRAM_BRIDGE_CONFIG':'/run/secrets/bridge.json','SUPPORT_SOURCE_COMMIT':build['commit']})
    r.write_env(ROOT/'site-candidate.env',{**old,'SUPPORT_ENVIRONMENT':'acceptance','SUPPORT_ENABLED':'false'})
    r.write_env(ROOT/'site-production.env',{**old,'SUPPORT_ENVIRONMENT':'production','SUPPORT_ENABLED':'true'})
    original=json.loads(Path('/opt/ztapi-telegram-support/secrets/config.json').read_text())
    r.require(original['owner_id']==841126452 and original['bot_id']==8722257604 and original['provider']['channel_id']==8 and original['provider']['supplier']=='xcloud','bot_provider_or_private_owner_changed')
    for candidate,name in [(True,CANDIDATE_SITE),(False,NEW_SITE)]:
        cfg=copy.deepcopy(original);cfg['bridge']={'endpoint':'http://'+name+':3012/internal/telegram-support','secret':secret}
        protected_config(ROOT/'secrets'/('bot-candidate.json' if candidate else 'bot-production.json'),cfg)
    for kind in ['support','telegram-support','support-receipt-import','support-experience-live']:build_image(kind,build['commit'])
    for name,source in [('candidate-site',SITE_DATA),('candidate-bot',BOT_DATA)]:
        directory=STAGE/name;directory.mkdir(mode=0o700);os.chown(directory,10001,10001);snapshot_db(source/'support.db',directory/'support.db')
    start_site(build,STAGE/'candidate-site',True);start_bot(build,STAGE/'candidate-bot',True)
    check_core(baseline,sidecars=True)
    r.json_save('prepared.json',{'prepared':True,'source':build['commit'],'main_api_unchanged':True,'finance_not_restarted':True,'telegram_receiver_not_started':True})


def bridge_call(action):
    secret=json.loads((ROOT/'secrets/bridge.json').read_text())['secret']
    raw=json.dumps(action,separators=(',',':')).encode();stamp=str(int(time.time()));nonce=secrets.token_hex(16)
    path='/internal/telegram-support';mac=hmac.new(bytes.fromhex(secret),(stamp+'\n'+nonce+'\nPOST\n'+path+'\n').encode()+raw,hashlib.sha256).hexdigest()
    req=request.Request(r.address(CANDIDATE_SITE,3012)+path,data=raw,headers={'Content-Type':'application/json','X-ZTAPI-Telegram-Timestamp':stamp,'X-ZTAPI-Telegram-Nonce':nonce,'X-ZTAPI-Telegram-Signature':mac})
    with request.build_opener(request.ProxyHandler({})).open(req,timeout=15) as response:
        value=json.loads(response.read(65536));r.require(response.status==200 and value['success'],'bridge_candidate_failed');return value['data']


def web_checks():
    build,baseline=load_payload();check_core(baseline)
    key=r.envmap(r.inspect('ztapi-server-1'))['ZTAPI_SESSION_SIGNING_KEY'];base=r.address(CANDIDATE_SITE,3012)
    def call(path,data=None):
        status,value=r.fetch(base+path,key,data=data);r.require(status in [200,201] and value['success'],'candidate_web_check_failed:'+str(status));return value['data']
    original=call('/api/support/telegram-binding');r.require(not original['bound'],'unexpected_shadow_root_binding')
    created=call('/api/support/telegram-binding',{'action':'create','idempotency_key':'experience-root-bind-create'})
    token=created['binding_url'].split('bind_',1)[1];pending=created['pending']['id'];tg=50999999001
    r.require(not bridge_call({'action':'binding_status','telegram_id':tg})['bound'],'shadow_identity_already_bound')
    claim=bridge_call({'action':'claim_binding','telegram_id':tg,'token':token,'username':'synthetic_acceptance'})
    r.require(claim['pending'] and not claim['bound'],'claim_was_auto_confirmed')
    confirmed=call('/api/support/telegram-binding',{'action':'confirm','idempotency_key':'experience-root-bind-confirm','challenge_id':pending});r.require(confirmed['bound'] and confirmed['telegram_id']==tg,'explicit_confirmation_failed')
    recent=bridge_call({'action':'recent_requests','telegram_id':tg});r.require(recent['bound'],'owned_diagnostics_unavailable')
    ticket=bridge_call({'action':'create_ticket','telegram_id':tg,'content':'仅验收工单链路，不修改任何账户资金或渠道。','idempotency_key':'experience-shadow-handoff'})['ticket']
    r.fetch(base+'/api/admin/support/tickets/'+str(ticket['id'])+'/replies',key,data={'content':'人工测试回复，未调整账户。','idempotency_key':'experience-shadow-reply'})
    events=bridge_call({'action':'poll_notifications'})['notifications'];r.require(any(e['ticket_id']==ticket['id'] for e in events),'ticket_update_not_projected')
    event=next(e for e in events if e['ticket_id']==ticket['id'])
    r.require(bridge_call({'action':'authorize_notification','telegram_id':tg,'event_id':event['id']})['allowed'],'notification_authorization_failed')
    call('/api/support/telegram-binding',{'action':'unbind','idempotency_key':'experience-root-unbind'})
    r.require(not bridge_call({'action':'authorize_notification','telegram_id':tg,'event_id':event['id']}).get('allowed',False),'unbind_did_not_revoke')
    check_core(baseline);r.json_save('web-acceptance.json',{'passed':True,'claim_not_binding':True,'explicit_confirm':True,'owned_diagnostics':True,'ticket_notification':True,'unbind_revokes':True,'paid_calls':0,'main_api_unchanged':True})


def valid_live_result(result,exit_code):
    expected={'normal_meme','configuration_help','serious_billing','serious_fault','vision_503'}
    cases=result.get('cases',[]);usages=result.get('usages',[])
    if exit_code!=0 or result.get('collection_error') or len(cases)!=5 or len(usages)!=5:return False
    if {c.get('name') for c in cases}!=expected or len({c.get('actor_id') for c in cases})!=5:return False
    required={'one_attempt','reply_present','no_false_operation','no_credential_echo','one_question','serious_tone'}
    extra={'configuration_help':'correct_base_url','serious_billing':'evidence_not_guess','serious_fault':'fault_code_503','vision_503':'visual_code_503'}
    for c in cases:
        checks=c.get('checks',{});keys=required|({extra[c['name']]} if c['name'] in extra else set())
        if c.get('attempts')!=1 or c.get('outcome')!='completed' or not c.get('reply') or any(checks.get(k) is not True for k in keys):return False
    ids={c.get('call_id') for c in cases}
    return None not in ids and '' not in ids and len(ids)==5 and {u.get('call_id') for u in usages}==ids


def live_tests():
    build,baseline=load_payload();check_core(baseline)
    proof=json.loads((STAGE/'budget-proof.json').read_text());r.require(proof['reserved_u']=='0.5' and proof['total_cap_u']=='15' and len(proof['attempt_ids'])==5,'test_budget_not_reserved')
    r.json_save('live-started.json',{'budget_reserved_u':'0.5','max_attempts':5,'automatic_retries':False,'started_at':time.time()})
    output=STAGE/'live-output';output.mkdir(mode=0o700);os.chown(output,10001,10001)
    r.docker('create','--name','ztapi-support-experience-live-20261102','--network','ztapi_app','--read-only','--cap-drop=ALL','--security-opt','no-new-privileges','--memory','256m','--pids-limit','64','--tmpfs','/tmp:rw,noexec,nosuid,size=32m','--env','GOMAXPROCS=2','--env','SUPPORT_EXPERIENCE_LIVE_CONFIG=/run/secrets/provider.json','--env','SUPPORT_EXPERIENCE_LIVE_OUTPUT=/result/acceptance.json','--env','SUPPORT_EXPERIENCE_LIVE_IMAGE=/fixture/vision.png','--mount','type=bind,src=/opt/ztapi-support/secrets/provider.json,dst=/run/secrets/provider.json,readonly','--mount','type=bind,src='+str(output)+',dst=/result','--mount','type=bind,src='+str(STAGE/'vision-fixture.png')+',dst=/fixture/vision.png,readonly',image_tag('support-experience-live',build['commit']),'-test.run=TestLiveSupportExperienceAcceptance','-test.v','-test.timeout=300s')
    r.docker('network','connect','ztapi_egress','ztapi-support-experience-live-20261102')
    process=subprocess.run(['docker','start','-a','ztapi-support-experience-live-20261102'],capture_output=True,timeout=330)
    r.save(STAGE/'live-test.log',process.stdout+process.stderr)
    container=r.inspect('ztapi-support-experience-live-20261102');r.require(not container['State']['Running'],'live_acceptance_still_running')
    result=json.loads((output/'acceptance.json').read_text()) if (output/'acceptance.json').exists() else {'passed':False,'usages':[],'report_missing':True,'usage_state':'unknown'}
    result['container_exit_code']=container['State']['ExitCode'];result['source_commit']=build['commit'];result['passed']=valid_live_result(result,container['State']['ExitCode'])
    r.json_save('live-result.json',result)
    r.require(container['State']['ExitCode']==0 and result.get('passed') is True,'real_inference_acceptance_failed')
    check_core(baseline)


def record_costs():
    build,baseline=load_payload();check_core(baseline);check_known_support(baseline)
    result=json.loads((STAGE/'live-result.json').read_text());usages=result['usages']
    r.require(len(usages)<=5,'unexpected_acceptance_call_count')
    r.require(usages and not result.get('report_missing'),'usage_evidence_missing_keep_budget_reserved')
    manifest=STAGE/'receipt-import.json';protected_config(manifest,{'release':build['commit'],'usages':usages});raw=manifest.read_bytes()
    snapshot_db(BOT_DATA/'support.db',STAGE/'bot-before-receipt-import.db')
    r.docker('stop','--time','75',OLD_BOT)
    try:
        imported=run_import(image_tag('support-receipt-import',build['commit']),manifest,r.digest(raw),841126452)
        r.json_save('costs-recorded.json',{'recorded':True,'calls':len(usages),'import_result':imported,'manifest_sha256':r.digest(raw),'basis':'immutable_customer_service_receipts_not_customer_settlements'})
    finally:resume_old_bot()
    r.wait_health(OLD_BOT,3013)
    r.json_save('cost-resume.json',r.identity(r.inspect(OLD_BOT)))
    check_core(baseline)


def stage_public(build):
    candidate=r.static_candidate('public')
    root='/usr/share/nginx/public';old=r.docker('exec',r.NGINX,'cat',root+'/index.html');r.save(STAGE/'public-before-index.html',old)
    previous=r.docker('cp',r.NGINX+':'+root+'/.','-');packed=gzip.compress(previous);r.save(STAGE/'public-before.tar.gz',packed)
    for file in candidate.rglob('*'):
        if not file.is_file() or file.name=='index.html':continue
        relative=file.relative_to(candidate).as_posix();target=root+'/'+relative
        r.require(r.digest(file.read_bytes())==build['bundles']['public'][relative],'public_bundle_integrity_failed')
        r.docker('exec',r.NGINX,'mkdir','-p',str(Path(target).parent))
        probe=subprocess.run(['docker','exec',r.NGINX,'cat',target],capture_output=True)
        if probe.returncode==0:r.require(r.digest(probe.stdout)==r.digest(file.read_bytes()),'stable_asset_collision:'+relative)
        else:r.docker('cp',str(file),r.NGINX+':'+target);r.docker('exec',r.NGINX,'chmod','644',target)
        r.require(r.docker('exec',r.NGINX,'sha256sum',target).decode().split()[0]==r.digest(file.read_bytes()),'static_copy_failed')
    return candidate,old


def rollback(original,old_index):
    bot_owned=stop_owned_container(NEW_BOT)
    stop_owned_container(NEW_SITE)
    # Old readers do not understand the new authorization callback. Quarantine
    # pending private replies rather than deliver them after a revoked binding.
    if bot_owned:
        with closing(sqlite3.connect(BOT_DATA/'support.db')) as db:
            db.execute("UPDATE telegram_support_outbox SET state='upgrade_suspended' WHERE chat_id>0 AND kind<>'alert' AND state IN ('pending','held')");db.commit()
    r.docker('start',OLD_SITE);r.docker('start',OLD_BOT)
    r.replace_inside(r.CONF,original);r.docker('exec',r.NGINX,'nginx','-t');r.docker('exec',r.NGINX,'nginx','-s','reload')
    if old_index is not None:r.replace_inside('/usr/share/nginx/public/index.html',old_index)
    r.json_save('rolled-back.json',{'website_restored':True,'old_support_resumed':True,'main_api_not_restarted':True,'finance_not_restarted':True,'all_cost_receipts_retained':True,'private_new_workflow_replies_quarantined':True})


def activate():
    build,baseline=load_payload();check_core(baseline);check_known_support(baseline)
    r.require(json.loads((STAGE/'web-acceptance.json').read_text())['passed'],'web_acceptance_missing')
    r.require(json.loads((STAGE/'live-result.json').read_text())['passed'],'live_acceptance_missing')
    r.require(json.loads((STAGE/'costs-recorded.json').read_text())['recorded'],'acceptance_costs_not_recorded')
    original=r.docker('exec',r.NGINX,'cat',r.CONF);r.save(STAGE/'nginx-before.conf',original);old_index=None
    try:
        r.run(['python3','-B','/opt/ztapi-support/backup.py']);r.run(['python3','-B','/opt/ztapi-support/backup.py','--telegram'])
        candidate,old_index=stage_public(build)
        snapshot_db(SITE_DATA/'support.db',STAGE/'site-before-activation.db');snapshot_db(BOT_DATA/'support.db',STAGE/'bot-before-activation.db')
        r.docker('stop','--time','75',OLD_BOT);r.docker('stop','--time','65',OLD_SITE)
        start_site(build,SITE_DATA,False)
        conf=nginx_candidate(original.decode(),build['commit']).encode();r.replace_inside(r.CONF,conf,r.digest(original));r.docker('exec',r.NGINX,'nginx','-t');r.docker('exec',r.NGINX,'nginx','-s','reload')
        r.replace_inside('/usr/share/nginx/public/index.html',(candidate/'index.html').read_bytes(),r.digest(old_index))
        start_bot(build,BOT_DATA,False)
        key=r.envmap(r.inspect('ztapi-server-1'))['ZTAPI_SESSION_SIGNING_KEY']
        status,cfg=r.fetch('https://ztapi.vip/api/support/config',key);r.require(status==200 and cfg['data']['telegram_binding_enabled'],'support_binding_route_failed')
        r.require(r.fetch('https://ztapi.vip/internal/telegram-support',data={'action':'binding_status','telegram_id':50999999001})[0]==404,'internal_bridge_exposed')
        r.require(r.fetch('https://ztapi.vip/api/admin/support/tickets',key)[0]==404,'public_staff_boundary_changed')
        r.require(r.fetch('https://admin.ztapi.vip/api/admin/support/tickets',key)[0]==200,'staff_support_failed')
        r.require(r.fetch('https://ztapi.vip/api/status')[0]==200 and r.fetch('https://ztapi.vip/api/pricing')[0]==200,'model_api_health_failed')
        finance=r.fetch('https://admin.ztapi.vip/api/admin/finance-reporting/report?month=2026-11',key);r.require(finance[0]==200 and finance[1]['data']['support_source_status']=='available','finance_reader_failed')
        with closing(sqlite3.connect('file:'+str(BOT_DATA/'support.db')+'?mode=ro',uri=True)) as db:
            r.require(db.execute('SELECT enabled FROM telegram_support_groups WHERE chat_id=?',(-1004401851162,)).fetchone()==(1,),'target_group_not_enabled')
        check_core(baseline,False)
        r.json_save('published.json',{'published':True,'commit':build['commit'],'main_api_unchanged':True,'finance_not_restarted':True,'nginx_not_restarted':True,'support':r.identity(r.inspect(NEW_SITE)),'telegram':r.identity(r.inspect(NEW_BOT)),'nginx_sha256':r.digest(conf),'group_preserved':True,'original_alert_settings_preserved':True,'backups_verified':True,'immutable_receipts_preserved':True})
    except Exception:
        rollback(original,old_index);raise


if __name__=='__main__':
    os.umask(0o077)
    try:
        r.require(os.geteuid()==0 and len(sys.argv)==2,'root_action_required')
        actions={'prepare':prepare,'web-checks':web_checks,'live-tests':live_tests,'record-costs':record_costs,'activate':activate}
        r.require(sys.argv[1] in actions,'invalid_experience_action');actions[sys.argv[1]]();print(json.dumps({'ok':True,'action':sys.argv[1]}))
    except Exception as e:
        print(json.dumps({'ok':False,'reason':str(e) if isinstance(e,RuntimeError) else 'experience_operation_failed','error_type':type(e).__name__,'automatic_model_retry':False}));raise SystemExit(1)
