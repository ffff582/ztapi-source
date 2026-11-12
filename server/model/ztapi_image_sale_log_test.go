package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageSaleLogMetadataUsesFrozenQuoteVersion(t *testing.T) {
	for _, policy := range []string{types.ZTAPIImageSaleContract20261109JSON(), types.ZTAPIImageSalePartial20261109JSON()} {
		quote, err := types.QuoteZTAPIImageSale(policy, "1K", 100, 1, 1)
		require.NoError(t, err)
		raw, err := common.Marshal(ztapiSettlementLogMetadata{ImageSaleQuote: &quote, SupplierCostStatus: "quoted_tokens"})
		require.NoError(t, err)
		log := Log{Other: string(raw)}
		require.NoError(t, sanitizeZTAPISettlementLogMetadata(&log))
		var kept ztapiSettlementLogMetadata
		require.NoError(t, common.UnmarshalJsonStr(log.Other, &kept))
		require.Equal(t, quote, *kept.ImageSaleQuote)

		quote.TotalUSD = "0.01"
		raw, err = common.Marshal(ztapiSettlementLogMetadata{ImageSaleQuote: &quote, SupplierCostStatus: "quoted_tokens"})
		require.NoError(t, err)
		log.Other = string(raw)
		require.ErrorIs(t, sanitizeZTAPISettlementLogMetadata(&log), ErrZTAPISettlementLogInvalid)
	}
}
