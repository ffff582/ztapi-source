package service

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPIVideoFetchHealthRecordsRecognizedProviderState(t *testing.T) {
	var recorded types.ZTAPIHealthOutcome
	backend := ztapiMediaHealthBackend{
		admitMediaRequest: func(context.Context, string, string, string, int, string, bool) (*types.ZTAPIHealthTicket, error) {
			return &types.ZTAPIHealthTicket{ExecutionID: "fetch-1", Modality: model.ZTAPIModalityVideo, Operation: types.ZTAPIHealthOperationVideoFetch, Source: "real"}, nil
		},
		admitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string) error { return nil },
		recordOutcome: func(_ context.Context, _ *types.ZTAPIHealthTicket, outcome types.ZTAPIHealthOutcome) error {
			recorded = outcome
			return nil
		},
	}
	health := beginZTAPIVideoFetchHealth(context.Background(), backend, &model.ZTAPIMediaTask{
		PublicTaskID: "public-task-1", PublicModel: "zt-video", UserID: 7, ChannelID: 9, UpstreamTaskID: "provider-task-1",
	})
	health.finish(200, &relaycommon.TaskInfo{
		Status: string(model.TaskStatusInProgress), ProviderStatus: "processing", UpstreamRequestID: "provider-request-1",
	})
	require.Equal(t, "success", recorded.Result)
	require.Equal(t, "valid_output", recorded.Reason)
	require.Equal(t, types.ZTAPIHealthOperationVideoFetch, recorded.Operation)
	require.Equal(t, "video-tasks", recorded.UpstreamProtocol)
	require.Equal(t, "provider-task-1", recorded.UpstreamTaskID)
	require.Equal(t, "provider-request-1", recorded.UpstreamRequestID)
	require.Equal(t, "processing", recorded.TerminalStatus)
	require.True(t, recorded.ResultValid)
}

func TestZTAPIVideoFetchHealthExcludesCustomerTaskFailureButCountsProbeFailure(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   string
	}{{source: "real", want: "excluded"}, {source: "probe", want: "failure"}} {
		t.Run(tc.source, func(t *testing.T) {
			var recorded types.ZTAPIHealthOutcome
			backend := ztapiMediaHealthBackend{
				admitMediaRequest: func(context.Context, string, string, string, int, string, bool) (*types.ZTAPIHealthTicket, error) {
					return &types.ZTAPIHealthTicket{ExecutionID: "fetch-2", Modality: model.ZTAPIModalityVideo, Operation: types.ZTAPIHealthOperationVideoFetch, Source: tc.source}, nil
				},
				admitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string) error { return nil },
				recordOutcome: func(_ context.Context, _ *types.ZTAPIHealthTicket, outcome types.ZTAPIHealthOutcome) error {
					recorded = outcome
					return nil
				},
			}
			health := beginZTAPIVideoFetchHealth(context.Background(), backend, &model.ZTAPIMediaTask{
				PublicTaskID: "public-task-2", PublicModel: "zt-video", UserID: 7, ChannelID: 9, UpstreamTaskID: "provider-task-2",
			})
			health.finish(200, &relaycommon.TaskInfo{Status: string(model.TaskStatusFailure), ProviderStatus: "content_filtered"})
			require.Equal(t, tc.want, recorded.Result)
			require.Equal(t, "provider_task_failed", recorded.Reason)
		})
	}
}

func TestZTAPIVideoFetchHealthAdmissionFailureNeverBlocksAcceptedTask(t *testing.T) {
	backend := ztapiMediaHealthBackend{
		admitMediaRequest: func(context.Context, string, string, string, int, string, bool) (*types.ZTAPIHealthTicket, error) {
			return nil, errors.New("health storage unavailable")
		},
	}
	health := beginZTAPIVideoFetchHealth(context.Background(), backend, &model.ZTAPIMediaTask{
		PublicTaskID: "public-task-3", PublicModel: "zt-video", UserID: 7, ChannelID: 9, UpstreamTaskID: "provider-task-3",
	})
	require.NotNil(t, health)
	require.NotPanics(t, func() { health.transportFailure() })
}

func TestZTAPIVideoPollingPathEmitsHealthObservation(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	f.legacy.Status = model.TaskStatusSubmitted
	require.NoError(t, f.legacy.Insert())
	var recorded types.ZTAPIHealthOutcome
	previous := productionZTAPIMediaHealthBackend
	productionZTAPIMediaHealthBackend = ztapiMediaHealthBackend{
		admitMediaRequest: func(_ context.Context, modelName, _, requestID string, userID int, operation string, allowUnavailable bool) (*types.ZTAPIHealthTicket, error) {
			require.Equal(t, f.info.OriginModelName, modelName)
			require.Equal(t, f.legacy.TaskID, requestID)
			require.Equal(t, f.user.Id, userID)
			require.Equal(t, types.ZTAPIHealthOperationVideoFetch, operation)
			require.True(t, allowUnavailable)
			return &types.ZTAPIHealthTicket{ExecutionID: "polling-path", Modality: model.ZTAPIModalityVideo, Operation: operation, Source: "real"}, nil
		},
		admitAttempt: func(_ context.Context, _ *types.ZTAPIHealthTicket, channelID int, protocol string) error {
			require.Equal(t, f.legacy.ChannelId, channelID)
			require.Equal(t, "video-tasks", protocol)
			return nil
		},
		recordOutcome: func(_ context.Context, _ *types.ZTAPIHealthTicket, outcome types.ZTAPIHealthOutcome) error {
			recorded = outcome
			return nil
		},
	}
	t.Cleanup(func() { productionZTAPIMediaHealthBackend = previous })

	baseURL := "https://aihub.example.test"
	adaptor := &capturingMediaPollingAdaptor{result: &relaycommon.TaskInfo{
		Status: string(model.TaskStatusInProgress), ProviderStatus: "processing", UpstreamRequestID: "fetch-request-path",
	}}
	channel := &model.Channel{Id: f.legacy.ChannelId, Key: "provider-secret", BaseURL: &baseURL}
	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, channel, f.legacy.GetUpstreamTaskID(), map[string]*model.Task{
		f.legacy.GetUpstreamTaskID(): &f.legacy,
	}))
	require.Equal(t, "success", recorded.Result)
	require.Equal(t, "fetch-request-path", recorded.UpstreamRequestID)
	require.Equal(t, f.legacy.GetUpstreamTaskID(), recorded.UpstreamTaskID)
}
