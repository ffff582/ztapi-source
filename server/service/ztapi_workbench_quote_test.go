package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchQuoteCoversAllFrozenTokenTiers(t *testing.T) {
	quotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 1000000
	t.Cleanup(func() { common.QuotaPerUnit = quotaPerUnit })
	snapshot := &relaycommon.ZTAPIPublicationSnapshot{Modality: model.ZTAPIModalityText, PriceSourceID: 1,
		AllowedChannelIDs: []int{7}, SaleUSD: map[string]string{"input_tokens": "2", "output_tokens": "4", "cache_write_1h": "3"},
		TokenPriceRulesJSON: `[{"conditions":[],"sale":{"input_tokens":"3","output_tokens":"8","cache_write_1h":"5"}}]`}
	quota, err := ZTAPIWorkbenchQuote(snapshot, 2048, 1024)
	require.NoError(t, err)
	require.EqualValues(t, max(2048+256, common.PreConsumedQuota)*5+1024*8, quota)
	snapshot.TokenPriceRulesJSON = `[{"conditions":[],"sale":{"input_tokens":"3","output_tokens":"8","image_units":"5"}}]`
	_, err = ZTAPIWorkbenchQuote(snapshot, 2048, 1024)
	require.ErrorIs(t, err, model.ErrZTAPISettlementInvalid)
}
