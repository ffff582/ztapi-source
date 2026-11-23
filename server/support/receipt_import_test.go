package support

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func receiptImportFixture(now time.Time, id int) ReceiptImportManifest {
	input, output, cache := int64(1234), int64(56), int64(78)
	quote, credit := "0.012340", "0.024680"
	completed := now.Add(-time.Minute)
	return ReceiptImportManifest{Release: strings.Repeat("a", 40), Usages: []FinancialUsage{{
		CallID: fmt.Sprintf("%032x", id), StartedAt: now.Add(-2 * time.Minute), CompletedAt: &completed,
		ChannelID: 8, Supplier: "xcloud", Model: "deepseek-v4.1-flash", Purpose: "customer_service",
		Environment: "telegram-acceptance-v2", Outcome: "completed", Dispatched: true,
		InputTokens: &input, OutputTokens: &output, CacheReadTokens: &cache, InputTokenScope: "inclusive",
		QuoteCostU: &quote, UpstreamCredit: &credit, UpstreamRequestID: "acceptance-request-1", QuoteEvidence: "frozen-original-quote",
	}}}
}

func receiptImportCounts(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for _, table := range []string{"support_conversations", "support_messages", "support_calls", "support_call_receipts", "support_active_calls", "support_receipt_import_audits"} {
		if s.db.Migrator().HasTable(table) {
			var count int64
			require.NoError(t, s.db.Table(table).Count(&count).Error)
			counts[table] = count
		}
	}
	return counts
}

func TestReceiptImportPreservesEvidenceAndIsIdempotent(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	manifest := receiptImportFixture(now, 1)
	before, err := s.db.Migrator().GetTables()
	require.NoError(t, err)
	n, err := s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	rows, err := ReadFinancialUsage(ctx, s.db, now.Add(-24*time.Hour), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, manifest.Usages, rows)
	counts := receiptImportCounts(t, s)
	n, err = s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now.Add(time.Second))
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, counts, receiptImportCounts(t, s))
	after, err := s.db.Migrator().GetTables()
	require.NoError(t, err)
	require.ElementsMatch(t, append(before, "support_receipt_import_audits"), after)
	var call callRecord
	require.NoError(t, s.db.Take(&call).Error)
	require.Equal(t, 900007, call.UserID)
	var messages []Message
	require.NoError(t, s.db.Find(&messages).Error)
	require.Len(t, messages, 2)
	for _, message := range messages {
		require.Equal(t, "system", message.Role)
		require.Contains(t, message.Content, "administrative acceptance receipt import")
		require.Contains(t, message.Content, "not a customer conversation")
	}
	require.Zero(t, counts["support_active_calls"])
	require.False(t, s.db.Migrator().HasTable("telegram_support_dialogues"))
	var audit struct {
		CallID             string
		Hash               string
		Release            string
		OperatorTelegramID int
	}
	require.NoError(t, s.db.Table("support_receipt_import_audits").Take(&audit).Error)
	require.Equal(t, manifest.Usages[0].CallID, audit.CallID)
	require.Len(t, audit.Hash, 64)
	require.Equal(t, manifest.Release, audit.Release)
	require.Equal(t, 900007, audit.OperatorTelegramID)
}

func TestReceiptImportUnknownsStayUnknown(t *testing.T) {
	for _, mode := range []string{"cache", "no_quote", "no_usage", "no_receipt", "explicit_zero"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
			manifest := receiptImportFixture(now, 2)
			u := &manifest.Usages[0]
			switch mode {
			case "cache":
				u.CacheReadTokens = nil
				u.QuoteEvidence += UnknownCacheUpperBoundSuffix
			case "no_quote":
				u.QuoteCostU, u.UpstreamCredit = nil, nil
			case "no_usage":
				u.InputTokens, u.OutputTokens, u.CacheReadTokens = nil, nil, nil
				u.QuoteCostU, u.UpstreamCredit = nil, nil
				u.Outcome = "usage_missing"
			case "no_receipt":
				u.CompletedAt = nil
				u.Outcome = "unconfirmed"
				u.InputTokens, u.OutputTokens, u.CacheReadTokens = nil, nil, nil
				u.QuoteCostU, u.UpstreamCredit = nil, nil
				u.QuoteEvidence, u.UpstreamRequestID = "", ""
			case "explicit_zero":
				zero := int64(0)
				u.CacheReadTokens = &zero
			}
			n, err := s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now)
			require.NoError(t, err)
			require.Equal(t, 1, n)
			rows, err := ReadFinancialUsage(ctx, s.db, now.Add(-time.Hour), now.Add(time.Minute))
			require.NoError(t, err)
			require.Equal(t, manifest.Usages, rows)
			if mode == "no_receipt" {
				require.Zero(t, receiptImportCounts(t, s)["support_call_receipts"])
				require.NoError(t, s.InterruptOrphans(ctx, now))
				require.Zero(t, receiptImportCounts(t, s)["support_call_receipts"], "bot recovery must not complete imports")
			}
		})
	}
}

