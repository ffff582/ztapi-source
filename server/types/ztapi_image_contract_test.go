package types

import (
	"crypto/sha256"
	"fmt"
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
	require.Equal(t, "ba46ac00047227152d56e09ba4469e06f09f9528bb70a3dcc1a98639c9ef4cd2", sealed.EvidenceHash)

	parsed, reparsedCanonical, err := ParseZTAPIImageProtocolContract(canonical)
	require.NoError(t, err)
	require.Equal(t, sealed, parsed)
	require.Equal(t, canonical, reparsedCanonical)
	require.Equal(t, []string{"1024x1024", "512x512"}, parsed.Capabilities.Sizes)
	require.Equal(t, []string{"high", "standard"}, parsed.Capabilities.Qualities)
}

func TestZTAPIImageProtocolContractV1CanonicalJSONRemainsByteForByteCompatible(t *testing.T) {
	contract := syntheticZTAPIImageProtocolContract()
	contract.Capabilities = ZTAPIImageCapabilities{
		Sizes: []string{"512x512"}, Qualities: []string{"standard"}, ResponseFormats: []string{"url"}, MinCount: 1, MaxCount: 1,
	}
	contract.Response.ResultFields = map[string]string{"url": "url"}
	contract.Reservations = []ZTAPIImageReservationAuthority{{
		Size: "512x512", Quality: "standard", ResponseFormat: "url", N: 1,
		MaximumDimensions: map[string]string{"input_tokens": "200000", "output_tokens": "4096"},
	}}

	sealed, canonical, err := SealZTAPIImageProtocolContract(contract)
	require.NoError(t, err)
	const wantCanonical = `{"version":1,"provider_model":"synthetic-image-v1","endpoint_type":"images_generation","method":"POST","path":"/v1/images/generations","capabilities":{"sizes":["512x512"],"qualities":["standard"],"response_formats":["url"],"min_count":1,"max_count":1,"supports_edits":false},"response":{"schema":"object_results_array","results_field":"data","result_fields":{"url":"url"}},"usage":{"usage_field":"usage","fields":{"input_tokens":"input_tokens","output_tokens":"output_tokens"},"total_field":"total_tokens","total_semantics":"sum_of_dimensions","cache_semantics":"not_reported"},"reservations":[{"size":"512x512","quality":"standard","response_format":"url","n":1,"maximum_dimensions":{"input_tokens":"200000","output_tokens":"4096"}}],"request_id_field":"request_id","evidence_version":1,"evidence_hash":"264d99bdae383319d0a4716b50989d59e586160dd7b446cb57389d1b41c31165"}`
	require.Equal(t, wantCanonical, canonical)
	require.Equal(t, "264d99bdae383319d0a4716b50989d59e586160dd7b446cb57389d1b41c31165", sealed.EvidenceHash)
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

func TestZTAPIImageProtocolContractAcceptsExactlyOneExplicitResponseIDSource(t *testing.T) {
	for _, tc := range []struct {
		name        string
		source      string
		key         string
		wantField   string
		legacyField bool
	}{
		{"body field", "body_field", "meta.request_id", `"request_id_key":"meta.request_id"`, false},
		{"header", "header", "X-Request-ID", `"request_id_key":"X-Request-ID"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := ztapiImageResponseIDContractJSON(t, `"request_id_source":"`+tc.source+`","request_id_key":"`+tc.key+`"`)

			_, canonical, err := ParseZTAPIImageProtocolContract(raw)
			require.NoError(t, err)
			require.Contains(t, canonical, `"request_id_source":"`+tc.source+`"`)
			require.Contains(t, canonical, tc.wantField)
			require.NotContains(t, canonical, `"request_id_field"`)
		})
	}
}

func TestZTAPIImageProtocolContractRejectsInvalidResponseIDSources(t *testing.T) {
	for _, tc := range []struct {
		name          string
		requestIDJSON string
	}{
		{"empty source", ""},
		{"legacy and explicit source", `"request_id_field":"request_id","request_id_source":"header","request_id_key":"X-Request-ID"`},
		{"invalid body path", `"request_id_source":"body_field","request_id_key":"meta..request_id"`},
		{"invalid header name", `"request_id_source":"header","request_id_key":"X Request ID"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseZTAPIImageProtocolContract(ztapiImageResponseIDContractJSON(t, tc.requestIDJSON))
			require.ErrorContains(t, err, "response-ID source")
		})
	}
}

func TestZTAPIImageProtocolContractRejectsWrongVersionResponseIDFieldsByPresence(t *testing.T) {
	_, v1, err := SealZTAPIImageProtocolContract(syntheticZTAPIImageProtocolContract())
	require.NoError(t, err)
	v2 := ztapiImageResponseIDContractJSON(t, `"request_id_source":"header","request_id_key":"X-Request-ID"`)

	for _, tc := range []struct {
		name      string
		raw       string
		marker    string
		forbidden string
		value     string
	}{
		{"v2 legacy empty", v2, `"request_id_source":`, "request_id_field", `""`},
		{"v2 legacy null", v2, `"request_id_source":`, "request_id_field", `null`},
		{"v1 source empty", v1, `"request_id_field":`, "request_id_source", `""`},
		{"v1 source null", v1, `"request_id_field":`, "request_id_source", `null`},
		{"v1 key empty", v1, `"request_id_field":`, "request_id_key", `""`},
		{"v1 key null", v1, `"request_id_field":`, "request_id_key", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(tc.raw, tc.marker, `"`+tc.forbidden+`":`+tc.value+`,`+tc.marker, 1)
			require.NotEqual(t, tc.raw, raw)

			_, _, err := ParseZTAPIImageProtocolContract(raw)
			require.ErrorContains(t, err, tc.forbidden)
		})
	}
}

func ztapiImageResponseIDContractJSON(t *testing.T, requestIDJSON string) string {
	t.Helper()
	_, canonical, err := SealZTAPIImageProtocolContract(syntheticZTAPIImageProtocolContract())
	require.NoError(t, err)

	payload := strings.Replace(canonical, `"version":1,`, `"version":2,`, 1)
	replacement := requestIDJSON
	if replacement != "" {
		replacement += ","
	}
	payload = strings.Replace(payload, `"request_id_field":"request_id",`, replacement, 1)
	hashIndex := strings.LastIndex(payload, `,"evidence_hash":`)
	require.NotEqual(t, -1, hashIndex)
	payload = payload[:hashIndex] + "}"
	sum := sha256.Sum256([]byte(payload))
	return payload[:len(payload)-1] + fmt.Sprintf(`,"evidence_hash":"%x"}`, sum)
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
