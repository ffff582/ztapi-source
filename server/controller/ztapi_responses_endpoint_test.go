package controller

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/stretchr/testify/require"
)

type ztapiResponsesEndpointTransport struct {
	base http.RoundTripper
	t    *testing.T
}

func (r ztapiResponsesEndpointTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/v1/responses" {
		r.t.Errorf("expected Responses upstream endpoint, got %q", req.URL.Path)
	}
	return r.base.RoundTrip(req)
}

func newZTAPIResponsesEndpointFixture(t *testing.T) *ztapiE2EFixture {
	t.Helper()
	f := newZTAPIHealthE2EFixture(t)
	settings := model_setting.GetGlobalSettings()
	old := *settings
	t.Cleanup(func() { *settings = old })
	settings.PassThroughRequestEnabled = false
	settings.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{}
	f.sourceModel, f.publicName = "gpt-5.4-pro", "zt-gpt-5.4-pro"
	seedEnabledPublicModel(t, f.db, f.publicName)
	require.NoError(t, f.db.Model(&model.ZTAPIModelPriceSource{}).Where("source_model IN ?", []string{"gpt-5.5", f.sourceModel}).Update("resource_type", "enterprise").Error)
	f.config = model.ZTAPIModelConfig{}
	require.NoError(t, f.db.First(&f.config, "public_name = ?", f.publicName).Error)
	f.assertPublishedPrice(t)
	require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", 1).Update("models", f.sourceModel).Error)
	require.NoError(t, f.db.Model(&model.Ability{}).Where("channel_id = ?", 1).Update("model", f.sourceModel).Error)
	require.NoError(t, f.db.Model(f.token).Update("model_limits", f.publicName).Error)
	model.InitChannelCache()
	model.InvalidateZTAPIAliasCache()
	model.InvalidatePricingCache()
	return f
}

func TestZTAPIResponsesEndpointRejectsChatBeforeAnyCharge(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			f := newZTAPIResponsesEndpointFixture(t)
			f.queue(ztapiE2ESuccess("/v1/chat/completions"))
			before := f.balances(t)
			w := f.request(t, "/v1/chat/completions", f.publicName, f.key, "responses-only-rejected", stream)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "/v1/responses")
			require.Contains(t, w.Body.String(), "unsupported_model_endpoint")
			require.Equal(t, before, f.balances(t), "no wallet/token precharge, refund-masked writes, health admission or dispatch")
			require.Empty(t, f.events(t))
		})
	}
}

func TestZTAPIResponsesEndpointAllowsNativeAndExplicitConversion(t *testing.T) {
	for _, converted := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("converted=%t/stream=%t", converted, stream), func(t *testing.T) {
				f := newZTAPIResponsesEndpointFixture(t)
				path := "/v1/responses"
				if converted {
					path = "/v1/chat/completions"
					model_setting.GetGlobalSettings().ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{Enabled: true, ChannelIDs: []int{1}, ModelPatterns: []string{`^zt-gpt-5\.4-pro$`}}
				}
				client := service.GetHttpClient()
				client.Transport = ztapiResponsesEndpointTransport{base: client.Transport, t: t}
				reply := ztapiE2ESuccess("/v1/responses")
				if stream {
					reply.stream = true
					reply.body = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello.\"}\n\n" +
						"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + reply.body + "}\n\n"
				}
				f.queue(reply)
				w := f.request(t, path, f.publicName, f.key, "responses-only-allowed", stream)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "Hello.")
				after := f.balances(t)
				require.EqualValues(t, 1, after.Calls)
				require.Equal(t, ztapiE2EInitialQuota-17, after.UserQuota, "10 input + 5 output tokens at the fixture publication price, charged once")
				require.Equal(t, after.UserQuota, after.TokenRemain)
				require.Equal(t, ztapiE2EInitialQuota-after.UserQuota, after.TokenUsed)
				f.assertSettlement(t, 0, model.ZTAPISettlementSettled, []int{1})
			})
		}
	}
}
