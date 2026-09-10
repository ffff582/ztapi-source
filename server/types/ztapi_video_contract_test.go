package types

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIVideoProtocolContractSealsAndParsesExactEvidence(t *testing.T) {
	contract := ztapiVideoProtocolContractFixture()
	sealed, canonical, err := SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	require.Len(t, sealed.EvidenceHash, 64)
	require.Equal(t, "45e6fec6cf29408ef9ca9339c200f8b7f33e56ed904217e185b7368f8a30e53e", sealed.EvidenceHash)

	parsed, reparsed, err := ParseZTAPIVideoProtocolContract(canonical)
	require.NoError(t, err)
	require.Equal(t, canonical, reparsed)
	require.Equal(t, sealed, parsed)
	require.Equal(t, "provider-video-exact", parsed.ProviderModel)
	require.Equal(t, "/hub/v1/video/tasks/{task_id}", parsed.Fetch.Path)
	require.Equal(t, "Bearer", parsed.Auth.Scheme)
	require.Equal(t, "data.status", parsed.States.Field)
	require.Equal(t, "data.result.url", parsed.Result.URLField)
	require.Equal(t, "data.usage.credits", parsed.Usage.Fields["credits"])

	authority, ok := parsed.FindReservationAuthority(ZTAPIVideoSelector{
		Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false,
	})
	require.True(t, ok)
	require.Equal(t, "9000", authority.MaximumDimensions["credits"])
}

func TestZTAPIVideoProtocolContractV1CanonicalJSONRemainsByteForByteCompatible(t *testing.T) {
	contract := ztapiVideoProtocolContractFixture()
	contract.Request.VideoInputField = ""
	contract.Capabilities = ZTAPIVideoCapabilities{
		Resolutions: []string{"720p"}, DurationSeconds: []int{5}, SupportsVideoInput: false,
	}
	contract.Reservations = []ZTAPIVideoReservationAuthority{{
		Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false, MaximumDimensions: map[string]string{"credits": "9000"},
	}}

	sealed, canonical, err := SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	const wantCanonical = `{"version":1,"provider":"aihub","provider_model":"provider-video-exact","auth":{"method":"header","header":"Authorization","scheme":"Bearer"},"create":{"method":"POST","path":"/hub/v1/video/tasks","task_id_field":"data.task_id","request_id_field":"request_id"},"fetch":{"method":"GET","path":"/hub/v1/video/tasks/{task_id}","task_id_field":"data.task_id","request_id_field":"request_id"},"callback":{"enabled":false},"request":{"model_field":"model","prompt_field":"prompt","resolution_field":"resolution","duration_field":"duration"},"capabilities":{"resolutions":["720p"],"duration_seconds":[5],"supports_video_input":false},"states":{"field":"data.status","accepted":["queued"],"processing":["processing"],"succeeded":["completed"],"failed":["failed"]},"result":{"url_field":"data.result.url","resolution_field":"data.result.resolution","duration_field":"data.result.duration","failure_reason_field":"data.error.message"},"usage":{"fields":{"credits":"data.usage.credits"}},"reservations":[{"resolution":"720p","duration_seconds":5,"contains_video_input":false,"maximum_dimensions":{"credits":"9000"}}],"evidence_version":1,"evidence_hash":"5340014b7a0a66d1b34af91958cf20cfde1901fea1c08ff951b920dcf56a926e"}`
	require.Equal(t, wantCanonical, canonical)
	require.Equal(t, "5340014b7a0a66d1b34af91958cf20cfde1901fea1c08ff951b920dcf56a926e", sealed.EvidenceHash)
}

func TestZTAPIVideoProtocolContractRejectsUnprovedOrAmbiguousContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ZTAPIVideoProtocolContract)
	}{
		{"empty model", func(c *ZTAPIVideoProtocolContract) { c.ProviderModel = "" }},
		{"wildcard model", func(c *ZTAPIVideoProtocolContract) { c.ProviderModel = "doubao-*" }},
		{"wrong auth", func(c *ZTAPIVideoProtocolContract) { c.Auth.Scheme = "Token" }},
		{"relative create path", func(c *ZTAPIVideoProtocolContract) { c.Create.Path = "hub/v1/video/tasks" }},
		{"missing fetch placeholder", func(c *ZTAPIVideoProtocolContract) { c.Fetch.Path = "/hub/v1/video/tasks" }},
		{"malformed accepted state", func(c *ZTAPIVideoProtocolContract) { c.States.Accepted = append(c.States.Accepted, "mystery state") }},
		{"overlapping terminal state", func(c *ZTAPIVideoProtocolContract) { c.States.Failed = append(c.States.Failed, "completed") }},
		{"missing result url", func(c *ZTAPIVideoProtocolContract) { c.Result.URLField = "" }},
		{"missing provider request id", func(c *ZTAPIVideoProtocolContract) { c.Create.RequestIDField = "" }},
		{"missing usage", func(c *ZTAPIVideoProtocolContract) { c.Usage.Fields = nil }},
		{"incomplete reservation", func(c *ZTAPIVideoProtocolContract) { c.Reservations = c.Reservations[:1] }},
		{"duplicate reservation", func(c *ZTAPIVideoProtocolContract) { c.Reservations[1] = c.Reservations[0] }},
		{"unbounded reservation", func(c *ZTAPIVideoProtocolContract) {
			c.Reservations[0].MaximumDimensions = map[string]string{"credits": "0"}
		}},
		{"guessed callback", func(c *ZTAPIVideoProtocolContract) { c.Callback.Enabled = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contract := ztapiVideoProtocolContractFixture()
			tc.mutate(&contract)
			_, _, err := SealZTAPIVideoProtocolContract(contract)
			require.Error(t, err)
		})
	}
}

