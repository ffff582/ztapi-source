package common

import (
	"context"
	base "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestZTAPIManagedBillingIdentityCannotReuseClientCorrelation(t *testing.T) {
	var identifiers []string
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		c.Set(base.RequestIdKey, "same-client-id")
		info := &RelayInfo{RequestId: "same-client-id", OriginModelName: "quoted-model", ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{PublicName: "quoted-model"}}
		var execution string
		_, err := StartZTAPIHealthRequest(c, info, ZTAPIHealthBackend{AdmitRequest: func(_ context.Context, _ string, id, correlation string, _ int, _ bool) (*types.ZTAPIHealthTicket, error) {
			execution = id
			require.Equal(t, "same-client-id", correlation)
			return nil, nil
		}})
		require.NoError(t, err)
		require.Equal(t, execution, info.RequestId)
		require.Equal(t, execution, c.GetString(base.RequestIdKey))
		require.Equal(t, execution, c.Request.Context().Value(base.RequestIdKey))
		require.Equal(t, execution, recorder.Header().Get("X-Request-ID"))
		identifiers = append(identifiers, info.RequestId)
	}
	require.NotEqual(t, identifiers[0], identifiers[1])
}

func TestZTAPIBillingIdentityDoesNotChangeUnmanagedCorrelation(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set(base.RequestIdKey, "legacy-id")
	info := &RelayInfo{RequestId: "legacy-id"}
	_, err := StartZTAPIHealthRequest(c, info, ZTAPIHealthBackend{AdmitRequest: func(context.Context, string, string, string, int, bool) (*types.ZTAPIHealthTicket, error) {
		return nil, nil
	}})
	require.NoError(t, err)
	require.Equal(t, "legacy-id", info.RequestId)
	require.Equal(t, "legacy-id", c.GetString(base.RequestIdKey))
}
