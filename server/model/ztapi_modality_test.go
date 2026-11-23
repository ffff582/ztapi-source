package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A video model the A/B workbook introduces must not be treated as text: that
// would bill it per token against a text price and route it to chat.
func TestZTAPIModelModalityCoversIntroducedQuotationModels(t *testing.T) {
	require.Equal(t, ZTAPIModalityVideo, ZTAPIModelModality("doubao-seedance-2-5"))
	require.Equal(t, ZTAPIModalityText, ZTAPIModelModality("kimi-k3"))
	require.Equal(t, ZTAPIModalityVideo, ZTAPIModelModality("doubao-seedance-2.0"))
	require.Equal(t, ZTAPIModalityText, ZTAPIModelModality("not-a-quoted-model"))
}
