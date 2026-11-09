package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageSalePublicationRequiresRootAndExactExplicitApproval(t *testing.T) {
	old := ztapiImageSalePublisher
	t.Cleanup(func() { ztapiImageSalePublisher = old })
	calls := 0
	ztapiImageSalePublisher = func(operator int, request model.ZTAPIImageSalePublicationRequest) (*model.ZTAPIModelPublicationSnapshot, error) {
		calls++
		require.Equal(t, 7, operator)
		require.Equal(t, model.ZTAPIImageSalePublicationRequest{Confirm: true, ExpectedModelConfigID: 85, ExpectedPriceSourceID: 1873, ExpectedSnapshotID: 1631, ExpectedVersion: 27}, request)
		return &model.ZTAPIModelPublicationSnapshot{ID: 1632, ModelVersion: 28}, nil
	}
	approved := `{"confirm":true,"expected_model_config_id":85,"expected_price_source_id":1873,"expected_snapshot_id":1631,"expected_version":27}`
	run := func(role int, body string) int {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", 7)
		context.Set("role", role)
		context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		context.Request.Header.Set("Content-Type", "application/json")
		PublishZTAPIImageSale(context)
		return recorder.Code
	}
	require.Equal(t, 403, run(common.RoleAdminUser, approved))
	for _, body := range []string{
		`{}`, `{`, `null`, approved + `{}`,
		strings.Replace(approved, `"confirm":true`, `"confirm":false`, 1),
		strings.Replace(approved, `"expected_model_config_id":85,`, "", 1),
		strings.Replace(approved, `"expected_price_source_id":1873,`, "", 1),
		strings.Replace(approved, `"expected_snapshot_id":1631,`, "", 1),
		strings.Replace(approved, `"expected_version":27`, `"expected_version":28`, 1),
		strings.Replace(approved, `"confirm":true`, `"confirm":true,"confirm":false`, 1),
		strings.Replace(approved, `"confirm":true`, `"confirm":true,"image_sale_contract_json":"{}"`, 1),
		strings.Replace(approved, `"confirm":true`, `"confirm":true,"base_prices":{"1K":"0.01"}`, 1),
		strings.Repeat(" ", 4097) + approved,
	} {
		require.Equal(t, 400, run(common.RoleRootUser, body), body)
	}
	require.Zero(t, calls)
	require.Equal(t, 200, run(common.RoleRootUser, approved))
	require.Equal(t, 1, calls)
	ztapiImageSalePublisher = func(int, model.ZTAPIImageSalePublicationRequest) (*model.ZTAPIModelPublicationSnapshot, error) {
		return nil, errors.New("stale publication")
	}
	require.Equal(t, 409, run(common.RoleRootUser, approved))
}

func TestZTAPIImageSalePublicationRestoreActionRequiresExplicitCurrentCAS(t *testing.T) {
	old := ztapiImageSalePublisher
	t.Cleanup(func() { ztapiImageSalePublisher = old })
	calls := 0
	ztapiImageSalePublisher = func(operator int, request model.ZTAPIImageSalePublicationRequest) (*model.ZTAPIModelPublicationSnapshot, error) {
		calls++
		require.Equal(t, 7, operator)
		require.Equal(t, uint64(28), request.ExpectedVersion)
		require.Equal(t, int64(1632), request.ExpectedSnapshotID)
		raw, err := common.Marshal(request)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"action":"restore_legacy_token"`)
		return &model.ZTAPIModelPublicationSnapshot{ID: 1633, ModelVersion: 29}, nil
	}
	approved := `{"action":"restore_legacy_token","confirm":true,"expected_model_config_id":85,"expected_price_source_id":1873,"expected_snapshot_id":1632,"expected_version":28}`
	run := func(body string) int {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", 7)
		context.Set("role", common.RoleRootUser)
		context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		PublishZTAPIImageSale(context)
		return recorder.Code
	}
	for _, body := range []string{
		strings.Replace(approved, `"confirm":true`, `"confirm":false`, 1),
		strings.Replace(approved, `"expected_snapshot_id":1632,`, "", 1),
		strings.Replace(approved, `"expected_version":28`, `"expected_version":0`, 1),
		strings.Replace(approved, `"expected_price_source_id":1873`, `"expected_price_source_id":0`, 1),
		strings.Replace(approved, `"restore_legacy_token"`, `"custom"`, 1),
		strings.Replace(approved, `"restore_legacy_token"`, `"enable"`, 1),
	} {
		require.Equal(t, http.StatusBadRequest, run(body), body)
	}
	require.Zero(t, calls)
	require.Equal(t, http.StatusOK, run(approved))
	require.Equal(t, 1, calls)
}
