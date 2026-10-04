package types

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func imageContractWithRequestPolicy(t *testing.T, policy any) ZTAPIImageProtocolContract {
	t.Helper()
	contract := syntheticZTAPIImageProtocolContract()
	contract.Version = ZTAPIImageProtocolContractVersionV2
	contract.RequestIDField = ""
	contract.RequestIDSource, contract.RequestIDKey = ZTAPIResponseIDSourceHeader, "X-Synthetic-Request-ID"
	raw, err := common.Marshal(contract)
	require.NoError(t, err)
	var object map[string]any
	require.NoError(t, common.Unmarshal(raw, &object))
	object["upstream_request_fields"] = policy
	raw, err = common.Marshal(object)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(raw, &contract))
	return contract
}

func imageRequestPolicy() map[string]string {
	return map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "optional", "response_format": "omit"}
}

func TestZTAPIImageRequestPolicyFrozenRoundTrip(t *testing.T) {
	contract := imageContractWithRequestPolicy(t, imageRequestPolicy())
	sealed, canonical, err := SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	require.Contains(t, canonical, `"upstream_request_fields":`)
	parsed, roundTrip, err := ParseZTAPIImageProtocolContract(canonical)
	require.NoError(t, err)
	require.Equal(t, canonical, roundTrip)
	require.Equal(t, sealed, parsed)
	other := imageRequestPolicy()
	other["response_format"] = "required"
	changed, _, err := SealZTAPIImageProtocolContract(imageContractWithRequestPolicy(t, other))
	require.NoError(t, err)
	require.NotEqual(t, sealed.EvidenceHash, changed.EvidenceHash)
	_, _, err = ParseZTAPIImageProtocolContract(strings.Replace(canonical, `"response_format":"omit"`, `"response_format":"required"`, 1))
	require.ErrorContains(t, err, "hash mismatch")
	cloned := sealed.Clone()
	cloned.UpstreamRequestFields["response_format"] = "required"
	require.Equal(t, "omit", sealed.UpstreamRequestFields["response_format"])
}

func TestZTAPIImageRequestPolicyRejectsIncompleteOrUnsafePolicy(t *testing.T) {
	for _, name := range []string{"missing", "unknown field", "unknown policy", "omit model", "optional prompt", "empty", "v1"} {
		t.Run(name, func(t *testing.T) {
			policy := imageRequestPolicy()
			switch name {
			case "missing":
				delete(policy, "quality")
			case "unknown field":
				policy["user"] = "optional"
			case "unknown policy":
				policy["quality"] = "default"
			case "omit model":
				policy["model"] = "omit"
			case "optional prompt":
				policy["prompt"] = "optional"
			case "empty":
				policy = map[string]string{}
			}
			contract := imageContractWithRequestPolicy(t, policy)
			if name == "v1" {
				contract.Version = ZTAPIImageProtocolContractVersion
				contract.RequestIDField = "request_id"
				contract.RequestIDSource, contract.RequestIDKey = "", ""
			}
			_, _, err := SealZTAPIImageProtocolContract(contract)
			require.Error(t, err)
		})
	}
}

func TestZTAPIGPTImageV2RequiresFrozenResponseFormatOmissionPolicy(t *testing.T) {
	for _, tt := range []struct {
		name   string
		policy map[string]string
	}{
		{name: "missing"},
		{name: "empty", policy: map[string]string{}},
		{name: "response format required", policy: map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "optional", "response_format": "required"}},
		{name: "response format optional", policy: map[string]string{"model": "required", "prompt": "required", "n": "required", "size": "required", "quality": "optional", "response_format": "optional"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			contract := imageContractWithRequestPolicy(t, tt.policy)
			contract.ProviderModel = "gpt-image-2"
			_, _, err := SealZTAPIImageProtocolContract(contract)
			require.Error(t, err)
		})
	}

	valid := imageContractWithRequestPolicy(t, imageRequestPolicy())
	valid.ProviderModel = "gpt-image-2"
	_, _, err := SealZTAPIImageProtocolContract(valid)
	require.NoError(t, err)
}

func TestZTAPIGPTImageV2ParsedPolicyRejectsNull(t *testing.T) {
	contract := imageContractWithRequestPolicy(t, imageRequestPolicy())
	contract.ProviderModel = "gpt-image-2"
	_, canonical, err := SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	var object map[string]any
	require.NoError(t, common.UnmarshalJsonStr(canonical, &object))
	object["upstream_request_fields"] = nil
	nullPolicy, err := common.Marshal(object)
	require.NoError(t, err)
	_, _, err = ParseZTAPIImageProtocolContract(string(nullPolicy))
	require.ErrorContains(t, err, "upstream image request policy")
}

func TestZTAPIImageRequestPolicyPreservesOlderV2WithoutPolicy(t *testing.T) {
	contract := imageContractWithRequestPolicy(t, nil)
	contract.UpstreamRequestFields = nil
	sealed, canonical, err := SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	require.NotEqual(t, "gpt-image-2", sealed.ProviderModel)
	require.NotContains(t, canonical, `"upstream_request_fields"`)
	_, reparsed, err := ParseZTAPIImageProtocolContract(canonical)
	require.NoError(t, err)
	require.Equal(t, canonical, reparsed)
}
