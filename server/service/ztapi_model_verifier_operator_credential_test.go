package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type ztapiOperatorCredentialFixture struct {
	channel model.Channel
	config  model.ZTAPIModelConfig
	source  model.ZTAPIModelPriceSource
}

func TestVerifyZTAPIXcloud20261128RetainsReceiptsWhenPersistenceFails(t *testing.T) {
	f := setupZTAPIOperatorCredential(t, false)
	previousRunner := ztapiModelVerificationProbeRunner
	t.Cleanup(func() { ztapiModelVerificationProbeRunner = previousRunner })
	ztapiModelVerificationProbeRunner = func(context.Context, *model.Channel, string) (ztapiModelProbeResult, error) {
		return ztapiModelProbeResult{StatusCategory: "verified", ProbeReceipts: []model.ZTAPIModelProbeReceipt20261128{{
			Mode: "nonstream", StatusCode: 200, RequestID: "req-preserved-write-failure", UsageJSON: `{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}`, Complete: true,
		}}}, nil
	}
	writeFailure := errors.New("synthetic verification persistence failure")
	const callback = "xcloud_operator_reject_verification_write"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.ZTAPIModelVerification{}).TableName() {
			tx.AddError(writeFailure)
		}
	}))
	t.Cleanup(func() { model.DB.Callback().Create().Remove(callback) })
	verification, err := VerifyZTAPIXcloud20261128WithCredential(t.Context(), f.channel.Id, f.config.SourceModel, 31, "synthetic-temporary-key")
	require.ErrorIs(t, err, writeFailure)
	require.NotNil(t, verification, "paid probe metadata must survive failure to persist its verification row")
	require.Zero(t, verification.ID)
	require.Equal(t, "persistence_failed", verification.StatusCategory)
	require.Len(t, verification.ProbeReceipts, 1)
	require.Equal(t, "req-preserved-write-failure", verification.ProbeReceipts[0].RequestID)
}

func TestZTAPIXcloud20261128ProbeRejectsRedirects(t *testing.T) {
	for _, location := range []string{"https://www.xcloudtoken.com/other", "http://www.xcloudtoken.com/other", "https://redirect.invalid/other"} {
		t.Run(location, func(t *testing.T) {
			f := setupZTAPIOperatorCredential(t, false)
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			requests := 0
			http.DefaultTransport = ztapiVerifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if requests > 1 {
					return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
				}
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			})
			_, err := runZTAPIModelVerificationProbes(t.Context(), &f.channel, f.config.SourceModel)
			require.Error(t, err)
			require.Equal(t, 1, requests, "a probe cannot follow redirects beyond its exact quoted endpoint")
		})
	}
}

func setupZTAPIOperatorCredential(t *testing.T, multiKey bool) ztapiOperatorCredentialFixture {
	t.Helper()
	previousCache, previousDB := common.MemoryCacheEnabled, model.DB
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousCache
		if previousCache && previousDB != nil {
			model.InitChannelCache()
		}
	})
	channel, _ := setupZTAPIModelVerifierTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Ability{}, &model.ZTAPICatalogLock{}, &model.ZTAPIAuditEvent{},
		&model.ZTAPIModelIdentity{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{},
		&model.ZTAPIDiscoverySnapshot{}, &model.ZTAPIDiscoveredModel{}))
	url := "https://www.xcloudtoken.com"
	channel.Id, channel.BaseURL, channel.Group, channel.Models = 7, &url, "default", "gpt-6-sol"
	channel.Status = common.ChannelStatusEnabled
	channel.Key = "synthetic-original-xcloud-key"
	if multiKey {
		channel.Key += "\nsynthetic-original-xcloud-second-key"
		channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling,
			MultiKeyPollingIndex: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled}}
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	stage, err := model.StageZTAPIXcloud20261128(31, model.ZTAPIXcloudStage20261128{
		SourceModel: "gpt-6.2-sol", Group: "gpt-plus", ChannelID: channel.Id}, []string{"gpt-6.2-sol"}, time.Now())
	require.NoError(t, err)
	require.NoError(t, model.DB.First(&channel, channel.Id).Error)
	require.NotEmpty(t, channel.Key, "read back after the managed BeforeSave encryption hook")
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	return ztapiOperatorCredentialFixture{channel, stage.Config, stage.Source}
}

