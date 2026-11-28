package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIXcloudPublication20261128StageRequiresRootAndExplicitConfirmation(t *testing.T) {
	for _, tc := range []struct {
		role int
		body string
		code int
	}{
		{common.RoleCommonUser, `{"confirm":true,"source_model":"gpt-6.2-sol","channel_id":7,"group":"gpt-plus"}`, 403},
		{common.RoleRootUser, `{"source_model":"gpt-6.2-sol","channel_id":7,"group":"gpt-plus"}`, 400},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", 1)
		c.Set("role", tc.role)
		c.Request = httptest.NewRequest(http.MethodPost, "/stage", strings.NewReader(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		StageZTAPIXcloud20261128(c)
		require.Equal(t, tc.code, w.Code)
	}
}

func TestZTAPIXcloudPublication20261128StageUsesServerFetchedEvidence(t *testing.T) {
	oldFetch, oldStage, oldQuote := ztapiXcloudStageDiscovery20261128, ztapiXcloudStage20261128, ztapiXcloudStageQuote20261128
	t.Cleanup(func() {
		ztapiXcloudStageDiscovery20261128, ztapiXcloudStage20261128, ztapiXcloudStageQuote20261128 = oldFetch, oldStage, oldQuote
	})
	ztapiXcloudStageQuote20261128 = func() error { return nil }
	ztapiXcloudStageDiscovery20261128 = func(source string, id int) ([]string, error) {
		require.Equal(t, "gpt-6.2-sol", source)
		require.Equal(t, 7, id)
		return []string{source}, nil
	}
	ztapiXcloudStage20261128 = func(operator int, r model.ZTAPIXcloudStage20261128, visible []string, at time.Time) (*model.ZTAPIXcloudStaged20261128, error) {
		require.Equal(t, 1, operator)
		require.Equal(t, []string{r.SourceModel}, visible)
		require.WithinDuration(t, time.Now(), at, time.Second)
		return &model.ZTAPIXcloudStaged20261128{}, nil
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)
	c.Request = httptest.NewRequest(http.MethodPost, "/stage", strings.NewReader(`{"confirm":true,"source_model":"gpt-6.2-sol","channel_id":7,"group":"gpt-plus"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	StageZTAPIXcloud20261128(c)
	require.Equal(t, http.StatusOK, w.Code)
}