func TestReceiptImportRejectsInvalidBatchWithoutWrites(t *testing.T) {
	cases := map[string]func(*ReceiptImportManifest){
		"release":         func(m *ReceiptImportManifest) { m.Release = strings.Repeat("A", 40) },
		"id":              func(m *ReceiptImportManifest) { m.Usages[0].CallID = strings.Repeat("A", 32) },
		"environment":     func(m *ReceiptImportManifest) { m.Usages[0].Environment = "production" },
		"channel":         func(m *ReceiptImportManifest) { m.Usages[0].ChannelID = 9 },
		"supplier":        func(m *ReceiptImportManifest) { m.Usages[0].Supplier = "other" },
		"model":           func(m *ReceiptImportManifest) { m.Usages[0].Model = "other" },
		"purpose":         func(m *ReceiptImportManifest) { m.Usages[0].Purpose = "settlement" },
		"scope":           func(m *ReceiptImportManifest) { m.Usages[0].InputTokenScope = "uncached" },
		"actual":          func(m *ReceiptImportManifest) { value := "0"; m.Usages[0].ActualCostU = &value },
		"actual_evidence": func(m *ReceiptImportManifest) { m.Usages[0].ActualEvidence = "supplier debit" },
		"over_cap": func(m *ReceiptImportManifest) {
			q, c := "0.100000000001", "0.200000000002"
			m.Usages[0].QuoteCostU, m.Usages[0].UpstreamCredit = &q, &c
		},
		"double_discount": func(m *ReceiptImportManifest) { q := "0.00617"; m.Usages[0].QuoteCostU = &q },
		"missing_credit":  func(m *ReceiptImportManifest) { m.Usages[0].UpstreamCredit = nil },
		"negative_quote":  func(m *ReceiptImportManifest) { q := "-0.1"; m.Usages[0].QuoteCostU = &q },
		"non_decimal":     func(m *ReceiptImportManifest) { q := "1e-2"; m.Usages[0].QuoteCostU = &q },
		"finance_precision": func(m *ReceiptImportManifest) {
			q, c := "0.0000000000001", "0.0000000000002"
			m.Usages[0].QuoteCostU, m.Usages[0].UpstreamCredit = &q, &c
		},
		"finance_leading_zero": func(m *ReceiptImportManifest) {
			q, c := "00.01234", "00.02468"
			m.Usages[0].QuoteCostU, m.Usages[0].UpstreamCredit = &q, &c
		},
		"cache_unknown_without_evidence": func(m *ReceiptImportManifest) { m.Usages[0].CacheReadTokens = nil },
		"cache_suffix_not_terminal": func(m *ReceiptImportManifest) {
			m.Usages[0].CacheReadTokens = nil
			m.Usages[0].QuoteEvidence += UnknownCacheUpperBoundSuffix + ";other"
		},
		"cache_exceeds_input": func(m *ReceiptImportManifest) { v := int64(1235); m.Usages[0].CacheReadTokens = &v },
		"negative_token":      func(m *ReceiptImportManifest) { v := int64(-1); m.Usages[0].OutputTokens = &v },
		"not_dispatched":      func(m *ReceiptImportManifest) { m.Usages[0].Dispatched = false },
		"zero_start":          func(m *ReceiptImportManifest) { m.Usages[0].StartedAt = time.Time{} },
		"stale":               func(m *ReceiptImportManifest) { m.Usages[0].StartedAt = m.Usages[0].StartedAt.Add(-24 * time.Hour) },
		"future": func(m *ReceiptImportManifest) {
			v := m.Usages[0].StartedAt.Add(time.Hour)
			m.Usages[0].CompletedAt = &v
		},
		"backward": func(m *ReceiptImportManifest) {
			v := m.Usages[0].StartedAt.Add(-time.Second)
			m.Usages[0].CompletedAt = &v
		},
		"missing_completion_with_usage": func(m *ReceiptImportManifest) { m.Usages[0].CompletedAt = nil },
		"long_evidence":                 func(m *ReceiptImportManifest) { m.Usages[0].QuoteEvidence = strings.Repeat("a", 8193) },
		"long_request":                  func(m *ReceiptImportManifest) { m.Usages[0].UpstreamRequestID = strings.Repeat("a", 129) },
		"control_metadata":              func(m *ReceiptImportManifest) { m.Usages[0].UpstreamRequestID = "bad\nrequest" },
		"duplicate":                     func(m *ReceiptImportManifest) { m.Usages = append(m.Usages, m.Usages[0]) },
		"empty":                         func(m *ReceiptImportManifest) { m.Usages = nil },
		"too_many": func(m *ReceiptImportManifest) {
			for i := 2; i <= 6; i++ {
				m.Usages = append(m.Usages, receiptImportFixture(m.Usages[0].StartedAt.Add(2*time.Minute), i).Usages[0])
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, now := testStore(t), time.Now().UTC()
			manifest := receiptImportFixture(now, 1)
			mutate(&manifest)
			if len(manifest.Usages) > 0 && name != "too_many" {
				manifest.Usages = append([]FinancialUsage{receiptImportFixture(now, 99).Usages[0]}, manifest.Usages...)
			}
			before := receiptImportCounts(t, s)
			n, err := s.ImportCustomerServiceReceipts(context.Background(), manifest, 900007, now)
			require.ErrorIs(t, err, ErrConflict)
			require.Zero(t, n)
			require.Equal(t, before, receiptImportCounts(t, s))
		})
	}
}

