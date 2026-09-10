package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIVideoProtocolContractSealsAndParsesExactEvidence(t *testing.T) {
	contract := ztapiVideoProtocolContractFixture()
	sealed, canonical, err := SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	require.Len(t, sealed.EvidenceHash, 64)

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
