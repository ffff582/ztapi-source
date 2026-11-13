package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageAdmissionRunsBeforeQuotaReservation(t *testing.T) {
	body := []byte(`{"model":"zt-image","prompt":"neutral","n":1,"size":"1024x1024","quality":"standard","response_format":"url"}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	request := &dto.ImageRequest{}
	require.NoError(t, common.Unmarshal(body, request))
	info := &relaycommon.RelayInfo{
		OriginModelName: "zt-image",
		Request:         request,
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: "zt-image", SourceModel: "provider-image", Modality: "image",
			ImageProtocolContract: nil,
		},
	}
	reservations := 0
	err := preConsumeAfterZTAPIImageAdmission(c, info, func() *types.NewAPIError {
		reservations++
		return nil
	})
	require.NotNil(t, err)
	require.Zero(t, reservations)
}
