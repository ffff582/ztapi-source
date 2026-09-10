package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func syntheticZTAPIImageProtocolContract() ZTAPIImageProtocolContract {
	contract := ZTAPIImageProtocolContract{
		Version:       1,
		ProviderModel: "synthetic-image-v1",
		EndpointType:  "images_generation",
		Method:        "POST",
		Path:          "/v1/images/generations",
		Capabilities: ZTAPIImageCapabilities{
			Sizes: []string{"1024x1024", "512x512"}, Qualities: []string{"standard", "high"},
			ResponseFormats: []string{"url", "b64_json"}, MinCount: 1, MaxCount: 4, SupportsEdits: false,
		},
		Response: ZTAPIImageResponseContract{
			Schema: "object_results_array", ResultsField: "data",
			ResultFields: map[string]string{"url": "url", "b64_json": "b64_json"},
		},
		Usage: ZTAPIImageUsageContract{
			UsageField: "usage", TotalField: "total_tokens",
			Fields:         map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"},
			TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported",
		},
		RequestIDField: "request_id", EvidenceVersion: 1,
	}
	for _, size := range contract.Capabilities.Sizes {
		for _, quality := range contract.Capabilities.Qualities {
			for _, responseFormat := range contract.Capabilities.ResponseFormats {
				for n := contract.Capabilities.MinCount; n <= contract.Capabilities.MaxCount; n++ {
					contract.Reservations = append(contract.Reservations, ZTAPIImageReservationAuthority{
						Size: size, Quality: quality, ResponseFormat: responseFormat, N: n,
						MaximumDimensions: map[string]string{"input_tokens": "200000", "output_tokens": "4096"},
					})
				}
			}
		}
	}
	return contract
}

func TestZTAPIImageProtocolContractCanonicalizesAndVerifiesHash(t *testing.T) {
	sealed, canonical, err := SealZTAPIImageProtocolContract(syntheticZTAPIImageProtocolContract())
	require.NoError(t, err)
	require.Len(t, sealed.EvidenceHash, 64)

	parsed, reparsedCanonical, err := ParseZTAPIImageProtocolContract(canonical)
	require.NoError(t, err)
	require.Equal(t, sealed, parsed)
	require.Equal(t, canonical, reparsedCanonical)
	require.Equal(t, []string{"1024x1024", "512x512"}, parsed.Capabilities.Sizes)
	require.Equal(t, []string{"high", "standard"}, parsed.Capabilities.Qualities)
}

func TestZTAPIImageProtocolContractRejectsUnknownDuplicateAndHashMismatch(t *testing.T) {
	_, canonical, err := SealZTAPIImageProtocolContract(syntheticZTAPIImageProtocolContract())
	require.NoError(t, err)

	hashPrefix := `"evidence_hash":"`
	hashStart := strings.Index(canonical, hashPrefix) + len(hashPrefix)
	require.GreaterOrEqual(t, hashStart, len(hashPrefix))
	replacement := byte('0')
	if canonical[hashStart] == replacement {
		replacement = '1'
	}
	tamperedHash := canonical[:hashStart] + string(replacement) + canonical[hashStart+1:]
	cases := []string{
		strings.Replace(canonical, `"version":1`, `"version":1,"unknown":true`, 1),
		strings.Replace(canonical, `"min_count":1`, `"min_count":1,"unknown":true`, 1),
		strings.Replace(canonical, `"request_id_field":"request_id",`, "", 1),
		strings.Replace(canonical, `"result_fields":{`, `"result_fields":{"url":"other","url":"url",`, 1),
		strings.Replace(canonical, `"fields":{"input_tokens":"input_tokens",`, `"fields":{"input_tokens":"other","input_tokens":"input_tokens",`, 1),
		strings.Replace(canonical, `"version":1`, `"version":1,"version":1`, 1),
		tamperedHash,
	}
	for index, raw := range cases {
		_, _, err := ParseZTAPIImageProtocolContract(raw)
		require.Error(t, err)
		if index == len(cases)-1 {
			require.Contains(t, err.Error(), "hash mismatch")
		}
	}
}

