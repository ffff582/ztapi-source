package common

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	base "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newZTAPIHealthProbePinContext(t *testing.T) (*gin.Context, *ZTAPIHealthProbePin) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set(base.RequestIdKey, "ztapi-health:case-1:1")
	c.Request.Header.Set("X-Request-ID", "ztapi-health:case-1:1")
	pin := &ZTAPIHealthProbePin{
		CaseID: "case-1", LeaseToken: "lease-1", RequestID: "ztapi-health:case-1:1",
		ModelID: 8, PublicModel: "zt-model", ChannelID: 9, EntryProtocol: "chat", Protocol: "chat",
		CredentialVersion: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Generation: 3,
	}
	SetZTAPIHealthProbePin(c, pin)
	return c, pin
}

func TestZTAPIHealthProbePinBindsDurableTicketAndPreservesRequestID(t *testing.T) {
	c, pin := newZTAPIHealthProbePinContext(t)
	info := &RelayInfo{OriginModelName: pin.PublicModel, UserId: 42, IsStream: pin.Stream, ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{PublicName: pin.PublicModel}}
	session, err := StartZTAPIHealthRequest(c, info, ZTAPIHealthBackend{
		AdmitRequest: func(_ context.Context, modelName, executionID, requestID string, userID int, stream bool) (*types.ZTAPIHealthTicket, error) {
			require.Equal(t, pin.PublicModel, modelName)
			require.NotEmpty(t, executionID)
			require.Equal(t, pin.RequestID, requestID)
			return &types.ZTAPIHealthTicket{
				ExecutionID: executionID, RequestID: requestID, ModelID: pin.ModelID,
				Generation: pin.Generation, PublicModel: modelName, EntryProtocol: pin.EntryProtocol, Stream: stream, Source: "probe",
			}, nil
		},
		RecordOutcome: func(context.Context, *types.ZTAPIHealthTicket, types.ZTAPIHealthOutcome) error { return nil },
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Equal(t, pin.RequestID, info.RequestId)
	require.Equal(t, pin.RequestID, c.GetString(base.RequestIdKey))
	require.Equal(t, pin.RequestID, c.GetHeader("X-Request-ID"))
}

func TestZTAPIHealthProbePinRejectsWrongRequestBeforeDurableAdmission(t *testing.T) {
	for name, mutate := range map[string]func(*RelayInfo){
		"model":  func(info *RelayInfo) { info.OriginModelName = "another-model" },
		"stream": func(info *RelayInfo) { info.IsStream = !info.IsStream },
	} {
		t.Run(name, func(t *testing.T) {
			c, pin := newZTAPIHealthProbePinContext(t)
			info := &RelayInfo{OriginModelName: pin.PublicModel, UserId: 42, IsStream: pin.Stream}
			mutate(info)
			admitted := false
			_, err := StartZTAPIHealthRequest(c, info, ZTAPIHealthBackend{AdmitRequest: func(context.Context, string, string, string, int, bool) (*types.ZTAPIHealthTicket, error) {
				admitted = true
				return nil, nil
			}})
			require.Error(t, err)
			require.False(t, admitted)
		})
	}
}

func TestZTAPIHealthProbePinRejectsMismatchedDurableTicket(t *testing.T) {
	c, pin := newZTAPIHealthProbePinContext(t)
	info := &RelayInfo{OriginModelName: pin.PublicModel, UserId: 42, IsStream: pin.Stream}
	_, err := StartZTAPIHealthRequest(c, info, ZTAPIHealthBackend{
		AdmitRequest: func(_ context.Context, _, executionID, requestID string, _ int, stream bool) (*types.ZTAPIHealthTicket, error) {
			return &types.ZTAPIHealthTicket{ExecutionID: executionID, RequestID: requestID, ModelID: pin.ModelID + 1, Generation: pin.Generation, PublicModel: pin.PublicModel, Stream: stream, Source: "probe"}, nil
		},
	})
	require.Error(t, err)
}

func TestZTAPIHealthProbePinRevalidatesFingerprintBeforeAttemptAdmission(t *testing.T) {
	c, pin := newZTAPIHealthProbePinContext(t)
	info := &RelayInfo{OriginModelName: pin.PublicModel, UserId: 42, IsStream: pin.Stream}
	validated := false
	admitted := false
	backend := ZTAPIHealthBackend{
		AdmitRequest: func(_ context.Context, _, executionID, requestID string, _ int, stream bool) (*types.ZTAPIHealthTicket, error) {
			return &types.ZTAPIHealthTicket{ExecutionID: executionID, RequestID: requestID, ModelID: pin.ModelID, Generation: pin.Generation, PublicModel: pin.PublicModel, EntryProtocol: pin.EntryProtocol, Stream: stream, Source: "probe"}, nil
		},
		ValidateProbeRoute: func(_ context.Context, check ZTAPIHealthProbeRouteCheck) error {
			validated = true
			require.Equal(t, pin.CaseID, check.CaseID)
			require.Equal(t, pin.ChannelID, check.ChannelID)
			require.Equal(t, pin.Protocol, check.Protocol)
			require.NotEqual(t, "actual-upstream-secret", check.CredentialVersion)
			require.Len(t, check.CredentialVersion, 64)
			return errors.New("route changed")
		},
		AdmitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string, string) error {
			admitted = true
			return nil
		},
	}
	_, err := StartZTAPIHealthRequest(c, info, backend)
	require.NoError(t, err)
	attempt, err := BeginZTAPIHealthUpstream(c, pin.ChannelID, "/v1/chat/completions", "actual-upstream-secret")
	require.Nil(t, attempt)
	require.Error(t, err)
	apiErr, ok := err.(*types.NewAPIError)
	require.True(t, ok)
	require.True(t, types.IsSkipRetryError(apiErr))
	require.True(t, validated)
	require.False(t, admitted)
}

func TestZTAPIHealthProbePinLeavesOrdinaryHealthSessionBehaviorUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	session, err := StartZTAPIHealthRequest(c, &RelayInfo{OriginModelName: "zt-model", UserId: 7}, ZTAPIHealthBackend{
		AdmitRequest: func(_ context.Context, _, executionID, requestID string, _ int, stream bool) (*types.ZTAPIHealthTicket, error) {
			return &types.ZTAPIHealthTicket{ExecutionID: executionID, RequestID: requestID, ModelID: 8, Generation: 3, PublicModel: "zt-model", Stream: stream, Source: "real"}, nil
		},
		AdmitAttempt: func(context.Context, *types.ZTAPIHealthTicket, int, string, string) error { return nil },
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	attempt, err := BeginZTAPIHealthUpstream(c, 9, "/v1/chat/completions", "ordinary-secret")
	require.NoError(t, err)
	require.NotNil(t, attempt)
}
