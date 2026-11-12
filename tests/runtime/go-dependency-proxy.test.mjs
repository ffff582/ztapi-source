import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import fs from 'node:fs';
import {fileURLToPath} from 'node:url';
import test from 'node:test';

const root=fileURLToPath(new URL('../../',import.meta.url));
const dockerfile=fs.readFileSync(new URL('../../server/Dockerfile.ztapi',import.meta.url),'utf8');

test('Docker dependency proxy is optional and keeps checksum verification enabled',()=>{
 assert.match(dockerfile,/ARG GOPROXY=https:\/\/proxy\.golang\.org,direct/);
 assert.match(dockerfile,/RUN GOPROXY="\$GOPROXY" go mod download/);
 assert.doesNotMatch(dockerfile,/GOSUMDB\s*=\s*(?:off|"off")/);
 for(const kind of ['dev','prod']){
  const file=`deploy/docker/docker-compose.${kind}.yml`;
  const text=fs.readFileSync(new URL('../../'+file,import.meta.url),'utf8');
  const env={...process.env};
  for(const [,name] of text.matchAll(/\$\{([A-Z_]+):\?/g))env[name]='a'.repeat(40);
  env.ZTAPI_TLS_DIR=root;
  for(const value of [null,'https://goproxy.cn,direct']){
   delete env.ZTAPI_DOCKER_GOPROXY;
   if(value)env.ZTAPI_DOCKER_GOPROXY=value;
   const result=spawnSync('docker',['compose','-f',file,'config','--format','json'],{cwd:root,env,encoding:'utf8',timeout:30000});
   assert.equal(result.status,0,result.stderr);
   const config=JSON.parse(result.stdout);
   assert.equal(config.services.server.build.args.GOPROXY,value??'https://proxy.golang.org,direct');
   assert.ok(!Object.hasOwn(config.services.server.environment,'GOPROXY'));
  }
 }
});
