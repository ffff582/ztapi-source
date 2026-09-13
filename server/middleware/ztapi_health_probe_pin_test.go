package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runZTAPIHealthProbePinMiddleware(
	t *testing.T,
	remoteAddr string,
	userID int,
	headers map[string]string,
	validator ztapiHealthProbePinValidator,
) (*httptest.ResponseRecorder, bool, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.RemoteAddr = remoteAddr
	c.Set("id", userID)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	c.Set(common.RequestIdKey, c.GetHeader("X-Request-ID"))
	called := false
	handler := ztapiHealthProbePinMiddleware(42, time.Now, validator)
	handler(c)
	if !c.IsAborted() {
		called = true
	}
	return recorder, called, c
}

func testZTAPIHealthProbePin() model.ZTAPIHealthVerificationPin {
	return model.ZTAPIHealthVerificationPin{
		CaseID: "case-1", LeaseToken: "lease-1", ProbeRequestID: "ztapi-health:case-1:1",
		ModelID: 8, PublicModel: "zt-model", ChannelID: 9, Protocol: "chat", Stream: false,
		CredentialVersion: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Generation: 3,
	}
}

func TestZTAPIHealthProbePinMiddlewareAllowsOrdinaryRequests(t *testing.T) {
	_, called, c := runZTAPIHealthProbePinMiddleware(t, "198.51.100.4:1234", 7, map[string]string{
		"X-Request-ID": "ordinary-request",
	}, func(context.Context, string, string, string, time.Time) (model.ZTAPIHealthVerificationPin, error) {
		t.Fatal("ordinary request must not touch verification storage")
		return model.ZTAPIHealthVerificationPin{}, nil
	})
	require.True(t, called)
	require.Nil(t, relaycommon.GetZTAPIHealthProbePin(c))
}

func TestZTAPIHealthProbePinMiddlewareRejectsPartialForgedAndWrongUserPins(t *testing.T) {
	complete := map[string]string{
		ZTAPIHealthProbeCaseHeader:  "case-1",
		ZTAPIHealthProbeLeaseHeader: "lease-1",
		"X-Request-ID":              "ztapi-health:case-1:1",
	}
	for name, setup := range map[string]func() (string, int, map[string]string){
		"partial": func() (string, int, map[string]string) {
			return "127.0.0.1:1234", 42, map[string]string{ZTAPIHealthProbeCaseHeader: "case-1", "X-Request-ID": "ztapi-health:case-1:1"}
		},
		"non loopback": func() (string, int, map[string]string) { return "198.51.100.4:1234", 42, complete },
		"wrong user":   func() (string, int, map[string]string) { return "127.0.0.1:1234", 7, complete },
	} {
		t.Run(name, func(t *testing.T) {
			remote, userID, headers := setup()
			calledValidator := false
			recorder, called, c := runZTAPIHealthProbePinMiddleware(t, remote, userID, headers,
				func(context.Context, string, string, string, time.Time) (model.ZTAPIHealthVerificationPin, error) {
					calledValidator = true
					return testZTAPIHealthProbePin(), nil
				})
			require.False(t, called)
			require.False(t, calledValidator)
			require.Equal(t, http.StatusForbidden, recorder.Code)
			require.Empty(t, c.Request.Header.Get(ZTAPIHealthProbeCaseHeader))
			require.Empty(t, c.Request.Header.Get(ZTAPIHealthProbeLeaseHeader))
		})
	}
}

func TestZTAPIHealthProbePinMiddlewarePinsExactChannelAndScrubsHeaders(t *testing.T) {
	pin := testZTAPIHealthProbePin()
	headers := map[string]string{
		ZTAPIHealthProbeCaseHeader: pin.CaseID, ZTAPIHealthProbeLeaseHeader: pin.LeaseToken,
		"X-Request-ID": pin.ProbeRequestID,
	}
	_, called, c := runZTAPIHealthProbePinMiddleware(t, "[::1]:1234", 42, headers,
		func(_ context.Context, caseID, leaseToken, requestID string, _ time.Time) (model.ZTAPIHealthVerificationPin, error) {
			require.Equal(t, pin.CaseID, caseID)
			require.Equal(t, pin.LeaseToken, leaseToken)
			require.Equal(t, pin.ProbeRequestID, requestID)
			return pin, nil
		})
	require.True(t, called)
	require.Equal(t, "9", common.GetContextKeyString(c, constant.ContextKeyTokenSpecificChannelId))
	require.Equal(t, pin.CredentialVersion, common.GetContextKeyString(c, constant.ContextKeyZTAPIHealthCredentialPin))
	require.Equal(t, pin.ProbeRequestID, c.GetHeader("X-Request-ID"))
	require.Empty(t, c.Request.Header.Get(ZTAPIHealthProbeCaseHeader))
	require.Empty(t, c.Request.Header.Get(ZTAPIHealthProbeLeaseHeader))
	bound := relaycommon.GetZTAPIHealthProbePin(c)
	require.NotNil(t, bound)
	require.Equal(t, pin.CredentialVersion, bound.CredentialVersion)
}

func TestZTAPIHealthProbePinSelectsTheExactMultiKeyCredential(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel := &model.Channel{
		Key: "first-key\nsecond-key",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
			MultiKeyStatusList: map[int]int{
				0: common.ChannelStatusEnabled,
				1: common.ChannelStatusEnabled,
			},
		},
	}
	fingerprint, err := model.FingerprintZTAPICredential("authorization\x00Bearer second-key")
	require.NoError(t, err)
	common.SetContextKey(c, constant.ContextKeyZTAPIHealthCredentialPin, fingerprint.String())

	key, index, apiErr := selectChannelKeyForRequest(c, channel)
	require.Nil(t, apiErr)
	require.Equal(t, "second-key", key)
	require.Equal(t, 1, index)
}

func TestZTAPIHealthProbePinMiddlewareRejectsValidatorIdentityMismatch(t *testing.T) {
	pin := testZTAPIHealthProbePin()
	headers := map[string]string{
		ZTAPIHealthProbeCaseHeader: pin.CaseID, ZTAPIHealthProbeLeaseHeader: pin.LeaseToken,
		"X-Request-ID": pin.ProbeRequestID,
	}
	recorder, called, c := runZTAPIHealthProbePinMiddleware(t, "127.0.0.1:1234", 42, headers,
		func(context.Context, string, string, string, time.Time) (model.ZTAPIHealthVerificationPin, error) {
			pin.ChannelID++
			pin.CaseID = "different-case"
			return pin, nil
		})
	require.False(t, called)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Nil(t, relaycommon.GetZTAPIHealthProbePin(c))
	require.Empty(t, c.Request.Header.Get(ZTAPIHealthProbeCaseHeader))
	require.Empty(t, c.Request.Header.Get(ZTAPIHealthProbeLeaseHeader))
}
