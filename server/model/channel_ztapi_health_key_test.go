package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestZTAPIHealthCredentialPinSelectsExactEnabledMultiKey(t *testing.T) {
	channel := Channel{
		Key: "first-key\nsecond-key\nthird-key\nfourth-key",
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeyMode: constant.MultiKeyModeRandom,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusEnabled, 1: common.ChannelStatusEnabled,
				2: common.ChannelStatusAutoDisabled, 3: common.ChannelStatusManuallyDisabled,
			},
		},
	}
	fingerprint, err := FingerprintZTAPICredential("authorization\x00Bearer second-key")
	require.NoError(t, err)
	key, index, apiErr := channel.GetEnabledKeyByZTAPIHealthCredentialVersion(fingerprint.String())
	require.Nil(t, apiErr)
	require.Equal(t, "second-key", key)
	require.Equal(t, 1, index)

	disabled, err := FingerprintZTAPICredential("authorization\x00Bearer third-key")
	require.NoError(t, err)
	_, _, apiErr = channel.GetEnabledKeyByZTAPIHealthCredentialVersion(disabled.String())
	require.NotNil(t, apiErr)

	manuallyDisabled, err := FingerprintZTAPICredential("authorization\x00Bearer fourth-key")
	require.NoError(t, err)
	_, _, apiErr = channel.GetEnabledKeyByZTAPIHealthCredentialVersion(manuallyDisabled.String())
	require.NotNil(t, apiErr)

	missing, err := FingerprintZTAPICredential("authorization\x00Bearer missing-key")
	require.NoError(t, err)
	_, _, apiErr = channel.GetEnabledKeyByZTAPIHealthCredentialVersion(missing.String())
	require.NotNil(t, apiErr)
}

func TestZTAPIHealthExcludedCredentialSelectsAnotherEnabledMultiKey(t *testing.T) {
	channel := &Channel{
		Id: 78, Key: "pool-broken\nenterprise-healthy",
		ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModeRandom},
	}
	broken, err := FingerprintZTAPICredential("authorization\x00Bearer pool-broken")
	require.NoError(t, err)
	key, index, apiErr := channel.GetNextEnabledKeyExcluding([]string{broken.String()})
	require.Nil(t, apiErr)
	require.Equal(t, "enterprise-healthy", key)
	require.Equal(t, 1, index)
}