func TestZTAPIVideoProtocolContractAcceptsExactlyOneExplicitResponseIDSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		key    string
	}{
		{"body field", "body_field", "data.request_id"},
		{"header", "header", "X-Request-ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := ztapiVideoResponseIDContractJSON(t, `"request_id_source":"`+tc.source+`","request_id_key":"`+tc.key+`"`)

			_, canonical, err := ParseZTAPIVideoProtocolContract(raw)
			require.NoError(t, err)
			require.Contains(t, canonical, `"request_id_source":"`+tc.source+`"`)
			require.Contains(t, canonical, `"request_id_key":"`+tc.key+`"`)
			require.NotContains(t, canonical, `"request_id_field"`)
		})
	}
}

func TestZTAPIVideoProtocolContractRejectsInvalidResponseIDSources(t *testing.T) {
	for _, tc := range []struct {
		name          string
		requestIDJSON string
	}{
		{"empty source", ""},
		{"legacy and explicit source", `"request_id_field":"request_id","request_id_source":"header","request_id_key":"X-Request-ID"`},
		{"invalid body path", `"request_id_source":"body_field","request_id_key":"data..request_id"`},
		{"invalid header name", `"request_id_source":"header","request_id_key":"X Request ID"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseZTAPIVideoProtocolContract(ztapiVideoResponseIDContractJSON(t, tc.requestIDJSON))
			require.ErrorContains(t, err, "response-ID source")
		})
	}
}

func TestZTAPIVideoProtocolContractRejectsWrongVersionResponseIDFieldsByPresence(t *testing.T) {
	_, v1, err := SealZTAPIVideoProtocolContract(ztapiVideoProtocolContractFixture())
	require.NoError(t, err)
	v2 := ztapiVideoResponseIDContractJSON(t, `"request_id_source":"header","request_id_key":"X-Request-ID"`)

	for _, endpoint := range []struct {
		name       string
		occurrence int
	}{
		{"create", 0},
		{"fetch", 1},
	} {
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
			t.Run(endpoint.name+" "+tc.name, func(t *testing.T) {
				raw := insertZTAPIVideoJSONField(t, tc.raw, tc.marker, endpoint.occurrence, tc.forbidden, tc.value)

				_, _, err := ParseZTAPIVideoProtocolContract(raw)
				require.ErrorContains(t, err, tc.forbidden)
			})
		}
	}
}

func insertZTAPIVideoJSONField(t *testing.T, raw, marker string, occurrence int, field, value string) string {
	t.Helper()
	index := -1
	remaining := raw
	offset := 0
	for current := 0; current <= occurrence; current++ {
		relative := strings.Index(remaining, marker)
		require.NotEqual(t, -1, relative)
		index = offset + relative
		offset = index + len(marker)
		remaining = raw[offset:]
	}
	return raw[:index] + `"` + field + `":` + value + `,` + raw[index:]
}

func ztapiVideoResponseIDContractJSON(t *testing.T, requestIDJSON string) string {
	t.Helper()
	_, canonical, err := SealZTAPIVideoProtocolContract(ztapiVideoProtocolContractFixture())
	require.NoError(t, err)

	payload := strings.Replace(canonical, `"version":1,`, `"version":2,`, 1)
	replacement := `"task_id_field":"data.task_id"`
	if requestIDJSON != "" {
		replacement += "," + requestIDJSON
	}
	payload = strings.ReplaceAll(payload, `"task_id_field":"data.task_id","request_id_field":"request_id"`, replacement)
	hashIndex := strings.LastIndex(payload, `,"evidence_hash":`)
	require.NotEqual(t, -1, hashIndex)
	payload = payload[:hashIndex] + "}"
	sum := sha256.Sum256([]byte(payload))
	return payload[:len(payload)-1] + fmt.Sprintf(`,"evidence_hash":"%x"}`, sum)
}

func TestZTAPIVideoProtocolContractRejectsUnknownDuplicateAndTamperedJSON(t *testing.T) {
	_, canonical, err := SealZTAPIVideoProtocolContract(ztapiVideoProtocolContractFixture())
	require.NoError(t, err)

	unknown := canonical[:len(canonical)-1] + `,"guessed_field":"no"}`
	_, _, err = ParseZTAPIVideoProtocolContract(unknown)
	require.ErrorContains(t, err, "unknown video protocol contract field")

	duplicate := `{"version":1,"version":1}`
	_, _, err = ParseZTAPIVideoProtocolContract(duplicate)
	require.ErrorContains(t, err, "duplicate video protocol contract field")

	tampered := []byte(canonical)
	for i := range tampered {
		if tampered[i] == '9' {
			tampered[i] = '8'
			break
		}
	}
	_, _, err = ParseZTAPIVideoProtocolContract(string(tampered))
	require.ErrorContains(t, err, "evidence hash mismatch")
}

func TestZTAPIVideoMaximumReservationUsesExactFrozenPriceAndUsageContract(t *testing.T) {
	protocol := ztapiVideoProtocolContractFixture()
	protocol.Usage.Fields = map[string]string{"input_tokens": "data.usage.input_tokens"}
	for index := range protocol.Reservations {
		value := protocol.Reservations[index].MaximumDimensions["credits"]
		protocol.Reservations[index].MaximumDimensions = map[string]string{"input_tokens": value}
	}
	sealed, _, err := SealZTAPIVideoProtocolContract(protocol)
	require.NoError(t, err)
	price, err := ParseZTAPIMediaPriceContract(`{
		"version":1,"modality":"video","rules":[
			{"id":"without_video_input","conditions":{"contains_video_input":"false"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1"},"sale_usd":{"input_tokens":"1.6666666667"},"source_cells":{"input_tokens":"F5"}},
			{"id":"with_video_input","conditions":{"contains_video_input":"true"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1.5"},"sale_usd":{"input_tokens":"2.5"},"source_cells":{"input_tokens":"G5"}}
		]}`)
	require.NoError(t, err)

	maximum, quota, err := CalculateZTAPIVideoMaximumReservation(price, sealed, ZTAPIVideoSelector{
		Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false,
	}, "500000")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"input_tokens": "9000"}, maximum)
	require.EqualValues(t, 7500, quota)

	sealed.Usage.Fields = map[string]string{"credits": "data.usage.input_tokens"}
	_, _, err = CalculateZTAPIVideoMaximumReservation(price, sealed, ZTAPIVideoSelector{
		Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false,
	}, "500000")
	require.Error(t, err)
}

func ztapiVideoProtocolContractFixture() ZTAPIVideoProtocolContract {
	return ZTAPIVideoProtocolContract{
		Version:       ZTAPIVideoProtocolContractVersion,
		Provider:      "aihub",
		ProviderModel: "provider-video-exact",
		Auth: ZTAPIVideoAuthContract{
			Method: "header", Header: "Authorization", Scheme: "Bearer",
		},
		Create: ZTAPIVideoEndpointContract{
			Method: "POST", Path: "/hub/v1/video/tasks",
			TaskIDField: "data.task_id", RequestIDField: "request_id",
		},
		Fetch: ZTAPIVideoEndpointContract{
			Method: "GET", Path: "/hub/v1/video/tasks/{task_id}",
			TaskIDField: "data.task_id", RequestIDField: "request_id",
		},
		Callback: ZTAPIVideoCallbackContract{Enabled: false},
		Request: ZTAPIVideoRequestContract{
			ModelField: "model", PromptField: "prompt", ResolutionField: "resolution",
			DurationField: "duration", VideoInputField: "video_url",
		},
		Capabilities: ZTAPIVideoCapabilities{
			Resolutions: []string{"720p", "1080p"}, DurationSeconds: []int{5, 10}, SupportsVideoInput: true,
		},
		States: ZTAPIVideoStateContract{
			Field: "data.status", Accepted: []string{"queued"}, Processing: []string{"processing"},
			Succeeded: []string{"completed"}, Failed: []string{"failed"},
		},
		Result: ZTAPIVideoResultContract{
			URLField: "data.result.url", ResolutionField: "data.result.resolution", DurationField: "data.result.duration", FailureReasonField: "data.error.message",
		},
		Usage: ZTAPIVideoUsageContract{
			Fields: map[string]string{"credits": "data.usage.credits"},
		},
		Reservations: []ZTAPIVideoReservationAuthority{
			{Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false, MaximumDimensions: map[string]string{"credits": "9000"}},
			{Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: true, MaximumDimensions: map[string]string{"credits": "7000"}},
			{Resolution: "720p", DurationSeconds: 10, ContainsVideoInput: false, MaximumDimensions: map[string]string{"credits": "18000"}},
			{Resolution: "720p", DurationSeconds: 10, ContainsVideoInput: true, MaximumDimensions: map[string]string{"credits": "14000"}},
			{Resolution: "1080p", DurationSeconds: 5, ContainsVideoInput: false, MaximumDimensions: map[string]string{"credits": "15000"}},
			{Resolution: "1080p", DurationSeconds: 5, ContainsVideoInput: true, MaximumDimensions: map[string]string{"credits": "12000"}},
			{Resolution: "1080p", DurationSeconds: 10, ContainsVideoInput: false, MaximumDimensions: map[string]string{"credits": "30000"}},
			{Resolution: "1080p", DurationSeconds: 10, ContainsVideoInput: true, MaximumDimensions: map[string]string{"credits": "24000"}},
		},
		EvidenceVersion: ZTAPIVideoEvidenceVersion,
	}
}
