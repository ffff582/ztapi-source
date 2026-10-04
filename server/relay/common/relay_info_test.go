package common

import (
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}

func TestZTAPIManagedImageDispatchPreservesMultipartContentType(t *testing.T) {
	info := &RelayInfo{}
	dispatch := &ZTAPIManagedImageDispatch{
		Body: []byte("multipart-body"), ProviderPath: "/v1/images/edits",
		WireProtocol: "openai_images_edit_multipart", ContentType: "multipart/form-data; boundary=upstream-boundary",
	}
	require.True(t, info.SetZTAPIManagedImageDispatch(dispatch))
	got := info.GetZTAPIManagedImageDispatch()
	require.Equal(t, dispatch.ContentType, got.ContentType)
	dispatch.ContentType = "mutated"
	require.Equal(t, "multipart/form-data; boundary=upstream-boundary", info.GetZTAPIManagedImageDispatch().ContentType)
}
