package controller

import (
	"fmt"
	"math"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func claudeNativeThenChatFixture(t *testing.T) (*ztapiE2EFixture, []int) {
	t.Helper()
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
	ids := configureZTAPIRetryChannels(t, f, 2)
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", ids[0]).Update("type", constant.ChannelTypeAnthropic).Error)
	model.InitChannelCache()
	model.InvalidateZTAPIAliasCache()
	return f, ids
}

func TestZTAPIClaudeNativeToChatFallbackFundsTheRaisedBudget(t *testing.T) {
	f, _ := claudeNativeThenChatFixture(t)
	f.queue(
		ztapiE2EReply{status: http.StatusUnauthorized, body: `{"type":"error","error":{"type":"authentication_error","message":"fixture unavailable"}}`},
		ztapiE2ESuccess("/v1/chat/completions"),
	)
	response := f.request(t, "/v1/messages", f.publicName, f.key, "native-chat-funded", false)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	f.mu.Lock()
	paths := append([]string(nil), f.upstreamPaths...)
	bodies := append([][]byte(nil), f.upstreamBodies...)
	reservations := append([]int64(nil), f.upstreamReservations...)
	f.mu.Unlock()
	require.Equal(t, []string{"/v1/messages", "/v1/chat/completions"}, paths)
	require.EqualValues(t, 64, gjson.GetBytes(bodies[0], "max_tokens").Int())
	require.EqualValues(t, 1024, gjson.GetBytes(bodies[1], "max_tokens").Int())
	require.Len(t, reservations, 2)
	minimumOutputReserve := int64(math.Ceil(1024 * 3.3333333333 * common.QuotaPerUnit / 1_000_000))
	require.GreaterOrEqual(t, reservations[1], minimumOutputReserve, "the raised cap must be funded before supplier I/O")
}

func TestZTAPIClaudeNativeToChatFallbackStopsWhenBudgetTopUpFails(t *testing.T) {
	for _, budget := range []string{"wallet", "token"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", budget, stream), func(t *testing.T) {
				f, _ := claudeNativeThenChatFixture(t)
				if budget == "wallet" {
					require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", f.user.Id).Update("quota", 1000).Error)
				} else {
					require.NoError(t, f.db.Model(&model.Token{}).Where("id = ?", f.token.Id).Update("remain_quota", 1000).Error)
				}
				before := f.balances(t)
				f.queue(
					ztapiE2EReply{status: http.StatusUnauthorized, body: `{"type":"error","error":{"type":"authentication_error","message":"fixture unavailable"}}`},
					ztapiE2ESuccess("/v1/chat/completions"),
				)
				response := f.request(t, "/v1/messages", f.publicName, f.key, "native-chat-unfunded-"+budget, stream)
				require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
				require.EqualValues(t, 1, f.calls.Load(), "no paid fallback may be sent when the reservation extension fails")
				after := f.balances(t)
				require.Equal(t, before.UserQuota, after.UserQuota)
				require.Equal(t, before.TokenRemain, after.TokenRemain)
				require.Equal(t, before.TokenUsed, after.TokenUsed)
			})
		}
	}
}

func TestZTAPIClaudeFallbackDoesNotReuseNativeResponseProtocol(t *testing.T) {
	f, _ := claudeNativeThenChatFixture(t)
	f.queue(
		ztapiE2EReply{status: http.StatusOK, body: `{"type":"error","error":{"type":"overloaded_error","message":"fixture busy"}}`},
		ztapiE2ESuccess("/v1/chat/completions"),
	)
	response := f.request(t, "/v1/messages", f.publicName, f.key, "native-response-chat-fallback", false)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "Hello.")
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, []string{"/v1/messages", "/v1/chat/completions"}, f.upstreamPaths)
	require.EqualValues(t, 1024, gjson.GetBytes(f.upstreamBodies[1], "max_tokens").Int(), fmt.Sprint(f.upstreamPaths))
}