func TestReceiptImportRejectsChangedReplayAndProductionCollision(t *testing.T) {
	for _, mode := range []string{"payload", "release", "operator", "production", "cost_tamper", "call_tamper"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
			manifest := receiptImportFixture(now, 1)
			operator := 900007
			if mode == "production" {
				c, err := s.CreateConversation(ctx, 12, "production-conversation", now.Add(-time.Minute))
				require.NoError(t, err)
				job, err := s.BeginCall(ctx, BeginInput{UserID: 12, ConversationID: c.ID, IdempotencyKey: "production-call", Content: "real customer", Model: "deepseek-v4.1-flash", Supplier: "xcloud", ChannelID: 8, Now: now.Add(-time.Minute)})
				require.NoError(t, err)
				manifest.Usages[0].CallID = job.CallID
			} else {
				_, err := s.ImportCustomerServiceReceipts(ctx, manifest, operator, now)
				require.NoError(t, err)
				switch mode {
				case "payload":
					manifest.Usages[0].UpstreamRequestID = "changed"
				case "release":
					manifest.Release = strings.Repeat("b", 40)
				case "operator":
					operator++
				case "cost_tamper":
					require.NoError(t, s.db.Exec("UPDATE support_call_receipts SET actual_cost_u = '0.9',actual_evidence = 'tampered'").Error)
				case "call_tamper":
					require.NoError(t, s.db.Exec("UPDATE support_calls SET environment = 'production'").Error)
				}
			}
			manifest.Usages = append([]FinancialUsage{receiptImportFixture(now, 99).Usages[0]}, manifest.Usages...)
			before := receiptImportCounts(t, s)
			n, err := s.ImportCustomerServiceReceipts(ctx, manifest, operator, now)
			require.ErrorIs(t, err, ErrConflict)
			require.Zero(t, n)
			require.Equal(t, before, receiptImportCounts(t, s))
			if mode == "cost_tamper" {
				var receipt callReceipt
				require.NoError(t, s.db.Take(&receipt).Error)
				require.Equal(t, "0.9", *receipt.ActualCostU, "import must not overwrite even corrupt costs")
			}
		})
	}
}

