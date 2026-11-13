package spending

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func oldHistory(t *testing.T, r *Reader, now time.Time, n int, status string) {
	t.Helper()
	old := now.Add(-30 * 24 * time.Hour)
	require.NoError(t, r.DB.Transaction(func(tx *gorm.DB) error {
		for i := 1; i <= n; i++ {
			op := fmt.Sprint("old-", i)
			if err := tx.Exec(`INSERT INTO ztapi_request_settlements(id,operation_id,request_id,user_id,status,public_model,price_snapshot_json,usage_json,missing_dimensions_json,reserved_quota,charged_quota,refunded_quota,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, i, op, op, 1, status, "text", `{"quota_per_unit":123}`, `{}`, `[]`, 1, 1, 0, old, old).Error; err != nil {
				return err
			}
			if status == "settled" {
				if err := tx.Exec(`INSERT INTO ztapi_settlement_log_outboxes VALUES(?,?,?)`, op, old.Unix(), i).Error; err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO balance_ledgers VALUES(?,?,?,?,?,?,?)`, i*10, 1, op, "usage_reservation", "ztapi:"+op+":begin", -1, old).Error; err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO ztapi_supplier_refund_charges VALUES(?,?,?,1,0,'',?,1,0,'{}')`, i, i, op, i*10).Error; err != nil {
					return err
				}
			}
		}
		return nil
	}))
}
func TestReviewWindowIgnoresOver2048OldHistoryAndPreservesInput(t *testing.T) {
	r, now := sourceFixture(t)
	oldHistory(t, r, now, 2100, "settled")
	seedSettlement(t, r, now, 9000, "current", "settled", 50000)
	// Production dto.Usage emits InputTokens=0 even when PromptTokens is populated.
	raw, err := common.Marshal(dto.Usage{PromptTokens: 1000, InputTokens: 0, UsageSource: "upstream_reported", PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 900}})
	require.NoError(t, err)
	dims, err := common.Marshal([]model.ZTAPISupplierRefundDimension{{Dimension: "input_tokens", Units: "100", ChargedQuota: 5000}, {Dimension: "cache_read_tokens", Units: "900", ChargedQuota: 45000}})
	require.NoError(t, err)
	require.NoError(t, r.DB.Exec(`UPDATE ztapi_request_settlements SET usage_json=?,charge_dimensions_json=? WHERE id=9000`, string(raw), string(dims)).Error)
	rows, queries := int64(0), 0
	require.NoError(t, r.DB.Callback().Query().After("gorm:query").Register("window_rows", func(db *gorm.DB) { rows += db.RowsAffected; queries++ }))
	snap, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.True(t, snap.Overview.Complete)
	require.Equal(t, "0.1", *snap.Overview.ConfirmedDailyU)
	require.Len(t, snap.Inputs, 1)
	require.EqualValues(t, 100, snap.Inputs[0].Tokens)
	require.Less(t, rows, int64(100))
	require.Less(t, queries, 30)
	store, err := NewStore(fixture(t))
	require.NoError(t, err)
	_, err = store.SavePreferences(context.Background(), 1, Preferences{Enabled: true, InputTokens: 50}, now.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, store.Evaluate(context.Background(), 1, snap, now))
	alerts, _, err := store.Alerts(context.Background(), 1, 20, 0)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Equal(t, "100", alerts[0].MeasuredValue)
}
func TestReviewCurrentExtraAndRefundCanReferenceOldParent(t *testing.T) {
	r, now := sourceFixture(t)
	seedSettlement(t, r, now, 1, "old-parent", "settled", 100000)
	require.NoError(t, r.DB.Exec(`UPDATE ztapi_settlement_log_outboxes SET created_at=?`, now.Add(-30*24*time.Hour).Unix()).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refund_charges VALUES(1,1,'old-parent',1,0,'',10,100000,0,'{}'),(2,1,'old-parent',1,9,'extra',30,50000,10000,'{"quota_per_unit":500000}')`).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_settlement_log_outboxes VALUES('extra',?,3)`, now.Unix()-1).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO balance_ledgers VALUES(30,1,'old-parent','usage_settlement','attempt-billing:proof',-50000,?),(31,1,'old-parent','supplier_refund','supplier-refund:proof',10000,?)`, now.Add(-time.Second), now.Add(-time.Second)).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refunds VALUES(1,2,'old-parent','applied',31,10000)`).Error)
	require.NoError(t, r.DB.Exec(`UPDATE ztapi_request_settlements SET refunded_quota=10000 WHERE id=1`).Error)
	snap, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.True(t, snap.Overview.Complete)
	require.Equal(t, "0.1", *snap.Overview.ConfirmedDailyU)
	require.Equal(t, "0.02", *snap.Overview.ConfirmedRefundsDailyU)
	require.Empty(t, snap.Inputs)
}

func TestReviewCurrentRefundRequiresOriginalWalletEvidence(t *testing.T) {
	r, now := sourceFixture(t)
	seedSettlement(t, r, now, 1, "old-refunded", "settled", 100000)
	require.NoError(t, r.DB.Exec(`UPDATE ztapi_settlement_log_outboxes SET created_at=?`, now.Add(-30*24*time.Hour).Unix()).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refund_charges VALUES(1,1,'old-refunded',1,0,'',10,100000,10000,'{}')`).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refunds VALUES(1,1,'old-refunded','applied',31,10000)`).Error)
	require.NoError(t, r.DB.Exec(`INSERT INTO balance_ledgers VALUES(31,1,'old-refunded','supplier_refund','supplier-refund:proof',10000,?)`, now.Add(-time.Second)).Error)
	require.NoError(t, r.DB.Exec(`DELETE FROM balance_ledgers WHERE id=10`).Error)
	snap, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.False(t, snap.Overview.Complete)
	require.Nil(t, snap.Overview.ConfirmedRefundsDailyU)
}
func TestReviewPendingIsCountNotHistoryProjection(t *testing.T) {
	r, now := sourceFixture(t)
	oldHistory(t, r, now, 2100, "pending")
	seedSettlement(t, r, now, 9000, "current", "settled", 1)
	snap, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.True(t, snap.Overview.Complete)
	require.Equal(t, 2100, snap.Overview.PendingRequests)
	require.Equal(t, "0.000002", *snap.Overview.ConfirmedDailyU)
}
func TestReviewCurrentMissingReceiptUnknownButUnrelatedOldBrokenRowsIgnored(t *testing.T) {
	r, now := sourceFixture(t)
	seedSettlement(t, r, now, 1, "old-broken", "settled", 100000)
	seedSettlement(t, r, now, 2, "today", "settled", 100000)
	require.NoError(t, r.DB.Exec(`DELETE FROM ztapi_settlement_log_outboxes WHERE operation_id='old-broken'`).Error)
	require.NoError(t, r.DB.Exec(`UPDATE ztapi_request_settlements SET updated_at=? WHERE id=1`, now.Add(-30*24*time.Hour)).Error)
	snap, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.True(t, snap.Overview.Complete)
	require.Equal(t, "0.2", *snap.Overview.ConfirmedDailyU)
	require.NoError(t, r.DB.Exec(`DELETE FROM ztapi_settlement_log_outboxes WHERE operation_id='today'`).Error)
	snap, err = r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.False(t, snap.Overview.Complete)
	require.Nil(t, snap.Overview.ConfirmedDailyU)
}
func TestReviewKnownBalanceSurvivesFinancialReadFailure(t *testing.T) {
	r, now := sourceFixture(t)
	require.NoError(t, r.DB.Exec(`DROP TABLE logs`).Error)
	snap, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.False(t, snap.Overview.Complete)
	require.Equal(t, "2", *snap.Overview.AvailableBalanceU)
}
func TestReviewIncompleteSpendingStillAlertsKnownLowBalance(t *testing.T) {
	store, err := NewStore(fixture(t))
	require.NoError(t, err)
	now := time.Now()
	ctx := context.Background()
	_, err = store.SavePreferences(ctx, 1, Preferences{Enabled: true, DailySpendU: "1", HourlySpendU: "1", InputTokens: 1, LowBalanceU: "1"}, now)
	require.NoError(t, err)
	snap := Snapshot{Overview: Overview{Complete: false, ConfirmedDailyU: str("100"), ConfirmedHourlyU: str("100"), AvailableBalanceU: str("-0.5")}, Inputs: []Input{{RequestID: "unknown", Tokens: 100, At: now}}}
	require.NoError(t, store.Evaluate(ctx, 1, snap, now))
	alerts, _, err := store.Alerts(ctx, 1, 20, 0)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Equal(t, "low_balance", alerts[0].Kind)
	require.Equal(t, "-0.5", alerts[0].MeasuredValue)
}
func TestReviewTelegramOptInRequiresLiveBindingBeforeSave(t *testing.T) {
	for _, kind := range []string{"unavailable", "missing", "group", "empty_generation", "bound"} {
		t.Run(kind, func(t *testing.T) {
			r, now := sourceFixture(t)
			store, err := NewStore(fixture(t))
			require.NoError(t, err)
			require.NoError(t, service.ConfigureZTAPISessionSigningKey(strings.Repeat("r", 32)))
			token, err := service.IssueZTAPIAccessToken(1, now)
			require.NoError(t, err)
			b := &testBindings{binding: Binding{TelegramID: 123, Generation: "gen"}}
			available := true
			switch kind {
			case "unavailable":
				available = false
			case "missing":
				b.err = ErrNotFound
			case "group":
				b.binding.TelegramID = -1
			case "empty_generation":
				b.binding.Generation = ""
			}
			h := NewHandler(r, store, HandlerConfig{PublicOrigin: "https://ztapi.vip", Now: func() time.Time { return now }, Bindings: b, TelegramAvailable: available})
			body := `{"enabled":true,"daily_spend_u":"1","hourly_spend_u":"","low_balance_u":"","input_tokens":0,"telegram":true,"version":0}`
			req := httptest.NewRequest("PUT", "/api/spending/config", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("New-Api-User", "1")
			req.Header.Set("Origin", "https://ztapi.vip")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			p, err := store.Preferences(context.Background(), 1)
			require.NoError(t, err)
			if kind == "bound" {
				require.Equal(t, 200, w.Code)
				require.EqualValues(t, 1, p.Version)
			} else {
				require.Equal(t, 409, w.Code)
				require.Zero(t, p.Version)
				require.False(t, p.Telegram)
			}
		})
	}
}
func TestReviewPrivateStoreFullSynchronous(t *testing.T) {
	db := fixture(t)
	require.NoError(t, db.Exec(`PRAGMA synchronous=OFF`).Error)
	_, err := NewStore(db)
	require.NoError(t, err)
	var level int
	require.NoError(t, db.Raw(`PRAGMA synchronous`).Scan(&level).Error)
	require.Equal(t, 2, level)
}

func TestReviewOrdinaryInputDimensionRequiresUniquePositiveCanonicalUnits(t *testing.T) {
	raw, err := common.Marshal(dto.Usage{PromptTokens: 1000, InputTokens: 0, UsageSource: "upstream_reported", PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 900}})
	require.NoError(t, err)
	for _, units := range []string{"100", "0", "-1", "1.0", "1e2", "01", "+1", "9223372036854775808"} {
		t.Run(units, func(t *testing.T) {
			dims, err := common.Marshal([]model.ZTAPISupplierRefundDimension{{Dimension: "input_tokens", Units: units}, {Dimension: "cache_read_tokens", Units: "900"}})
			require.NoError(t, err)
			value, ok := normalizedInput(string(raw), string(dims))
			if units == "100" {
				require.True(t, ok)
				require.EqualValues(t, 100, value)
			} else {
				require.False(t, ok)
			}
		})
	}
	for _, dims := range []string{`[]`, `[{"dimension":"cache_read_tokens","units":"900"}]`, `[{"dimension":"input_tokens","units":"100"},{"dimension":"input_tokens","units":"100"}]`, `[{"dimension":"input_tokens","quantity":100}]`} {
		_, ok := normalizedInput(string(raw), dims)
		require.False(t, ok)
	}
}

func TestReviewTransientBindingReadKeepsPendingWithoutSending(t *testing.T) {
	e, b, sender, now := queuedEngine(t)
	b.err = ErrUnavailable
	require.NoError(t, e.Deliver(context.Background(), now))
	rows, _, err := e.Store.Alerts(context.Background(), 1, 20, 0)
	require.NoError(t, err)
	require.Equal(t, "pending", rows[0].DeliveryStatus)
	require.Zero(t, sender.calls)
	b.err = nil
	require.NoError(t, e.Deliver(context.Background(), now.Add(time.Minute)))
	require.Equal(t, 1, sender.calls)
}

type interruptedBindingRead struct {
	calls   int
	binding Binding
	cancel  context.CancelFunc
}

func (b *interruptedBindingRead) Lookup(context.Context, int) (Binding, error) {
	b.calls++
	if b.calls == 2 {
		b.cancel()
		return Binding{}, ErrUnavailable
	}
	return b.binding, nil
}
func TestReviewCancelledPreSendRecheckRestoresPendingNotUnknown(t *testing.T) {
	e, b, sender, now := queuedEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Bindings = &interruptedBindingRead{binding: b.binding, cancel: cancel}
	require.NoError(t, e.Deliver(ctx, now))
	rows, _, err := e.Store.Alerts(context.Background(), 1, 20, 0)
	require.NoError(t, err)
	require.Equal(t, "pending", rows[0].DeliveryStatus)
	require.Zero(t, sender.calls)
	require.NoError(t, e.Deliver(context.Background(), now.Add(time.Minute)))
	require.Equal(t, 1, sender.calls)
	require.NoError(t, e.Deliver(context.Background(), now.Add(2*time.Minute)))
	require.Equal(t, 1, sender.calls)
}

type cancelSource struct {
	cancel context.CancelFunc
	calls  int
}

func (s *cancelSource) Identity(context.Context, int) (Identity, error) {
	s.calls++
	s.cancel()
	return Identity{}, ErrUnavailable
}
func (s *cancelSource) Load(context.Context, int, time.Time) (Snapshot, error) {
	return Snapshot{}, ErrUnavailable
}
func TestReviewRunOnceDoesNotAdvanceUnprocessedOwnersAfterCancellation(t *testing.T) {
	store, err := NewStore(fixture(t))
	require.NoError(t, err)
	now := time.Now()
	for user := 1; user <= 25; user++ {
		_, err = store.SavePreferences(context.Background(), user, Preferences{Enabled: true, DailySpendU: "1"}, now)
		require.NoError(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancelSource{cancel: cancel}
	engine := &Engine{Store: store, Source: source}
	require.ErrorIs(t, engine.RunOnce(ctx, now), context.Canceled)
	require.Equal(t, 1, source.calls)
	require.Zero(t, engine.cursor)
}
