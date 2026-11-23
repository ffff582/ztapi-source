package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func performZTAPIWorkbenchRequest(router *gin.Engine, endpoint, user, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/__workbench_replay/"+endpoint, bytes.NewBufferString(body))
	req.RemoteAddr = "192.0.2.11:1000"
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+user)
	}
	req.Header.Set("X-ZTAPI-Workbench-Key", key)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}

func TestZTAPIWorkbenchHTTPReplay(t *testing.T) {
	dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "workbench.db"))
	db := setupZTAPIAuthRouterTestDBWithDSN(t, dsn)
	require.NoError(t, db.AutoMigrate(&model.BalanceLedger{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIWorkbenchTask{}))
	engine := newZTAPIRealRouter(t, false)
	seedZTAPIRouterUser(t, "alice", "at-least-ten")
	seedZTAPIRouterUser(t, "bob", "at-least-ten")
	const address = "192.0.2.11:1000"
	login := func(name string) (int, string) {
		r := performZTAPIRouterRequest(engine, "POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":"at-least-ten"}`, name), "", address)
		require.Equal(t, 200, r.Code)
		var auth ztAPIRealRouterResponse
		require.NoError(t, common.Unmarshal(r.Body.Bytes(), &auth))
		require.NotEmpty(t, auth.Data.AccessToken)
		return auth.Data.User.ID, auth.Data.AccessToken
	}
	owner, access := login("alice")
	_, other := login("bob")
	require.Equal(t, 404, performZTAPIWorkbenchRequest(engine, "reserve", access, "", `{}`).Code)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", owner).Update("quota", 1000).Error)
	newKey := func(quota int) (model.Token, string) {
		secret, hash, prefix, err := common.GenerateZTAPIKey()
		require.NoError(t, err)
		key := model.Token{UserId: owner, KeyHash: hash, KeyPrefix: prefix, Status: common.TokenStatusEnabled, RemainQuota: quota,
			ModelLimitsEnabled: true, ModelLimits: controller.WorkbenchReplayModel, ExpiredTime: time.Now().Add(10 * time.Minute).Unix()}
		require.NoError(t, model.DB.Create(&key).Error)
		return key, secret
	}
	key, secret := newKey(12) // Eight input bytes plus two output tokens at rate 2.
	var calls int
	var interrupted bool
	bind := func(target *gin.Engine) {
		local := target.Group("/__workbench_replay", middleware.ZTAPIUserAuth())
		controller.MountWorkbenchReplay(local, func(_ context.Context, input string, limit int) (controller.WorkbenchReplayReceipt, error) {
			calls++
			if interrupted {
				return controller.WorkbenchReplayReceipt{}, errors.New("simulated disconnect")
			}
			require.Equal(t, "abcdefgh", input)
			require.Equal(t, 2, limit)
			return controller.WorkbenchReplayReceipt{Text: "ok", InputTokens: 3, OutputTokens: 1}, nil
		})
	}
	bind(engine)
	request := func(router *gin.Engine, endpoint, user, key, json string, want int) []byte {
		t.Helper()
		r := performZTAPIWorkbenchRequest(router, endpoint, user, key, json)
		require.Equal(t, want, r.Code, "%s: %s", endpoint, r.Body.String())
		return r.Body.Bytes()
	}
	const task = "e2e84201-1111-4444-8888-aec098cf7654"
	const requestID = "981d2080-1111-4444-8888-aec098cf7654"
	const req = `{"input":"abcdefgh","max_output_tokens":2}`
	reserve := func(id, nonce, legs string) string {
		return fmt.Sprintf(`{"task":%q,"request":%q,"legs":%s}`, id, nonce, legs)
	}
	leg := `[{"stage":"main","body":` + req + `}]`
	body := reserve(task, requestID, leg)
	request(engine, "reserve", "", secret, body, 401)
	request(engine, "reserve", other, secret, body, 403)
	request(engine, "reserve", access, secret, body[:len(body)-1]+`,"quota":1}`, 400)
	request(engine, "reserve", access, secret, body[:len(body)-1]+`,"task":"duplicate"}`, 400)
	request(engine, "reserve", access, secret, reserve(task, requestID, `[{"stage":"main","body":{"input":"abc","max_output_tokens":513}}]`), 400)
	var held struct{ Holds []struct{ ID, Stage string } }
	accepted := request(engine, "reserve", access, secret, body, 200)
	require.NoError(t, common.Unmarshal(accepted, &held))
	require.Len(t, held.Holds, 1)
	require.Equal(t, accepted, request(engine, "reserve", access, secret, body, 200))
	request(engine, "reserve", access, secret, reserve(task, requestID, `[{"stage":"child","body":`+req+`}]`), 409)
	request(engine, "reserve", access, secret, reserve(task, "13c42080-1111-4444-8888-aec098cf7654", leg), 402)
	check := func(wallet, keyBalance int, tokenID int) {
		t.Helper()
		u, err := model.GetUserById(owner, false)
		require.NoError(t, err)
		k, err := model.GetTokenByIds(tokenID, owner)
		require.NoError(t, err)
		require.Equal(t, wallet, u.Quota)
		require.Equal(t, keyBalance, k.RemainQuota)
	}
	check(988, 0, key.Id)
	dispatch := func(id, hold, json string) string {
		return fmt.Sprintf(`{"task":%q,"hold":%q,"body":%s}`, id, hold, json)
	}
	op := held.Holds[0].ID
	request(engine, "dispatch", access, secret, dispatch(task, op, `{"input":"hgfedcba","max_output_tokens":2}`), 409)
	require.Zero(t, calls)
	request(engine, "dispatch", access, secret, dispatch(task, op, req), 200)
	check(995, 7, key.Id)
	request(engine, "dispatch", access, secret, dispatch(task, op, req), 409)
	require.Equal(t, 1, calls)

	secondKey, secondSecret := newKey(30)
	pair := `[{"stage":"summary","body":` + req + `},{"stage":"continuation","body":` + req + `}]`
	accepted = request(engine, "reserve", access, secondSecret, reserve(task, "bb3e2080-1111-4444-8888-aec098cf7654", pair), 200)
	require.NoError(t, common.Unmarshal(accepted, &held))
	require.Len(t, held.Holds, 2)
	request(engine, "dispatch", access, secondSecret, dispatch(task, held.Holds[1].ID, req), 409)
	interrupted = true
	request(engine, "dispatch", access, secondSecret, dispatch(task, held.Holds[0].ID, req), 502)
	check(971, 6, secondKey.Id)
	var pending model.ZTAPIRequestSettlement
	require.NoError(t, db.Where("operation_id = ?", held.Holds[0].ID).Take(&pending).Error)
	require.Equal(t, model.ZTAPISettlementPending, pending.Status)
	require.True(t, pending.Dispatched)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	reopened, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	reopenedSQL, err := reopened.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopenedSQL.Close() })
	model.DB, model.LOG_DB = reopened, reopened
	restarted := newZTAPIRealRouter(t, false)
	bind(restarted)
	request(restarted, "dispatch", access, secondSecret, dispatch(task, held.Holds[0].ID, req), 409)
	request(restarted, "dispatch", access, secondSecret, dispatch(task, held.Holds[1].ID, req), 409)
	require.Equal(t, 2, calls)
	check(971, 6, secondKey.Id)

	interrupted = false
	thirdKey, thirdSecret := newKey(30)
	accepted = request(restarted, "reserve", access, thirdSecret, reserve(task, "cd7e2080-1111-4444-8888-aec098cf7654", pair), 200)
	require.NoError(t, common.Unmarshal(accepted, &held))
	require.Len(t, held.Holds, 2)
	request(restarted, "dispatch", access, thirdSecret, dispatch(task, held.Holds[0].ID, req), 200)
	request(restarted, "dispatch", access, thirdSecret, dispatch(task, held.Holds[1].ID, req), 200)
	check(961, 20, thirdKey.Id)
	require.Equal(t, 4, calls)
}
