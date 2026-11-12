package common

import (
	"math"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningOutputReservationUsesFrozenPricesAndRoundsUp(t *testing.T) {
	previousUnit, previousMinimum := basecommon.QuotaPerUnit, basecommon.PreConsumedQuota
	basecommon.QuotaPerUnit, basecommon.PreConsumedQuota = 500000, 500
	t.Cleanup(func() {
		basecommon.QuotaPerUnit, basecommon.PreConsumedQuota = previousUnit, previousMinimum
	})
	for _, tc := range []struct {
		name     string
		snapshot ZTAPIPublicationSnapshot
		prompt   int
		want     int
	}{
		{
			name: "float quote", prompt: 1000, want: 6120,
			snapshot: ZTAPIPublicationSnapshot{InputPricePerMillion: 2, OutputPricePerMillion: 10},
		},
		{
			name: "canonical prices override float display values", prompt: 1000, want: 6120,
			snapshot: ZTAPIPublicationSnapshot{
				InputPricePerMillion: 99, OutputPricePerMillion: 99,
				SaleUSD: map[string]string{"input_tokens": "2", "output_tokens": "10"},
			},
		},
		{
			name: "selected frozen tier overrides base prices", prompt: 1000, want: 6120,
			snapshot: ZTAPIPublicationSnapshot{
				BillingDimensions:   []string{"input_tokens", "output_tokens"},
				SaleUSD:             map[string]string{"input_tokens": "99", "output_tokens": "99"},
				TokenPriceRulesJSON: `[{"conditions":[],"sale":{"input_tokens":"2","output_tokens":"10"}}]`,
			},
		},
		{
			name: "fractional quota rounds upward", prompt: 1001, want: 11,
			snapshot: ZTAPIPublicationSnapshot{SaleUSD: map[string]string{"input_tokens": "0.01", "output_tokens": "0.01"}},
		},
		{
			name: "minimum prompt reserve retained", prompt: 1, want: 5620,
			snapshot: ZTAPIPublicationSnapshot{InputPricePerMillion: 2, OutputPricePerMillion: 10},
		},
		{
			name: "explicit free quote", prompt: 1000, want: 0,
			snapshot: ZTAPIPublicationSnapshot{SaleUSD: map[string]string{"input_tokens": "0", "output_tokens": "0"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.snapshot.Clone()
			quota, err := ZTAPIReasoningOutputReservationQuota(&tc.snapshot, tc.prompt, 1024)
			require.NoError(t, err)
			require.Equal(t, tc.want, quota)
			require.Equal(t, before, &tc.snapshot, "reservation must not mutate the frozen quote")
		})
	}
}

func TestZTAPIReasoningOutputReservationRejectsInvalidQuote(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot *ZTAPIPublicationSnapshot
	}{
		{"absent quote", nil},
		{"missing output price", &ZTAPIPublicationSnapshot{SaleUSD: map[string]string{"input_tokens": "2"}}},
		{"negative price", &ZTAPIPublicationSnapshot{SaleUSD: map[string]string{"input_tokens": "2", "output_tokens": "-1"}}},
		{"non-finite price", &ZTAPIPublicationSnapshot{InputPricePerMillion: 2, OutputPricePerMillion: math.Inf(1)}},
		{"invalid price rules", &ZTAPIPublicationSnapshot{TokenPriceRulesJSON: "invalid"}},
		{"quota overflow", &ZTAPIPublicationSnapshot{SaleUSD: map[string]string{"input_tokens": "2", "output_tokens": "100000000000000000000"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ZTAPIReasoningOutputReservationQuota(tc.snapshot, 1000, 1024)
			require.Error(t, err)
		})
	}
	_, err := ZTAPIReasoningOutputReservationQuota(&ZTAPIPublicationSnapshot{InputPricePerMillion: 2, OutputPricePerMillion: 10}, 1000, 0)
	require.Error(t, err)
}
