// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateXcloudUsagePricesExclusiveCacheDimensions(t *testing.T) {
	estimate, err := EstimateXcloudUsage("claude-opus-4-8", []UsageDimension{
		{Dimension: "input_tokens", Quantity: 1_000_000},
		{Dimension: "output_tokens", Quantity: 1_000_000},
		{Dimension: "cache_read", Quantity: 1_000_000},
		{Dimension: "cache_write", Quantity: 1_000_000},
		{Dimension: "cache_write_5m", Quantity: 1_000_000},
		{Dimension: "cache_write_1h", Quantity: 1_000_000},
	})
	require.NoError(t, err)
	require.Equal(t, "53.000000", estimate.CostUSDT)
	require.Equal(t, "https://www.xcloudtoken.com/pricing", estimate.SourceURL)
	require.True(t, estimate.IsEstimate)
}

func TestEstimateXcloudUsageRejectsDuplicateDimension(t *testing.T) {
	_, err := EstimateXcloudUsage("claude-opus-4-8", []UsageDimension{
		{Dimension: "input_tokens", Quantity: 1},
		{Dimension: "input_tokens", Quantity: 1},
	})
	require.Error(t, err)
}

func TestEstimateXcloudUsageRequiresPublicQuote(t *testing.T) {
	estimate, err := EstimateXcloudUsage("claude-opus-5", []UsageDimension{{
		Dimension: "input_tokens", Quantity: 1_000_000,
	}})
	require.ErrorIs(t, err, ErrXcloudQuoteMissing)
	require.Equal(t, "0", estimate.CostUSDT)
	require.False(t, estimate.IsEstimate)
}
