package common

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestZTAPITokenTierSelectsFrozenGPTContextPrice(t *testing.T) {
	snapshot := &ZTAPIPublicationSnapshot{
		PriceSourceID: 12, PriceSourceVersion: 2,
		BillingDimensions:   []string{"input_tokens", "output_tokens"},
		SaleUSD:             map[string]string{"input_tokens": "3.9", "output_tokens": "19.5"},
		TokenPriceRulesJSON: `[{"conditions":["输入长度≤272K"],"sale":{"input_tokens":"3.9","output_tokens":"19.5"}},{"conditions":["输入长度>272K"],"sale":{"input_tokens":"7.8","output_tokens":"29.25"}}]`,
	}
	for _, test := range []struct {
		prompt int
		want   string
	}{
		{prompt: 272000, want: "3.9"},
		{prompt: 272001, want: "7.8"},
	} {
		selected, tier, err := SelectZTAPIFrozenTokenTier(snapshot, &dto.Usage{PromptTokens: test.prompt, CompletionTokens: 10})
		require.NoError(t, err)
		require.NotEmpty(t, tier)
		require.Equal(t, test.want, selected.SaleUSD["input_tokens"])
		require.Equal(t, "3.9", snapshot.SaleUSD["input_tokens"])
	}
}

func TestZTAPITokenTierSelectsGLMOutputBoundary(t *testing.T) {
	snapshot := &ZTAPIPublicationSnapshot{
		PriceSourceID: 13, PriceSourceVersion: 2,
		BillingDimensions:   []string{"input_tokens", "output_tokens"},
		SaleUSD:             map[string]string{"input_tokens": "1", "output_tokens": "4"},
		TokenPriceRulesJSON: `[{"conditions":["输入长度=<32K且输出长度<0.2K"],"sale":{"input_tokens":"1","output_tokens":"4"}},{"conditions":["输入长度=<32K且输出长度≥0.2K"],"sale":{"input_tokens":"1.5","output_tokens":"7"}},{"conditions":["输入长度=≥32K且<200K"],"sale":{"input_tokens":"2","output_tokens":"8"}}]`,
	}
	short, _, err := SelectZTAPIFrozenTokenTier(snapshot, &dto.Usage{PromptTokens: 1000, CompletionTokens: 199})
	require.NoError(t, err)
	require.Equal(t, "1", short.SaleUSD["input_tokens"])
	long, _, err := SelectZTAPIFrozenTokenTier(snapshot, &dto.Usage{PromptTokens: 1000, CompletionTokens: 200})
	require.NoError(t, err)
	require.Equal(t, "1.5", long.SaleUSD["input_tokens"])
	inputLong, _, err := SelectZTAPIFrozenTokenTier(snapshot, &dto.Usage{PromptTokens: 32000, CompletionTokens: 199})
	require.NoError(t, err)
	require.Equal(t, "2", inputLong.SaleUSD["input_tokens"])
}

func TestZTAPITokenTierFailsClosedOnUnmatchedOrMixedInputType(t *testing.T) {
	snapshot := &ZTAPIPublicationSnapshot{
		PriceSourceID: 14, PriceSourceVersion: 2,
		BillingDimensions:   []string{"input_tokens", "output_tokens"},
		SaleUSD:             map[string]string{"input_tokens": "1", "output_tokens": "4"},
		TokenPriceRulesJSON: `[{"conditions":["输入类型=文本/图像/视频"],"sale":{"input_tokens":"1","output_tokens":"4"}},{"conditions":["输入类型=音频"],"sale":{"input_tokens":"2","output_tokens":"4"}}]`,
	}
	_, _, err := SelectZTAPIFrozenTokenTier(snapshot, &dto.Usage{PromptTokens: 100, PromptTokensDetails: dto.InputTokenDetails{AudioTokens: 50}})
	require.Error(t, err)
	_, _, err = SelectZTAPIFrozenTokenTier(snapshot, nil)
	require.Error(t, err)
}