func TestZTAPIImageProtocolContractRejectsInvalidAndAmbiguousContracts(t *testing.T) {
	tests := []func(*ZTAPIImageProtocolContract){
		func(c *ZTAPIImageProtocolContract) { c.ProviderModel = "" },
		func(c *ZTAPIImageProtocolContract) { c.EndpointType = "chat" },
		func(c *ZTAPIImageProtocolContract) { c.Method = "GET" },
		func(c *ZTAPIImageProtocolContract) { c.Path = "/v1/images/*" },
		func(c *ZTAPIImageProtocolContract) { c.Capabilities.Sizes = nil },
		func(c *ZTAPIImageProtocolContract) { c.Capabilities.Qualities = []string{"standard", "standard"} },
		func(c *ZTAPIImageProtocolContract) { c.Capabilities.ResponseFormats = []string{"json"} },
		func(c *ZTAPIImageProtocolContract) { c.Capabilities.ResponseFormats = []string{"url"} },
		func(c *ZTAPIImageProtocolContract) { c.Response.ResultFields = map[string]string{"url": "url"} },
		func(c *ZTAPIImageProtocolContract) { c.Capabilities.MinCount = 0 },
		func(c *ZTAPIImageProtocolContract) { c.Capabilities.MaxCount = 11 },
		func(c *ZTAPIImageProtocolContract) { c.Capabilities.SupportsEdits = true },
		func(c *ZTAPIImageProtocolContract) {
			c.Response.ResultFields = map[string]string{"url": "value", "b64_json": "value"}
		},
		func(c *ZTAPIImageProtocolContract) {
			c.Response.ResultFields = map[string]string{"url": "value", "b64_json": "value.child"}
		},
		func(c *ZTAPIImageProtocolContract) { c.Response.ResultsField = "" },
		func(c *ZTAPIImageProtocolContract) { c.Usage.UsageField = "" },
		func(c *ZTAPIImageProtocolContract) { c.Usage.TotalField = "input_tokens" },
		func(c *ZTAPIImageProtocolContract) { c.Usage.TotalField = "input_tokens.value" },
		func(c *ZTAPIImageProtocolContract) { c.RequestIDField = "data" },
		func(c *ZTAPIImageProtocolContract) { c.RequestIDField = "data.id" },
		func(c *ZTAPIImageProtocolContract) { c.Usage.CacheSemantics = "included_in_input" },
		func(c *ZTAPIImageProtocolContract) { c.Usage.TotalSemantics = "provider_total" },
		func(c *ZTAPIImageProtocolContract) { c.EvidenceVersion = 2 },
	}
	for index, mutate := range tests {
		contract := syntheticZTAPIImageProtocolContract()
		mutate(&contract)
		_, _, err := SealZTAPIImageProtocolContract(contract)
		require.Errorf(t, err, "case %d must fail", index)
	}
}

func TestZTAPIImageProtocolContractCloneDeepCopiesMutableFields(t *testing.T) {
	original := syntheticZTAPIImageProtocolContract()
	clone := original.Clone()
	clone.Capabilities.Sizes[0] = "2048x2048"
	clone.Capabilities.Qualities[0] = "draft"
	clone.Capabilities.ResponseFormats[0] = "b64_json"
	clone.Response.ResultFields["url"] = "changed"
	clone.Usage.Fields["input_tokens"] = "changed"
	clone.Reservations[0].MaximumDimensions["input_tokens"] = "1"
	require.Equal(t, "1024x1024", original.Capabilities.Sizes[0])
	require.Equal(t, "standard", original.Capabilities.Qualities[0])
	require.Equal(t, "url", original.Capabilities.ResponseFormats[0])
	require.Equal(t, "url", original.Response.ResultFields["url"])
	require.Equal(t, "input_tokens", original.Usage.Fields["input_tokens"])
	require.Equal(t, "200000", original.Reservations[0].MaximumDimensions["input_tokens"])
}

func TestZTAPIImageProtocolReservationAuthorityCoversEveryAdmittedSelectorExactlyOnce(t *testing.T) {
	sealed, _, err := SealZTAPIImageProtocolContract(syntheticZTAPIImageProtocolContract())
	require.NoError(t, err)
	require.Len(t, sealed.Reservations, 32)

	entry, ok := sealed.FindReservationAuthority(ZTAPIImageSelector{
		Size: "1024x1024", Quality: "high", ResponseFormat: "b64_json", N: 4,
	})
	require.True(t, ok)
	require.Equal(t, map[string]string{"input_tokens": "200000", "output_tokens": "4096"}, entry.MaximumDimensions)

	_, ok = sealed.FindReservationAuthority(ZTAPIImageSelector{
		Size: "2048x2048", Quality: "high", ResponseFormat: "b64_json", N: 4,
	})
	require.False(t, ok)
}

func TestZTAPIImageProtocolReservationAuthorityRejectsIncompleteDuplicateAndInvalidBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ZTAPIImageProtocolContract)
	}{
		{"missing selector", func(c *ZTAPIImageProtocolContract) { c.Reservations = c.Reservations[1:] }},
		{"duplicate selector", func(c *ZTAPIImageProtocolContract) { c.Reservations[1] = c.Reservations[0] }},
		{"unexpected dimension", func(c *ZTAPIImageProtocolContract) { c.Reservations[0].MaximumDimensions["other"] = "1" }},
		{"missing dimension", func(c *ZTAPIImageProtocolContract) { delete(c.Reservations[0].MaximumDimensions, "output_tokens") }},
		{"negative dimension", func(c *ZTAPIImageProtocolContract) { c.Reservations[0].MaximumDimensions["input_tokens"] = "-1" }},
		{"non canonical dimension", func(c *ZTAPIImageProtocolContract) { c.Reservations[0].MaximumDimensions["input_tokens"] = "0200000" }},
		{"zero dimensions", func(c *ZTAPIImageProtocolContract) {
			c.Reservations[0].MaximumDimensions["input_tokens"] = "0"
			c.Reservations[0].MaximumDimensions["output_tokens"] = "0"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := syntheticZTAPIImageProtocolContract()
			tt.mutate(&contract)
			_, _, err := SealZTAPIImageProtocolContract(contract)
			require.Error(t, err)
		})
	}
}
