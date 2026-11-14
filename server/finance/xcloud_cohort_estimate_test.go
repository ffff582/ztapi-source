package finance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFinanceXcloudCohortUsesHistoricalQuoteAndWinningRoute(t *testing.T) {
	for _, tc := range []struct {
		name, source, public, want string
		id, primary, actual        int
		usage                      string
		mutate                     func(*model.ZTAPIModelPriceSource)
	}{
		{"opus", "claude-opus-5-5", "zt-claude-opus-5.5", "0.035927", 126, 6, 6, `[{"dimension":"input_tokens","quantity":2},{"dimension":"output_tokens","quantity":3},{"dimension":"cache_read","quantity":24805},{"dimension":"cache_write_5m","quantity":6978}]`, nil},
		{"sonnet", "claude-sonnet-5-5", "zt-claude-sonnet-5.5", "0.010559", 128, 6, 6, `[{"dimension":"input_tokens","quantity":2},{"dimension":"output_tokens","quantity":3},{"dimension":"cache_read","quantity":14727},{"dimension":"cache_write_5m","quantity":4100}]`, nil},
		{"same model on backup", "claude-opus-5-5", "zt-claude-opus-5.5", "0.016219", 126, 6, 2, `[{"dimension":"input_tokens","quantity":10000}]`, nil},
		{"gpt split cache", "gpt-5.6-terra", "zt-gpt-5.6-terra", "0.005538", 84, 7, 7, `[{"dimension":"input_tokens","quantity":1175},{"dimension":"output_tokens","quantity":5},{"dimension":"cache_read","quantity":3225}]`, nil},
		{"tier uses whole input", "gpt-6-astra", "zt-gpt-6-astra", "4.896069", 103, 6, 6, `[{"dimension":"input_tokens","quantity":272000},{"dimension":"output_tokens","quantity":1},{"dimension":"cache_read","quantity":1}]`, nil},
		{"wrong rate", "claude-opus-5-5", "zt-claude-opus-5.5", "", 126, 6, 6, `[{"dimension":"input_tokens","quantity":10000}]`, func(p *model.ZTAPIModelPriceSource) { p.InputPerMillion = "0.01" }},
		{"wrong version", "claude-opus-5-5", "zt-claude-opus-5.5", "", 126, 6, 6, `[{"dimension":"input_tokens","quantity":10000}]`, func(p *model.ZTAPIModelPriceSource) { p.Version++ }},
		{"future source", "claude-opus-5-5", "zt-claude-opus-5.5", "", 126, 6, 6, `[{"dimension":"input_tokens","quantity":10000}]`, func(p *model.ZTAPIModelPriceSource) { p.CreatedAt++ }},
		{"wrong group", "claude-opus-5-5", "zt-claude-opus-5.5", "", 126, 6, 7, `[{"dimension":"input_tokens","quantity":10000}]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture()
			s.Channels = []Channel{{ID: 2, Name: "Yunxin", BaseURL: "https://ai.yunxinapi.com/hub"}, {ID: tc.actual, Name: "Xcloud", BaseURL: "https://www.xcloudtoken.com"}}
			if tc.actual == 2 {
				s.Channels = s.Channels[:1]
			}
			p, err := model.ZTAPIXcloudPriceEvidence20261114(tc.source, 1)
			require.NoError(t, err)
			p.ID, p.Version = 9999, 41
			s.Settlements[0].CreatedAt = s.Attempts[0].CreatedAt
			p.CreatedAt = s.Settlements[0].CreatedAt.Unix()
			internal, err := json.Marshal(map[string]any{"SourceModel": tc.source, "PublicName": tc.public, "PublicationID": tc.id,
				"PricePolicy": p.PricePolicy, "PriceSourceID": p.ID, "PriceSourceVersion": p.Version,
				"TokenPriceRulesJSON": p.TokenPriceRulesJSON, "AllowedChannelIDs": []int{2, tc.primary}})
			require.NoError(t, err)
			if tc.mutate != nil {
				tc.mutate(&p)
			}
			s.XcloudPriceSources = []model.ZTAPIModelPriceSource{p}
			s.Settlements[0].PublicModel = tc.public
			s.Settlements[0].PriceSnapshotJSON = string(internal)
			s.Settlements[0].ChargeDimensionsJSON = tc.usage
			s.Attempts[0].ChannelID = tc.actual
			r := calculate(t, s, facts()[:1])
			require.Nil(t, r.ActualCost)
			require.Len(t, r.Estimates, 1)
			require.Equal(t, tc.actual, r.Estimates[0].ChannelID)
			require.Equal(t, tc.want, r.Estimates[0].CostUSDT)
			require.Equal(t, tc.want != "", r.Estimates[0].Complete)
		})
	}
}

func TestReaderLoadsOnlyHistoricalXcloudQuoteEvidence(t *testing.T) {
	db := readerSnapshotFixture(t)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIModelPriceSource{}))
	price, err := model.ZTAPIXcloudPriceEvidence20261114("claude-opus-5-5", 1)
	require.NoError(t, err)
	price.Version = 1
	price.CreatedAt = moment("2026-11-14T00:00:00Z").Unix()
	require.NoError(t, db.Create(&price).Error)
	other := price
	other.ID, other.PricePolicy = 0, "other_policy"
	require.NoError(t, db.Create(&other).Error)
	reader := &Reader{DB: db}
	before, err := reader.Load(context.Background(), moment("2026-11-14T00:00:00Z"))
	require.NoError(t, err)
	require.Empty(t, before.XcloudPriceSources)
	after, err := reader.Load(context.Background(), moment("2026-11-15T00:00:00Z"))
	require.NoError(t, err)
	require.Len(t, after.XcloudPriceSources, 1)
	require.Equal(t, price.ID, after.XcloudPriceSources[0].ID)
	require.Equal(t, price.TokenPriceRulesJSON, after.XcloudPriceSources[0].TokenPriceRulesJSON)
	t.Run("old column grants keep the report available", func(t *testing.T) {
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:deny_xcloud_quote_columns", func(tx *gorm.DB) {
			if strings.Contains(tx.Statement.Table, "ztapi_model_price_sources") {
				tx.AddError(&mysqldriver.MySQLError{Number: 1143})
			}
		}))
		legacy := &Reader{DB: db}
		result, err := legacy.Load(context.Background(), moment("2026-11-15T00:00:00Z"))
		require.NoError(t, err)
		require.Empty(t, result.XcloudPriceSources)
		require.Len(t, result.Settlements, 1)
	})
}
