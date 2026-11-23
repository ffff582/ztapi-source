package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchStreamReplayWithR35Client(t *testing.T) {
	db := setupZTAPIAuthRouterTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.BalanceLedger{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIWorkbenchTask{}))
	seedZTAPIRouterUser(t, "r35-wire", "at-least-ten")
	var owner model.User
	require.NoError(t, db.Where("username = ?", "r35-wire").Take(&owner).Error)
	require.NoError(t, db.Model(&owner).Update("quota", 10000).Error)
	secret, hash, _, err := common.GenerateZTAPIKey()
	require.NoError(t, err)
	key := model.Token{UserId: owner.Id, KeyHash: hash, Status: common.TokenStatusEnabled, RemainQuota: 5000,
		ModelLimitsEnabled: true, ModelLimits: controller.WorkbenchReplayModel, ExpiredTime: time.Now().Add(10 * time.Minute).Unix()}
	require.NoError(t, db.Create(&key).Error)
	engine := newZTAPIRealRouter(t, false)
	const reserve = "/__workbench_budget/reserve"
	require.Equal(t, http.StatusNotFound, performZTAPIRouterRequest(engine, http.MethodPost, reserve, "{}", "Bearer "+secret, "192.0.2.2:1234").Code)
	var calls atomic.Int32
	controller.MountWorkbenchStreamReplay(engine.Group(""), func(_ context.Context, body []byte) (io.ReadCloser, controller.WorkbenchReplayReceipt, error) {
		calls.Add(1)
		prefix := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_local\"}}\n\n" +
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"test\"}\n\n"
		terminal := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_local\",\"model\":\"zt-workbench-replay\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n"
		receipt := controller.WorkbenchReplayReceipt{InputTokens: 3, OutputTokens: 1}
		if strings.Contains(string(body), "truncate") {
			return io.NopCloser(strings.NewReader(prefix)), receipt, nil
		}
		if strings.Contains(string(body), "mismatch") {
			receipt.InputTokens = 4
		}
		return io.NopCloser(strings.NewReader(prefix + terminal)), receipt, nil
	})
	server := httptest.NewServer(engine)
	defer server.Close()
	client, err := filepath.Abs(filepath.Join("..", "..", "..", "20261112-r35-budget-resume", "src", "gateway-budget-client.mjs"))
	require.NoError(t, err)
	if _, err = os.Stat(client); os.IsNotExist(err) {
		t.Skip("Archived R35 client is not present alongside R36")
	}
	require.NoError(t, err)
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", r35StreamReplayProbe, client, server.URL)
	cmd.Stdin = strings.NewReader(secret)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), "R35_STREAM_REPLAY_OK")
	require.EqualValues(t, 5, calls.Load())
	var rows []model.ZTAPIRequestSettlement
	require.NoError(t, db.Where("workbench_task_id <> ''").Find(&rows).Error)
	counts := map[string]int{}
	var debit int64
	for _, row := range rows {
		counts[row.Status]++
		switch row.Status {
		case model.ZTAPISettlementSettled:
			require.EqualValues(t, 5, row.ChargedQuota)
			debit += row.ChargedQuota
		case model.ZTAPISettlementPending:
			require.True(t, row.Dispatched)
			require.Zero(t, row.ChargedQuota)
			debit += row.ReservedQuota
		case model.ZTAPISettlementReleased:
			require.False(t, row.Dispatched)
		}
	}
	require.Equal(t, map[string]int{"settled": 3, "pending": 2, "released": 1}, counts)
	updated, err := model.GetUserById(owner.Id, false)
	require.NoError(t, err)
	updatedKey, err := model.GetTokenByIds(key.Id, owner.Id)
	require.NoError(t, err)
	require.EqualValues(t, 10000-debit, updated.Quota)
	require.EqualValues(t, 5000-debit, updatedKey.RemainQuota)
}

const r35StreamReplayProbe = `
import {readFileSync} from 'node:fs';
import {pathToFileURL} from 'node:url';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
const [client, origin]=process.argv.slice(1);
const clientUrl=pathToFileURL(client);
const {GatewayTaskBudget}=await import(clientUrl);
const {observeUsage}=await import(new URL('./task-spend.mjs',clientUrl));
const gateway=new GatewayTaskBudget(origin);
const key=readFileSync(0,'utf8');
const model='zt-workbench-replay';
const request=text=>({model,input:[{role:'user',content:[{type:'input_text',text}]}],stream:true,store:false,max_output_tokens:4});
const leg=(stage,body)=>({stage,bytes:Buffer.byteLength(JSON.stringify(body)),output:body.max_output_tokens});
const task=randomUUID();
const main=request('hello');
await assert.rejects(gateway.reserve(key,task,model,[{stage:'main',bytes:24000,output:1}]),/TASK_BUDGET_EXHAUSTED/);
const [hold]=await gateway.reserve(key,task,model,[leg('main',main)]);
async function check(res,hold,status){
 assert.equal(res.status,status);
 if(status===200){
  assert.match(res.headers.get('content-type'),/^text\/event-stream/);
  assert.equal(res.headers.get('x-ztapi-budget-hold'),hold.id);
  const observer=observeUsage();
  for await(const part of res.body)observer.feed(part);
  assert.equal(observer.receipt(),4);
  assert.deepEqual(observer.details(),{input:3,output:1,cacheRead:0,cacheWrite:0});
 }
 else {assert.equal(res.headers.get('x-ztapi-budget-hold'),null);await res.body?.cancel();}
}
async function relay(hold,body,status){await check(await gateway.relay(key,hold,body),hold,status);}
await relay(hold,{...main,max_output_tokens:5},402);
await relay(hold,request('oversize payload'),402);
const duplicates=await Promise.all([gateway.relay(key,hold,main),gateway.relay(key,hold,main)]);
duplicates.sort((a,b)=>a.status-b.status);
await check(duplicates[0],hold,200);
await check(duplicates[1],hold,409);
await relay(hold,main,409);
const summary=request('summary'), continuation=request('continue');
const [sum,next]=await gateway.reserve(key,task,model,[leg('summary',summary),leg('continuation',continuation)]);
await relay(next,continuation,409);
await relay(sum,summary,200);
await relay(next,continuation,200);
for (const text of ['truncate','mismatch']) {
 const body=request(text);
 const [hold]=await gateway.reserve(key,task,model,[leg('child',body)]);
 await relay(hold,body,502);
 await relay(hold,body,409);
 await assert.rejects(gateway.release(key,hold),/TASK_BUDGET_RELEASE_UNCONFIRMED/);
}
const [unused]=await gateway.reserve(key,task,model,[leg('main',main)]);
await gateway.release(key,unused);
await gateway.release(key,unused);
await relay(unused,main,409);
console.log('R35_STREAM_REPLAY_OK');
`
