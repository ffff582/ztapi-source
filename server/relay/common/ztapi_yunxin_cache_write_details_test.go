package common

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestYunxinResponsesZeroTTLDetailsDoNotBlockSettlement(t *testing.T) {
	const zero = `{"ttl_30m_tokens":0,"ttl_12h_tokens":0}`
	for _, tt := range []struct {
		name, aggregate, details string
		channel                  int
		format                   types.RelayFormat
		model, baseURL           string
		want                     bool
	}{
		{"captured_receipt", "0", zero, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", true},
		{"unverified_channel", "0", zero, 4, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"wrong_host", "0", zero, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://other.example/hub", false},
		{"different_publication", "0", zero, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"other_model", "0", zero, 2, types.RelayFormatOpenAIResponses, "gpt-5.6-sol", "https://ai.yunxinapi.com/hub", false},
		{"unmapped_model", "0", zero, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol", "https://ai.yunxinapi.com/hub", false},
		{"chat", "0", zero, 2, types.RelayFormatOpenAI, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"nonzero_aggregate", "10", zero, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"nonzero_ttl", "10", `{"ttl_30m_tokens":10,"ttl_12h_tokens":0}`, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"missing_ttl", "0", `{"ttl_30m_tokens":0}`, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"missing_aggregate", "", zero, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"unknown_ttl", "0", `{"ttl_30m_tokens":0,"ttl_12h_tokens":0,"ttl_other":0}`, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"duplicate_ttl", "0", `{"ttl_30m_tokens":0,"ttl_30m_tokens":0,"ttl_12h_tokens":0}`, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
		{"invalid_ttl_type", "0", `{"ttl_30m_tokens":"0","ttl_12h_tokens":0}`, 2, types.RelayFormatOpenAIResponses, "gpt-6.1-sol-hc", "https://ai.yunxinapi.com/hub", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := uniformCacheWriteInfo()
			info.ZTAPIPublicationSnapshot.PublicName = "zt-gpt-6.1-sol"
			info.ZTAPIPublicationSnapshot.SourceModel = "gpt-6.1-sol"
			if tt.name == "different_publication" {
				info.ZTAPIPublicationSnapshot.PublicName = "zt-other-model"
			}
			info.FinalRequestRelayFormat = tt.format
			info.ChannelMeta = &ChannelMeta{ChannelId: tt.channel, ChannelBaseUrl: tt.baseURL, UpstreamModelName: tt.model}
			aggregate := ""
			if tt.aggregate != "" {
				aggregate = `,"cache_write_tokens":` + tt.aggregate
			}
			raw := `{"usage":{"input_tokens":4226,"input_tokens_details":{"cached_tokens":4096` + aggregate +
				`,"cache_write_tokens_details":` + tt.details + `},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":4231}}`
			usage := ObserveZTAPIOpenAIUsage(nil, info, []byte(raw))
			if !tt.want {
				require.Nil(t, usage)
				require.NotEmpty(t, info.ZTAPIMetering.UsageIntegrityReasons)
				require.True(t, ValidateZTAPIUsageDimensions(nil, info, &dto.Usage{}).Pending)
				return
			}
			require.NotNil(t, usage)
			require.Empty(t, info.ZTAPIMetering.UsageIntegrityReasons)
			require.Equal(t, "upstream_reported", usage.UsageSource)
			result := ValidateZTAPIUsageDimensions(nil, info, usage)
			require.False(t, result.Pending)
			require.Equal(t, int64(130), usageDimension(t, result, "input_tokens").Quantity)
			require.Equal(t, int64(4096), usageDimension(t, result, "cache_read").Quantity)
			require.Equal(t, int64(5), usageDimension(t, result, "output_tokens").Quantity)
			require.Len(t, result.Dimensions, 3)
		})
	}
}
