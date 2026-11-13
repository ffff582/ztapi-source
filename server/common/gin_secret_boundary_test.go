package common

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBusinessAPIErrorRedactsCredentials(t *testing.T) {
	for _, write := range []func(*gin.Context){
		func(c *gin.Context) { ApiError(c, errors.New("request req_public API_KEY=opaque-secret")) },
		func(c *gin.Context) { ApiErrorMsg(c, "request req_public password=opaque-secret") },
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		write(c)
		require.NotContains(t, recorder.Body.String(), "opaque-secret")
		require.Contains(t, recorder.Body.String(), "req_public")
	}
}
