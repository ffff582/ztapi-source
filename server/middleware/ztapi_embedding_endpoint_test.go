package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIEmbeddingEndpointIsolation(t *testing.T) {
	for _, tt := range []struct {
		source, path string
		allowed      bool
	}{
		{"text-embedding-ada-002", "/v1/embeddings", true},
		{"text-embedding-3-small", "/v1/chat/completions", false},
		{"text-embedding-ada-002", "/v1/responses", false},
		{"text-embedding-ada-002", "/v1/messages", false},
		{"gpt-5.5", "/v1/embeddings", false},
		{"gpt-5.5", "/v1/chat/completions", true},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", tt.path, strings.NewReader(`{"input":"test"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{SourceModel: tt.source})
		err := validateZTAPIRequestProtocol(c, &model.Channel{}, "zt-"+tt.source)
		if tt.allowed {
			require.Nil(t, err)
		} else {
			require.NotNil(t, err)
			require.Equal(t, 400, err.StatusCode)
		}
	}
}

func TestZTAPIEmbeddingRejectsUnsupportedInputBeforeDispatch(t *testing.T) {
	for _, body := range []string{`{"input":"test","stream":true}`, `{"input":"test","encoding_format":"base64"}`, `{"input":"test","dimensions":0}`, `{"input":[]}`, `{"input":null}`, `{"input":["first",2]}`} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "text-embedding-ada-002"})
		err := validateZTAPIRequestProtocol(c, &model.Channel{}, "zt-text-embedding-ada-002")
		require.NotNil(t, err, body)
	}
}
