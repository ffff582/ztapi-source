package controller

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRelayClientErrorKeepsInternalAndNonAuthErrorsIntact(t *testing.T) {
	upstream := types.WithOpenAIError(types.OpenAIError{
		Type: "authentication_error", Code: "invalid_api_key", Message: "raw-provider-auth-detail",
	}, http.StatusUnauthorized)
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		mapped := relayClientError(upstream, format)
		require.NotSame(t, upstream, mapped)
		require.Equal(t, http.StatusServiceUnavailable, mapped.StatusCode)
		require.Equal(t, http.StatusUnauthorized, upstream.StatusCode)
		require.Equal(t, types.ErrorCode("invalid_api_key"), upstream.GetErrorCode())
		require.Equal(t, "raw-provider-auth-detail", upstream.Error())
		require.Equal(t, "raw-provider-auth-detail", upstream.ToOpenAIError().Message)
	}
	for _, original := range []*types.NewAPIError{
		nil,
		types.NewErrorWithStatusCode(errors.New("client key is invalid"), types.ErrorCode("invalid_api_key"), http.StatusUnauthorized),
		types.NewErrorWithStatusCode(errors.New("wallet has insufficient quota"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden),
		types.WithOpenAIError(types.OpenAIError{Code: "invalid_request"}, http.StatusBadRequest),
		types.WithOpenAIError(types.OpenAIError{Code: "permission_denied"}, http.StatusForbidden),
		types.WithOpenAIError(types.OpenAIError{Code: "safety_refusal"}, http.StatusForbidden),
		types.WithOpenAIError(types.OpenAIError{Code: "rate_limit_exceeded"}, http.StatusTooManyRequests),
		types.WithOpenAIError(types.OpenAIError{Code: "upstream_error"}, http.StatusBadGateway),
	} {
		if original == nil {
			require.Nil(t, relayClientError(nil, types.RelayFormatClaude))
			continue
		}
		require.Same(t, original, relayClientError(original, types.RelayFormatClaude))
	}
	for _, original := range []*types.NewAPIError{
		types.WithOpenAIError(types.OpenAIError{Code: "model_not_granted"}, http.StatusForbidden),
		types.WithClaudeError(types.ClaudeError{Type: "authentication_error"}, http.StatusForbidden),
		types.InitOpenAIError(types.ErrorCodeBadResponseStatusCode, http.StatusUnauthorized),
	} {
		require.Equal(t, http.StatusServiceUnavailable, relayClientError(original, types.RelayFormatClaude).StatusCode)
	}
}

func TestZTAPIUpstreamAuthIsNotReportedAsCustomerAuthentication(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/stream=%t", path, status, stream), func(t *testing.T) {
					f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
					f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
					ids := configureZTAPIRetryChannels(t, f, 1)
					before := f.balances(t)
					badKey := f.request(t, path, f.publicName, "invalid-local-key", "auth-boundary-bad-key", stream)
					require.Equal(t, http.StatusUnauthorized, badKey.Code, badKey.Body.String())
					require.Equal(t, before, f.balances(t), "a real customer auth failure must never dispatch or reserve")
					f.queue(ztapiE2EReply{status: status, body: `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"raw-provider-auth-detail"}}`})
					response := f.request(t, path, f.publicName, f.key, "auth-boundary-upstream", stream)
					require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
					require.NotContains(t, response.Body.String(), "raw-provider-auth-detail")
					require.NotContains(t, response.Body.String(), "invalid_api_key")
					require.Contains(t, response.Body.String(), "upstream")
					if path == "/v1/messages" {
						require.Equal(t, "error", gjson.GetBytes(response.Body.Bytes(), "type").String())
						require.Equal(t, "overloaded_error", gjson.GetBytes(response.Body.Bytes(), "error.type").String())
					} else {
						require.Equal(t, "upstream_unavailable", gjson.GetBytes(response.Body.Bytes(), "error.code").String())
					}
					events := f.events(t)
					require.Len(t, events, 1)
					require.Equal(t, status, events[0].HTTPStatus, "health must retain the real supplier status")
					require.Equal(t, "upstream_auth", events[0].Reason)
					settlement := f.assertSettlement(t, 0, model.ZTAPISettlementReleased, ids)
					require.Zero(t, settlement.ChargedQuota)
					after := f.balances(t)
					require.Equal(t, before.UserQuota, after.UserQuota)
					require.Equal(t, before.TokenRemain, after.TokenRemain)
					require.Equal(t, before.TokenUsed, after.TokenUsed)
					var attempts []model.ZTAPIRequestAttempt
					require.NoError(t, f.db.Where("settlement_id = ?", settlement.ID).Find(&attempts).Error)
					require.Len(t, attempts, 1)
					require.Equal(t, status, attempts[0].HTTPStatus)
				})
			}
		}
	}
}

func TestZTAPIClaudeMessagesChatBridgeForwardsTheReasoningFloor(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
			f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
			configureZTAPIRetryChannels(t, f, 1)
			f.queue(ztapiE2ESuccess("/v1/chat/completions"))
			response := f.request(t, path, f.publicName, f.key, "claude-floor-bridge", false)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "Hello.")
			f.mu.Lock()
			bodies := append([][]byte(nil), f.upstreamBodies...)
			f.mu.Unlock()
			require.Len(t, bodies, 1)
			require.EqualValues(t, 1024, gjson.GetBytes(bodies[0], "max_tokens").Int())
			require.False(t, gjson.GetBytes(bodies[0], "reasoning_effort").Exists())
			require.False(t, gjson.GetBytes(bodies[0], "thinking").Exists())
			var frozen map[string]any
			var settlement model.ZTAPIRequestSettlement
			require.NoError(t, f.db.Take(&settlement).Error)
			require.NoError(t, common.UnmarshalJsonStr(settlement.PriceSnapshotJSON, &frozen))
			require.NotEmpty(t, frozen)
		})
	}
}

func TestZTAPIClaudeRaisedOutputBudgetMustBeFundedBeforeDispatch(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
	configureZTAPIRetryChannels(t, f, 1)
	require.NoError(t, f.db.Model(&model.User{}).Where("id = ?", f.user.Id).Update("quota", 1000).Error)
	before := f.balances(t)
	response := f.request(t, "/v1/messages", f.publicName, f.key, "claude-floor-insufficient-wallet", false)
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
	require.Zero(t, f.calls.Load(), "a caller unable to fund the raised cap must not reach the supplier")
	after := f.balances(t)
	require.Equal(t, before.UserQuota, after.UserQuota)
	require.Equal(t, before.TokenRemain, after.TokenRemain)
	require.Equal(t, before.TokenUsed, after.TokenUsed)
	require.Equal(t, before.LedgerRows, after.LedgerRows)
}
