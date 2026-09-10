package relay

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/aihub"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPIVideoContractRegistryWiresDedicatedTaskAdaptor(t *testing.T) {
	require.IsType(t, &aihub.TaskAdaptor{}, GetTaskAdaptor(constant.TaskPlatformZTAPIAIHubVideo))
}

func TestZTAPIVideoContractRegistryBlocksAllQuotationCandidatesWithoutEvidence(t *testing.T) {
	registry := DefaultZTAPIVideoContractRegistry()
	for _, publicName := range []string{"seedance-2.0", "Seedance 2.0 Fast", "Seedance 2.0 Mini"} {
		_, err := registry.Resolve(publicName)
		require.ErrorIs(t, err, ErrZTAPIVideoContractUnavailable, publicName)
	}
}

func TestZTAPIVideoContractRegistryUsesOnlyExactPublicAndProviderNames(t *testing.T) {
	contract := ztapiVideoProtocolContractForRelayTest(t)
	registry, err := NewZTAPIVideoContractRegistry(map[string]string{"Seedance 2.0 Fast": contract})
	require.NoError(t, err)

	resolved, err := registry.Resolve("Seedance 2.0 Fast")
	require.NoError(t, err)
	require.Equal(t, "provider-video-exact", resolved.ProviderModel)

	for _, candidate := range []string{
		"seedance 2.0 fast", "Seedance 2.0", "doubao-*", "doubao-seedance-2-0-fast-260128",
		"dreamina-seedance-2.0-fast", "provider-video", "provider-video-exact-latest",
	} {
		_, err = registry.Resolve(candidate)
		require.ErrorIs(t, err, ErrZTAPIVideoContractUnavailable, candidate)
	}
}

func TestZTAPIManagedTaskDTOExposesOnlyPublicVideoProxy(t *testing.T) {
	task := &model.Task{
		TaskID: "task_public_video", Status: model.TaskStatusSuccess,
		PrivateData: model.TaskPrivateData{
			ZTAPIMediaManaged: true,
			ResultURL:         "https://private-provider.invalid/result.mp4",
		},
	}

	result := TaskModel2Dto(task)
	require.Equal(t, taskcommon.BuildProxyURL(task.TaskID), result.ResultURL)
	require.NotContains(t, result.ResultURL, "private-provider.invalid")
}

func ztapiVideoProtocolContractForRelayTest(t *testing.T) string {
	t.Helper()
	contract := types.ZTAPIVideoProtocolContract{
		Version: types.ZTAPIVideoProtocolContractVersion, Provider: "aihub", ProviderModel: "provider-video-exact",
		Auth:            types.ZTAPIVideoAuthContract{Method: "header", Header: "Authorization", Scheme: "Bearer"},
		Create:          types.ZTAPIVideoEndpointContract{Method: "POST", Path: "/hub/v1/video/tasks", TaskIDField: "data.task_id", RequestIDField: "request_id"},
		Fetch:           types.ZTAPIVideoEndpointContract{Method: "GET", Path: "/hub/v1/video/tasks/{task_id}", TaskIDField: "data.task_id", RequestIDField: "request_id"},
		Callback:        types.ZTAPIVideoCallbackContract{Enabled: false},
		Request:         types.ZTAPIVideoRequestContract{ModelField: "model", PromptField: "prompt", ResolutionField: "resolution", DurationField: "duration"},
		Capabilities:    types.ZTAPIVideoCapabilities{Resolutions: []string{"720p"}, DurationSeconds: []int{5}, SupportsVideoInput: false},
		States:          types.ZTAPIVideoStateContract{Field: "data.status", Accepted: []string{"queued"}, Processing: []string{"processing"}, Succeeded: []string{"completed"}, Failed: []string{"failed"}},
		Result:          types.ZTAPIVideoResultContract{URLField: "data.result.url", ResolutionField: "data.result.resolution", DurationField: "data.result.duration", FailureReasonField: "data.error.message"},
		Usage:           types.ZTAPIVideoUsageContract{Fields: map[string]string{"input_tokens": "data.usage.input_tokens"}},
		Reservations:    []types.ZTAPIVideoReservationAuthority{{Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false, MaximumDimensions: map[string]string{"input_tokens": "9000"}}},
		EvidenceVersion: types.ZTAPIVideoEvidenceVersion,
	}
	_, canonical, err := types.SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	return canonical
}