func TestReceiptImportRollsBackOnWriteFailure(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	manifest := receiptImportFixture(now, 1)
	manifest.Usages = append(manifest.Usages, receiptImportFixture(now, 2).Usages[0])
	require.NoError(t, s.db.Exec("CREATE TRIGGER reject_import_receipt BEFORE INSERT ON support_call_receipts WHEN NEW.call_id = '00000000000000000000000000000002' BEGIN SELECT RAISE(ABORT, 'test write failure'); END").Error)
	before := receiptImportCounts(t, s)
	n, err := s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now)
	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, before, receiptImportCounts(t, s))
}

func TestReceiptImportRejectsInvalidOperatorAndCanceledContext(t *testing.T) {
	s, now := testStore(t), time.Now().UTC()
	manifest := receiptImportFixture(now, 1)
	for _, operator := range []int{0, -1} {
		n, err := s.ImportCustomerServiceReceipts(context.Background(), manifest, operator, now)
		require.ErrorIs(t, err, ErrConflict)
		require.Zero(t, n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now)
	require.Error(t, err)
	require.Zero(t, n)
	require.Zero(t, receiptImportCounts(t, s)["support_calls"])
}

func TestReceiptImportAuditIsImmutableIncludingReplace(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	manifest := receiptImportFixture(now, 1)
	_, err := s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now)
	require.NoError(t, err)
	var audit receiptImportAudit
	require.NoError(t, s.db.Take(&audit).Error)
	require.Error(t, s.db.Model(&audit).Update("release", strings.Repeat("b", 40)).Error)
	require.Error(t, s.db.Delete(&audit).Error)
	require.Error(t, s.db.Exec("UPDATE support_receipt_import_audits SET release = ?", strings.Repeat("b", 40)).Error)
	require.Error(t, s.db.Exec("DELETE FROM support_receipt_import_audits").Error)
	require.Error(t, s.db.Exec("INSERT OR REPLACE INTO support_receipt_import_audits(call_id,hash,release,operator_telegram_id,created_at) VALUES(?,?,?,?,?)", audit.CallID, audit.Hash, strings.Repeat("b", 40), audit.OperatorTelegramID, audit.CreatedAt).Error)
	var after receiptImportAudit
	require.NoError(t, s.db.Take(&after).Error)
	require.Equal(t, audit, after)
}

func TestReceiptImportFiveCallsBoundaryAndMixedReplay(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	manifest := receiptImportFixture(now, 1)
	quote, credit := "0.1", "0.2"
	manifest.Usages[0].QuoteCostU, manifest.Usages[0].UpstreamCredit = &quote, &credit
	for id := 2; id <= 5; id++ {
		manifest.Usages = append(manifest.Usages, receiptImportFixture(now, id).Usages[0])
	}
	first := ReceiptImportManifest{Release: manifest.Release, Usages: manifest.Usages[:2]}
	n, err := s.ImportCustomerServiceReceipts(ctx, first, 900007, now)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	n, err = s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Equal(t, int64(5), receiptImportCounts(t, s)["support_calls"])
	rows, err := ReadFinancialUsage(ctx, s.db, now.Add(-time.Hour), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, manifest.Usages, rows)
}

func TestReceiptImportTimeZoneAndClockTolerance(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	manifest := receiptImportFixture(now, 1)
	zone := time.FixedZone("UTC+8", 8*3600)
	manifest.Usages[0].StartedAt = now.Add(-24 * time.Hour).In(zone)
	completed := now.Add(30 * time.Second).In(zone)
	manifest.Usages[0].CompletedAt = &completed
	n, err := s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now.In(zone))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now)
	require.NoError(t, err)
	require.Zero(t, n)
	manifest = receiptImportFixture(now, 2)
	completed = now.Add(30*time.Second + time.Nanosecond)
	manifest.Usages[0].CompletedAt = &completed
	_, err = s.ImportCustomerServiceReceipts(ctx, manifest, 900007, now)
	require.ErrorIs(t, err, ErrConflict)
}
