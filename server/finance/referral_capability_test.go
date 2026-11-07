package finance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type healthCapabilitySource struct {
	Source
	probe func(context.Context) bool
}

func (s healthCapabilitySource) SupportsReferralV1(ctx context.Context) bool { return s.probe(ctx) }

func healthCapabilityResponse(t *testing.T, source Source, ctx context.Context, supported bool) {
	t.Helper()
	h := NewHandler(source, nil, "", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(ctx))
	require.Equal(t, http.StatusOK, w.Code)
	expected := `{"ok":true,"capabilities":{"referral_v1":false}}`
	if supported {
		expected = `{"ok":true,"capabilities":{"referral_v1":true}}`
	}
	require.JSONEq(t, expected, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestReferralHealthCapabilityLegacyDefaultsFalse(t *testing.T) {
	// An existing snapshot cannot establish a live capability; no Load/Identity
	// or fact store access is required by this public health endpoint.
	healthCapabilityResponse(t, &testSource{s: fixture()}, context.Background(), false)
	healthCapabilityResponse(t, nil, context.Background(), false)
}

func TestReferralHealthCapabilityOptionalSourceAndDeadline(t *testing.T) {
	for _, supported := range []bool{false, true} {
		called := false
		source := healthCapabilitySource{probe: func(ctx context.Context) bool {
			called = true
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			if ok {
				require.LessOrEqual(t, time.Until(deadline), 2*time.Second)
			}
			return supported
		}}
		healthCapabilityResponse(t, source, context.Background(), supported)
		require.True(t, called)
	}
}

func TestReferralHealthCapabilityTimeoutPreservesHTTPHealth(t *testing.T) {
	called := false
	source := healthCapabilitySource{probe: func(ctx context.Context) bool {
		called = true
		<-ctx.Done()
		return true // A late affirmative result must not advertise readiness.
	}}
	start := time.Now()
	healthCapabilityResponse(t, source, context.Background(), false)
	require.True(t, called)
	require.Less(t, time.Since(start), 3*time.Second)
}

func TestReferralHealthCapabilityRespectsEarlierCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	healthCapabilityResponse(t, healthCapabilitySource{probe: func(ctx context.Context) bool { return true }}, ctx, false)
}

func referralCapabilityDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newStore(t).db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_ledger_entries(id INTEGER,user_id INTEGER,kind TEXT,amount_micros BIGINT,available_at BIGINT,created_at BIGINT)").Error)
	require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_withdrawals(id INTEGER,amount_micros BIGINT,fee_micros BIGINT,net_amount_micros BIGINT,status TEXT,created_at BIGINT,paid_at BIGINT,address TEXT,tx_id TEXT)").Error)
	return db
}

func readerReferralCapability(t *testing.T, reader *Reader, ctx context.Context) bool {
	t.Helper()
	probe, ok := any(reader).(interface{ SupportsReferralV1(context.Context) bool })
	require.True(t, ok, "Reader must implement the optional capability interface")
	return probe.SupportsReferralV1(ctx)
}

func TestReaderReferralCapabilityBoundedReadOnlyProjection(t *testing.T) {
	db := referralCapabilityDB(t)
	// A second malformed row must never be scanned by the capability probe.
	require.NoError(t, db.Exec("INSERT INTO ztapi_referral_ledger_entries VALUES(1,1,'commission',5000000,1,1),(2,1,'commission','not-an-integer',1,1)").Error)
	require.NoError(t, db.Exec("INSERT INTO ztapi_referral_withdrawals VALUES(1,20000000,1000000,19000000,'paid',1,2,'private-wallet','private-tx'),(2,'not-an-integer',0,0,'requested',1,0,'private-wallet','private-tx')").Error)
	require.NoError(t, db.Exec("PRAGMA query_only = ON").Error)
	queries := map[string]int{}
	expected := map[string]string{
		"ztapi_referral_ledger_entries": "id,user_id,kind,amount_micros,available_at,created_at",
		"ztapi_referral_withdrawals":    "id,amount_micros,fee_micros,net_amount_micros,status,created_at,paid_at",
	}
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("capability_projection", func(tx *gorm.DB) {
		projection, ok := expected[tx.Statement.Table]
		require.True(t, ok, "health must not load report source tables")
		require.Equal(t, []string{projection}, tx.Statement.Selects)
		limit, ok := tx.Statement.Clauses["LIMIT"].Expression.(clause.Limit)
		require.True(t, ok)
		require.NotNil(t, limit.Limit)
		if limit.Limit != nil {
			require.Equal(t, 1, *limit.Limit)
		}
		require.NotContains(t, strings.Join(tx.Statement.Selects, ","), "address")
		require.NotContains(t, strings.Join(tx.Statement.Selects, ","), "tx_id")
		deadline, ok := tx.Statement.Context.Deadline()
		require.True(t, ok)
		if ok {
			require.LessOrEqual(t, time.Until(deadline), 2*time.Second)
		}
		queries[tx.Statement.Table]++
	}))
	reader := &Reader{DB: db}
	require.True(t, readerReferralCapability(t, reader, context.Background()))
	require.Equal(t, map[string]int{"ztapi_referral_ledger_entries": 1, "ztapi_referral_withdrawals": 1}, queries)
	healthCapabilityResponse(t, reader, context.Background(), true)
}

func TestReaderReferralCapabilityEmptyTablesAreReadable(t *testing.T) {
	db := referralCapabilityDB(t)
	require.True(t, readerReferralCapability(t, &Reader{DB: db}, context.Background()))
}

func TestReaderReferralCapabilityMissingOldAndDeniedAreFalse(t *testing.T) {
	for _, mode := range []string{"missing_ledger", "missing_withdrawals", "old_ledger", "old_withdrawals", "denied_ledger", "denied_withdrawals"} {
		t.Run(mode, func(t *testing.T) {
			db := referralCapabilityDB(t)
			switch mode {
			case "missing_ledger":
				require.NoError(t, db.Exec("DROP TABLE ztapi_referral_ledger_entries").Error)
			case "missing_withdrawals":
				require.NoError(t, db.Exec("DROP TABLE ztapi_referral_withdrawals").Error)
			case "old_ledger":
				require.NoError(t, db.Exec("ALTER TABLE ztapi_referral_ledger_entries DROP COLUMN amount_micros").Error)
			case "old_withdrawals":
				require.NoError(t, db.Exec("ALTER TABLE ztapi_referral_withdrawals DROP COLUMN net_amount_micros").Error)
			default:
				table := "ztapi_referral_ledger_entries"
				if mode == "denied_withdrawals" {
					table = "ztapi_referral_withdrawals"
				}
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("capability_denied", func(tx *gorm.DB) {
					if tx.Statement.Table == table {
						tx.AddError(errors.New("permission denied; private source details"))
					}
				}))
			}
			require.NoError(t, db.Exec("PRAGMA query_only = ON").Error)
			reader := &Reader{DB: db}
			require.False(t, readerReferralCapability(t, reader, context.Background()))
			healthCapabilityResponse(t, reader, context.Background(), false)
		})
	}
}

func TestReaderReferralCapabilityUnavailableDBIsFalse(t *testing.T) {
	require.False(t, readerReferralCapability(t, nil, context.Background()))
	require.False(t, readerReferralCapability(t, &Reader{}, context.Background()))
	db := referralCapabilityDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	require.False(t, readerReferralCapability(t, &Reader{DB: db}, context.Background()))
}