func TestVerifyZTAPIXcloud20261128WithCredentialIsolatedRunnerAndPersistence(t *testing.T) {
	for _, mode := range []string{"pass", "fail", "multi_key_pass", "multi_key_fail"} {
		t.Run(mode, func(t *testing.T) {
			f := setupZTAPIOperatorCredential(t, strings.HasPrefix(mode, "multi_key"))
			failed := strings.HasSuffix(mode, "fail")
			const credential = "synthetic-temporary-capped-credential"
			cached, err := model.CacheGetChannel(f.channel.Id)
			require.NoError(t, err)
			cacheBefore, err := json.Marshal(cached)
			require.NoError(t, err)
			keysBefore := append([]string(nil), cached.Keys...)
			previousRunner := ztapiModelVerificationProbeRunner
			t.Cleanup(func() { ztapiModelVerificationProbeRunner = previousRunner })
			calls := 0
			ztapiModelVerificationProbeRunner = func(ctx context.Context, got *model.Channel, source string) (ztapiModelProbeResult, error) {
				calls++
				require.NotSame(t, cached, got)
				require.Equal(t, f.config.SourceModel, source)
				require.Equal(t, credential, got.Key)
				require.Empty(t, got.Keys)
				require.Empty(t, got.ZTAPIKeyCiphertext)
				require.False(t, got.ChannelInfo.IsMultiKey)
				require.Nil(t, got.ChannelInfo.MultiKeyStatusList)
				selected, _, apiErr := got.GetNextEnabledKey()
				require.Nil(t, apiErr)
				require.Equal(t, credential, selected, "runner must not select or poll an original production key")
				_, bounded := ctx.Deadline()
				require.True(t, bounded)
				got.Key = "runner-local-mutation"
				result := ztapiModelProbeResult{NonStreamingPassed: true, StreamingRequired: true, StreamingPassed: !failed,
					UsageReconciled: !failed, PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4, StatusCategory: "verified",
					ProbeReceipts: []model.ZTAPIModelProbeReceipt20261128{{Mode: "nonstream", StatusCode: 200, RequestID: "req-offline-123", UsageJSON: `{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}`, Complete: true}}}
				if failed {
					result.StatusCategory = "upstream_response"
					return result, errors.New(credential)
				}
				return result, nil
			}
			verification, err := VerifyZTAPIXcloud20261128WithCredential(t.Context(), f.channel.Id, f.config.SourceModel, 31, credential)
			if failed {
				require.Error(t, err)
				require.NotContains(t, err.Error(), credential)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, calls)
			require.NotNil(t, verification)
			require.Positive(t, verification.ID)
			require.Equal(t, f.config.ID, verification.ModelConfigID)
			require.Equal(t, model.ZTAPIModalityText, verification.Modality)
			require.Equal(t, !failed, verification.StreamingPassed)
			require.Len(t, verification.ProbeReceipts, 1)
			var stored model.ZTAPIModelVerification
			require.NoError(t, model.DB.First(&stored, verification.ID).Error)
			require.Equal(t, verification.StatusCategory, stored.StatusCategory)
			require.Equal(t, verification.TotalTokens, stored.TotalTokens)
			require.Empty(t, stored.ProbeReceipts)
			for _, value := range []any{verification, stored} {
				raw, marshalErr := json.Marshal(value)
				require.NoError(t, marshalErr)
				require.NotContains(t, string(raw), credential)
			}
			original, err := model.GetChannelById(f.channel.Id, true)
			require.NoError(t, err)
			require.Equal(t, f.channel.Key, original.Key)
			require.Equal(t, f.channel.ZTAPIKeyCiphertext, original.ZTAPIKeyCiphertext)
			require.Equal(t, f.channel.Models, original.Models, "verification cannot install a new route")
			require.Equal(t, f.channel.ChannelInfo, original.ChannelInfo)
			var rawChannel struct {
				Key                string `gorm:"column:key"`
				ZTAPIKeyCiphertext string `gorm:"column:ztapi_key_ciphertext"`
			}
			require.NoError(t, model.DB.Table("channels").Select("key", "ztapi_key_ciphertext").Where("id = ?", f.channel.Id).Scan(&rawChannel).Error)
			require.Empty(t, rawChannel.Key)
			require.Equal(t, f.channel.ZTAPIKeyCiphertext, rawChannel.ZTAPIKeyCiphertext)
			cachedAfter, err := model.CacheGetChannel(f.channel.Id)
			require.NoError(t, err)
			require.Same(t, cached, cachedAfter)
			cacheAfter, err := json.Marshal(cachedAfter)
			require.NoError(t, err)
			require.Equal(t, string(cacheBefore), string(cacheAfter))
			require.Equal(t, keysBefore, cachedAfter.Keys)
			var configAfter model.ZTAPIModelConfig
			require.NoError(t, model.DB.First(&configAfter, f.config.ID).Error)
			require.Equal(t, f.config, configAfter)
		})
	}
}

