package helper

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeChatOutputFloorReservesTheEffectiveOutputBudget(t *testing.T) {
	previousQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = previousQuotaPerUnit })
	for _, tc := range []struct {
		name, source string
		channel, cap int
		wantQuota    int
	}{
		{"sonnet chat tiny cap", "claude-sonnet-5", constant.ChannelTypeOpenAI, 64, 6120},
		{"opus chat tiny cap", "claude-opus-5", constant.ChannelTypeOpenAI, 64, 6120},
		{"fable chat tiny cap", "claude-fable-5", constant.ChannelTypeOpenAI, 16, 6120},
		{"native messages unchanged", "claude-sonnet-5", constant.ChannelTypeAnthropic, 64, 1064},
		{"haiku unchanged", "claude-haiku-4.5", constant.ChannelTypeOpenAI, 64, 1064},
		{"existing large cap unchanged", "claude-sonnet-5", constant.ChannelTypeOpenAI, 4096, 5096},
		{"omitted cap unchanged", "claude-sonnet-5", constant.ChannelTypeOpenAI, 0, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(ctx, constant.ContextKeyChannelType, tc.channel)
			info := &relaycommon.RelayInfo{
				OriginModelName: "zt-claude-budget-fixture",
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
					PublicName: "zt-claude-budget-fixture", SourceModel: tc.source,
					InputPricePerMillion: 2, OutputPricePerMillion: 10,
					CacheReadRatio: 0.1, CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25,
					CacheCreation1hRatio: 2, ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1,
				},
			}
			meta := &types.TokenCountMeta{MaxTokens: tc.cap}
			price, err := ModelPriceHelper(ctx, info, 1000, meta)
			require.NoError(t, err)
			require.Equal(t, tc.wantQuota, price.QuotaToPreConsume)
			require.Equal(t, tc.cap, meta.MaxTokens, "pricing must not mutate the client's cap or native request")
			require.Equal(t, 2.0, info.ZTAPIPublicationSnapshot.InputPricePerMillion)
			require.Equal(t, 10.0, info.ZTAPIPublicationSnapshot.OutputPricePerMillion)
		})
	}
}
