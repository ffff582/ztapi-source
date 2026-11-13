package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
)

func TestZTAPIUpstreamAdmissionDispatchedHTTPFailureCannotReplay(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-6.1-sol"})
	err := types.WithOpenAIError(types.OpenAIError{Type: "server_error", Code: "server_error", Message: "synthetic failure"}, 503)
	require.True(t, shouldRetry(c, err, 1), "the regression must exercise an otherwise retryable HTTP failure")
	c.Set("ztapi_upstream_admission_dispatched", true)
	require.False(t, shouldRetry(c, err, 1), "a dispatched admission-controlled request must not be replayed on another route")
}

func TestZTAPIUpstreamAdmissionUndispatchedFailureReleasesOnlyOwnReservation(t *testing.T) {
	f := newZTAPIResponsesEndpointFixture(t)
	// New immutable local contracts, not edits to an existing publication.
	var source model.ZTAPIModelPriceSource
	var snapshot model.ZTAPIModelPublicationSnapshot
	var channel model.Channel
	require.NoError(t, f.db.First(&source, "model_config_id = ?", f.config.ID).Error)
	require.NoError(t, f.db.First(&snapshot, "model_config_id = ?", f.config.ID).Error)
	require.NoError(t, f.db.First(&channel, 1).Error)
	f.sourceModel, f.publicName = "gpt-6.1-sol", "zt-gpt-6.1-sol"
	config := f.config
	config.ID, config.PublicationSnapshotID = 0, 0
	config.SourceModel, config.PublicName = f.sourceModel, &f.publicName
	require.NoError(t, f.db.Create(&config).Error)
	var err error
	source, err = model.BuildZTAPISupplierAdmissionTextPriceSource20261019("GPT 6.1 Sol", config.ID, 1, 1_792_419_000,
		model.ZTAPIABFXParams{Mode: model.ZTAPIFXModePlatformV1, PlatformRate: decimal.RequireFromString("6.63"), UpstreamUSDRate: decimal.RequireFromString("6.7628")})
	require.NoError(t, err)
	source.Version = 1
	require.NoError(t, f.db.Create(&source).Error)
	snapshot.ID, snapshot.ModelConfigID = 0, config.ID
	snapshot.SourceModel, snapshot.PublicName, snapshot.PriceSourceID = f.sourceModel, f.publicName, source.ID
	snapshot.PricePolicy, snapshot.TokenPriceRulesJSON = source.PricePolicy, source.TokenPriceRulesJSON
	snapshot.InputPricePerMillion, snapshot.OutputPricePerMillion = 3.12, 11.7
	snapshot.CacheReadRatio, snapshot.CacheCreationRatio = 0.05, 1.25
	snapshot.AllowedChannelIDs = `[2]`
	require.NoError(t, f.db.Create(&snapshot).Error)
	require.NoError(t, f.db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error)
	channel.Id, channel.Models = 2, f.sourceModel
	channel.ModelMapping = common.GetPointer(`{"gpt-6.1-sol":"gpt-6.1-sol-hc"}`)
	require.NoError(t, f.db.Create(&channel).Error)
	require.NoError(t, f.db.Create(&model.Ability{ChannelId: 2, Model: f.sourceModel, Group: "default", Enabled: true, Weight: 100}).Error)
	require.NoError(t, f.db.Model(f.token).Update("model_limits", f.publicName).Error)
	model.InitChannelCache()
	model.InvalidateZTAPIAliasCache()
	model.InvalidatePricingCache()
	_, publicationErr := model.GetZTAPIRuntimePublication(f.publicName)
	require.NoError(t, publicationErr)
	oldClient, oldRedis := common.RDB, common.RedisEnabled
	common.RDB, common.RedisEnabled = nil, false
	t.Cleanup(func() { common.RDB, common.RedisEnabled = oldClient, oldRedis })
	before := f.balances(t)
	response := f.postJSON(t, "/v1/responses", map[string]any{"model": f.publicName, "input": "hi", "max_output_tokens": 128, "reasoning": map[string]string{"effort": "high"}})
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "not sent upstream")
	after := f.balances(t)
	require.Zero(t, after.Calls)
	require.Zero(t, after.Attempts)
	require.Equal(t, before.UserQuota, after.UserQuota)
	require.Equal(t, before.TokenRemain, after.TokenRemain)
	require.Equal(t, before.TokenUsed, after.TokenUsed)
	require.Zero(t, after.Logs)
	var settlements []model.ZTAPIRequestSettlement
	require.NoError(t, f.db.Find(&settlements).Error)
	require.Len(t, settlements, 1)
	require.False(t, settlements[0].Dispatched)
	require.Equal(t, model.ZTAPISettlementReleased, settlements[0].Status)
	require.Zero(t, settlements[0].ChargedQuota)
	var ledger []model.BalanceLedger
	require.NoError(t, f.db.Order("id").Find(&ledger).Error)
	require.Len(t, ledger, 2, "one immutable reservation and one compensating release")
	for _, event := range f.events(t) {
		require.Equal(t, "not_dispatched", event.Reason)
	}
}
