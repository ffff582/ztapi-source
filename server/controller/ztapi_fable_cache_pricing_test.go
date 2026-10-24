package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIFableUniformPriceRequiresExactApprovalAndVersion(t *testing.T) {
	old := ztapiFableCachePublisher
	t.Cleanup(func() { ztapiFableCachePublisher = old })
	calls := 0
	ztapiFableCachePublisher = func(operator int, version uint64) (*model.ZTAPIModelPublicationSnapshot, error) {
		calls++
		require.Equal(t, 7, operator)
		require.Equal(t, uint64(26), version)
		return &model.ZTAPIModelPublicationSnapshot{ID: 42}, nil
	}
	run := func(body string) int {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", 7)
		context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		context.Request.Header.Set("Content-Type", "application/json")
		PublishZTAPIFableUniformCacheWrite(context)
		return recorder.Code
	}
	for _, body := range []string{`{}`, `{"confirm":true,"expected_version":26,"cache_write_price_per_million":"18.9"}`,
		`{"confirm":false,"expected_version":26,"cache_write_price_per_million":"18.86582633"}`,
		`{"confirm":true,"cache_write_price_per_million":"18.86582633"}`} {
		require.Equal(t, 400, run(body))
	}
	require.Zero(t, calls)
	require.Equal(t, 200, run(`{"confirm":true,"expected_version":26,"cache_write_price_per_million":"18.86582633"}`))
	require.Equal(t, 1, calls)
}
