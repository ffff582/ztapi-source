package types

import (
	"errors"
	"net/http"
)

var ztapiSeedanceProviderModels = map[string]struct{}{
	"doubao-seedance-2.0":      {},
	"doubao-seedance-2-0-fast": {},
	"doubao-seedance-2-0-mini": {},
}

// BuildZTAPISeedanceProtocolContract returns the paid-verified AIHub surface.
// Wider resolutions, durations and video input stay closed until separately verified.
func BuildZTAPISeedanceProtocolContract(providerModel string) (ZTAPIVideoProtocolContract, string, error) {
	if _, ok := ztapiSeedanceProviderModels[providerModel]; !ok {
		return ZTAPIVideoProtocolContract{}, "", errors.New("Seedance provider model is not quotation-authorized")
	}
	contract := ZTAPIVideoProtocolContract{
		Version:       ZTAPIVideoProtocolContractVersionV2,
		Provider:      "aihub",
		ProviderModel: providerModel,
		Auth: ZTAPIVideoAuthContract{
			Method: "header", Header: "Authorization", Scheme: "Bearer",
		},
		Create: ZTAPIVideoEndpointContract{
			Method: http.MethodPost, Path: "/hub/v1/videos", TaskIDField: "id",
			RequestIDSource: ZTAPIResponseIDSourceHeader, RequestIDKey: "X-Request-Id",
		},
		Fetch: ZTAPIVideoEndpointContract{
			Method: http.MethodGet, Path: "/hub/v1/videos/{task_id}", TaskIDField: "id",
			RequestIDSource: ZTAPIResponseIDSourceHeader, RequestIDKey: "X-Request-Id",
		},
		Callback: ZTAPIVideoCallbackContract{Enabled: false},
		Request: ZTAPIVideoRequestContract{
			ModelField: "model", PromptField: "prompt",
			ResolutionField: "provider_options.volcengine.resolution",
			DurationField:   "provider_options.volcengine.duration",
		},
		Capabilities: ZTAPIVideoCapabilities{
			Resolutions: []string{"720p"}, DurationSeconds: []int{5}, SupportsVideoInput: false,
		},
		States: ZTAPIVideoStateContract{
			Field: "status", Accepted: []string{"queued"}, Processing: []string{"in_progress"},
			Succeeded: []string{"completed"}, Failed: []string{"failed"},
		},
		Result: ZTAPIVideoResultContract{
			URLField:        "provider_result.volcengine.content.video_url",
			ResolutionField: "provider_result.volcengine.resolution",
			DurationField:   "provider_result.volcengine.duration", FailureReasonField: "error.message",
		},
		Usage: ZTAPIVideoUsageContract{Fields: map[string]string{
			"input_tokens": "provider_result.volcengine.usage.completion_tokens",
		}},
		Reservations: []ZTAPIVideoReservationAuthority{{
			Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false,
			MaximumDimensions: map[string]string{"input_tokens": "250000"},
		}},
		EvidenceVersion: ZTAPIVideoEvidenceVersion,
	}
	return SealZTAPIVideoProtocolContract(contract)
}
