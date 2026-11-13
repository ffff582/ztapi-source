package spending

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestPrivateStoreRefusesForeignTables(t *testing.T) {
	db := fixture(t)
	require.NoError(t, db.Exec(`CREATE TABLE support_telegram_bindings(user_id INTEGER)`).Error)
	_, err := NewStore(db)
	require.Error(t, err)
	require.False(t, db.Migrator().HasTable("spending_preferences"))
}
func TestSourceOnlySelectAndNoCrossOwnerMoney(t *testing.T) {
	r, now := sourceFixture(t)
	seedSettlement(t, r, now, 1, "one", "settled", 1)
	require.NoError(t, r.DB.Callback().Create().Before("gorm:create").Register("no_write", func(db *gorm.DB) { t.Error("source attempted CREATE") }))
	require.NoError(t, r.DB.Callback().Update().Before("gorm:update").Register("no_write", func(db *gorm.DB) { t.Error("source attempted UPDATE") }))
	require.NoError(t, r.DB.Callback().Delete().Before("gorm:delete").Register("no_write", func(db *gorm.DB) { t.Error("source attempted DELETE") }))
	require.NoError(t, r.DB.Exec(`PRAGMA query_only=1`).Error)
	require.NoError(t, r.Preflight(context.Background()))
	one, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.True(t, one.Overview.Complete)
	require.Equal(t, "0.000002", *one.Overview.ConfirmedDailyU)
	other, err := r.Load(context.Background(), 2, now)
	require.NoError(t, err)
	require.True(t, other.Overview.Complete)
	require.Equal(t, "0", *other.Overview.ConfirmedDailyU)
	require.Equal(t, "6", *other.Overview.AvailableBalanceU)
}

func TestSourcePagesAreMemoryBounded(t *testing.T) {
	r, now := sourceFixture(t)
	for i := 1; i <= 70; i++ {
		seedSettlement(t, r, now, i, fmt.Sprint("pending-", i), "pending", 0)
	}
	require.NoError(t, r.DB.Callback().Query().After("gorm:query").Register("bounded_page", func(db *gorm.DB) {
		if limit, ok := db.Statement.Clauses["LIMIT"].Expression.(clause.Limit); ok && limit.Limit != nil {
			require.LessOrEqual(t, *limit.Limit, 64)
		}
	}))
	snap, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.Equal(t, 70, snap.Overview.PendingRequests)
}
func TestDuplicatePrimaryAnchorAndContradictoryUnitsFailClosed(t *testing.T) {
	r, now := sourceFixture(t)
	seedSettlement(t, r, now, 1, "one", "settled", 1)
	require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refund_charges VALUES(1,1,'one',1,0,'',10,1,0,'{}'),(2,1,'one',1,0,'',10,1,0,'{}')`).Error)
	snap, err := r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.False(t, snap.Overview.Complete)
	// Several corrupt anchors for one request still describe one incomplete request.
	require.NoError(t, r.DB.Exec(`UPDATE ztapi_supplier_refund_charges SET charged_quota=2`).Error)
	snap, err = r.Load(context.Background(), 1, now)
	require.NoError(t, err)
	require.Equal(t, 1, snap.Overview.IncompleteRequests)
	_, err = snapshotUnit(`{"quota_per_unit":500000,"ztapi_quota_per_unit":"1000000"}`, decimal.RequireFromString("500000"))
	require.Error(t, err)
}
func TestRecoveryRearmsLowBalanceWithoutSaveFlood(t *testing.T) {
	ctx := context.Background()
	s, err := NewStore(fixture(t))
	require.NoError(t, err)
	now := time.Now().UTC()
	p, err := s.SavePreferences(ctx, 1, Preferences{Enabled: true, LowBalanceU: "1"}, now)
	require.NoError(t, err)
	snap := Snapshot{Overview: Overview{Complete: true, AvailableBalanceU: str("0.5")}}
	require.NoError(t, s.Evaluate(ctx, 1, snap, now))
	require.NoError(t, s.Evaluate(ctx, 1, snap, now.Add(25*time.Hour)))
	rows, _, err := s.Alerts(ctx, 1, 100, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	p, err = s.SavePreferences(ctx, 1, p, now)
	require.NoError(t, err)
	require.NoError(t, s.Evaluate(ctx, 1, snap, now.Add(25*time.Hour)))
	rows, _, _ = s.Alerts(ctx, 1, 100, 0)
	require.Len(t, rows, 1)
	snap.Overview.AvailableBalanceU = str("2")
	require.NoError(t, s.Evaluate(ctx, 1, snap, now.Add(25*time.Hour)))
	snap.Overview.AvailableBalanceU = str("0.5")
	require.NoError(t, s.Evaluate(ctx, 1, snap, now.Add(26*time.Hour)))
	rows, _, _ = s.Alerts(ctx, 1, 100, 0)
	require.Len(t, rows, 2)
}
func TestConcurrentWorkersCannotDuplicateAlertOrSend(t *testing.T) {
	e, _, sender, now := queuedEngine(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.Deliver(context.Background(), now) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, sender.calls)
}
func TestInputOnlyNormalizedConfirmedField(t *testing.T) {
	dimensions := `[{"dimension":"input_tokens","units":"100"},{"dimension":"cache_read_tokens","units":"1000"},{"dimension":"cache_write_tokens","units":"2000"}]`
	for _, raw := range []string{`{}`, `{"input_tokens":200,"usage_source":"estimated"}`, `{"prompt_tokens":200,"usage_source":"upstream_reported"}`, `{"input_tokens":-1,"usage_source":"upstream_reported"}`} {
		_, ok := normalizedInput(raw, `[]`)
		require.False(t, ok)
	}
	n, ok := normalizedInput(`{"input_tokens":0,"usage_source":"upstream_reported","cache_read_input_tokens":1000,"cache_creation_input_tokens":2000}`, dimensions)
	require.True(t, ok)
	require.EqualValues(t, 100, n)
}
func TestTelegramExplicitRateLimitUnknownAndRedirectNeverReplay(t *testing.T) {
	for _, tc := range []struct {
		code         int
		body, status string
	}{{429, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`, "rate_limited"}, {403, `{"ok":false,"error_code":403}`, "blocked"}, {500, `{"ok":false,"error_code":500}`, "unknown"}, {500, `{"ok":false,"error_code":403}`, "unknown"}, {502, `{"ok":false,"error_code":400}`, "unknown"}, {200, `{`, "unknown"}, {302, `{}`, "unknown"}} {
		t.Run(fmt.Sprint(tc.code, tc.body), func(t *testing.T) {
			calls := 0
			c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				header := make(http.Header)
				header.Set("Location", "https://evil.example")
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(tc.body)), Header: header}, nil
			})}
			sender, err := NewTelegramSender("123456:"+strings.Repeat("a", 32), c)
			require.NoError(t, err)
			require.Equal(t, tc.status, sender.Send(context.Background(), 123).Status)
			require.Equal(t, 1, calls)
		})
	}
}