func TestVerifyZTAPIXcloud20261128WithCredentialUsesTemporaryAuthorization(t *testing.T) {
	for _, failureAt := range []int{0, 1, 2} {
		name := "pass"
		if failureAt == 1 {
			name = "nonstream_failure"
		}
		if failureAt == 2 {
			name = "stream_failure"
		}
		t.Run(name, func(t *testing.T) {
			f := setupZTAPIOperatorCredential(t, true)
			const credential = "synthetic-temporary-transport-key"
			cached, err := model.CacheGetChannel(f.channel.Id)
			require.NoError(t, err)
			cacheBefore, err := json.Marshal(cached)
			require.NoError(t, err)
			previousRunner, previousTransport := ztapiModelVerificationProbeRunner, http.DefaultTransport
			t.Cleanup(func() { ztapiModelVerificationProbeRunner, http.DefaultTransport = previousRunner, previousTransport })
			ztapiModelVerificationProbeRunner = runZTAPIModelVerificationProbes
			calls := 0
			http.DefaultTransport = ztapiVerifierRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "www.xcloudtoken.com", request.URL.Host)
				require.Equal(t, "/v1/chat/completions", request.URL.Path)
				var payload map[string]any
				require.NoError(t, common.DecodeJson(request.Body, &payload))
				require.Equal(t, f.config.SourceModel, payload["model"])
				if calls == 3 {
					require.Equal(t, "Bearer ztapi-deliberately-invalid-credential", request.Header.Get("Authorization"))
					return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				}
				require.Equal(t, "Bearer "+credential, request.Header.Get("Authorization"))
				status, body := 200, ztapiReceiptBody20261128(false, calls == 2, ztapiReceiptChatUsage20261128)
				if calls == failureAt {
					status, body = 500, `{"error":{"message":"rejected `+credential+`"}}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"req-temporary-offline"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			verification, err := VerifyZTAPIXcloud20261128WithCredential(t.Context(), f.channel.Id, f.config.SourceModel, 31, credential)
			if failureAt == 0 {
				require.NoError(t, err)
				require.Equal(t, 3, calls)
			} else {
				require.Error(t, err)
				require.Equal(t, failureAt, calls)
			}
			require.NotNil(t, verification)
			require.Positive(t, verification.ID)
			raw, err := json.Marshal(verification)
			require.NoError(t, err)
			require.NotContains(t, string(raw), credential, "even upstream failure metadata must redact the temporary credential")
			original, err := model.GetChannelById(f.channel.Id, true)
			require.NoError(t, err)
			require.Equal(t, f.channel.Key, original.Key)
			require.Equal(t, f.channel.ZTAPIKeyCiphertext, original.ZTAPIKeyCiphertext)
			require.Equal(t, f.channel.ChannelInfo, original.ChannelInfo)
			cacheAfter, err := json.Marshal(cached)
			require.NoError(t, err)
			require.Equal(t, string(cacheBefore), string(cacheAfter))
		})
	}
}

func TestVerifyZTAPIXcloud20261128WithCredentialRejectsBeforeRunner(t *testing.T) {
	for _, mode := range []string{"blank", "same_key", "same_key_whitespace", "same_multi_key_member", "header_injection",
		"wrong_channel_group", "changed_customer_group", "wrong_host", "wrong_physical", "mapping_cycle", "duplicate_mapping",
		"unmanaged", "disabled", "wrong_transport", "wrong_source", "wrong_policy", "wrong_checksum", "wrong_cash",
		"no_staged_source", "newer_non_xcloud_source", "wrong_protocol", "wrong_provider", "wrong_public_name", "unknown_model", "operator_missing"} {
		t.Run(mode, func(t *testing.T) {
			f := setupZTAPIOperatorCredential(t, mode == "same_multi_key_member")
			key, sourceModel, channelID, operatorID := "synthetic-temporary-key", f.config.SourceModel, f.channel.Id, 31
			switch mode {
			case "blank":
				key = " \t "
			case "same_key":
				key = f.channel.Key
			case "same_key_whitespace":
				key = " " + f.channel.Key + " "
			case "same_multi_key_member":
				key = "synthetic-original-xcloud-second-key"
			case "header_injection":
				key = "synthetic-key\r\nX-Other: bad"
			case "wrong_channel_group":
				other := f.channel
				other.Id = 6
				require.NoError(t, model.DB.Create(&other).Error)
				channelID = 6
			case "changed_customer_group":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("group", "other").Error)
			case "wrong_host":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("base_url", "https://wrong-upstream.invalid").Error)
			case "wrong_physical":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("model_mapping", `{"gpt-6.2-sol":"gpt-6.1-sol"}`).Error)
			case "mapping_cycle":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("model_mapping", `{"gpt-6.2-sol":"other","other":"gpt-6.2-sol"}`).Error)
			case "duplicate_mapping":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("model_mapping", `{"gpt-6.2-sol":"wrong","gpt-6.2-sol":"gpt-6.2-sol"}`).Error)
			case "unmanaged":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("ztapi_managed", false).Error)
			case "disabled":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("status", common.ChannelStatusManuallyDisabled).Error)
			case "wrong_transport":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Update("type", constant.ChannelTypeGemini).Error)
			case "wrong_source":
				require.NoError(t, model.DB.Model(&model.ZTAPIModelPriceSource{}).Where("id = ?", f.source.ID).Update("source_model", "gpt-6.1-sol").Error)
			case "wrong_policy":
				require.NoError(t, model.DB.Model(&model.ZTAPIModelPriceSource{}).Where("id = ?", f.source.ID).Update("price_policy", string(model.ZTAPIPricePolicyXcloud20261114)).Error)
			case "wrong_checksum":
				require.NoError(t, model.DB.Model(&model.ZTAPIModelPriceSource{}).Where("id = ?", f.source.ID).Update("source_document_checksum", strings.Repeat("f", 64)).Error)
			case "wrong_cash":
				require.NoError(t, model.DB.Model(&model.ZTAPIModelPriceSource{}).Where("id = ?", f.source.ID).Update("input_per_million", "99").Error)
			case "no_staged_source":
				require.NoError(t, model.DB.Delete(&model.ZTAPIModelPriceSource{}, f.source.ID).Error)
			case "newer_non_xcloud_source":
				newer := f.source
				newer.ID = 0
				newer.Version++
				newer.PricePolicy = string(model.ZTAPIPricePolicyEnterprise40Margin)
				require.NoError(t, model.DB.Create(&newer).Error)
			case "wrong_protocol":
				require.NoError(t, model.DB.Model(&model.ZTAPIModelConfig{}).Where("id = ?", f.config.ID).Update("protocol", model.ZTAPIProtocolGemini).Error)
			case "wrong_provider":
				require.NoError(t, model.DB.Model(&model.ZTAPIModelConfig{}).Where("id = ?", f.config.ID).Update("provider_family", model.ZTAPIProviderAnthropic).Error)
			case "wrong_public_name":
				require.NoError(t, model.DB.Model(&model.ZTAPIModelConfig{}).Where("id = ?", f.config.ID).Update("public_name", "zt-other").Error)
			case "unknown_model":
				sourceModel = "gpt-6.2-sol-near-match"
			case "operator_missing":
				operatorID = 0
			}
			previousRunner := ztapiModelVerificationProbeRunner
			t.Cleanup(func() { ztapiModelVerificationProbeRunner = previousRunner })
			calls := 0
			ztapiModelVerificationProbeRunner = func(context.Context, *model.Channel, string) (ztapiModelProbeResult, error) {
				calls++
				return ztapiModelProbeResult{}, errors.New("must not dispatch")
			}
			verification, err := VerifyZTAPIXcloud20261128WithCredential(t.Context(), channelID, sourceModel, operatorID, key)
			require.Error(t, err)
			require.Nil(t, verification)
			require.Zero(t, calls)
			var count int64
			require.NoError(t, model.DB.Model(&model.ZTAPIModelVerification{}).Count(&count).Error)
			require.Zero(t, count, "rejected overrides cannot leave fake verification rows")
		})
	}
}
