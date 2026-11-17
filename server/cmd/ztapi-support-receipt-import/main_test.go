package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/finance"
	"github.com/QuantumNous/new-api/support"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func importCLIStore(t *testing.T) (string, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "support.sqlite")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
	_, err = support.NewStore(db)
	require.NoError(t, err)
	return path, db
}

func importCLIManifest(t *testing.T, now time.Time) (string, receiptImportOptions) {
	t.Helper()
	completed := now.Add(-time.Minute)
	input, output, cache := int64(123), int64(4), int64(5)
	quote, credit := "0.00002", "0.00004"
	manifest := support.ReceiptImportManifest{Release: strings.Repeat("a", 40), Usages: []support.FinancialUsage{{
		CallID: strings.Repeat("b", 32), StartedAt: now.Add(-2 * time.Minute), CompletedAt: &completed,
		ChannelID: 8, Supplier: "xcloud", Model: "deepseek-v4.1-flash", Purpose: "customer_service",
		Environment: "telegram-acceptance-v2", Outcome: "completed", Dispatched: true, InputTokenScope: "inclusive",
		InputTokens: &input, OutputTokens: &output, CacheReadTokens: &cache, QuoteCostU: &quote, UpstreamCredit: &credit,
		UpstreamRequestID: "private-request-do-not-print", QuoteEvidence: "private-evidence-do-not-print",
	}}}
	raw, err := common.Marshal(manifest)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	protectTestManifest(t, path)
	hash := sha256.Sum256(raw)
	return path, receiptImportOptions{ManifestPath: path, SHA256: hex.EncodeToString(hash[:]), OperatorID: "900007"}
}

func TestImportCLIIdempotentAndVisibleToExistingReader(t *testing.T) {
	path, reader := importCLIStore(t)
	now := time.Now().UTC()
	_, args := importCLIManifest(t, now)
	var out bytes.Buffer
	require.NoError(t, runReceiptImport(context.Background(), args, path, &out, now))
	require.JSONEq(t, `{"imported":1,"skipped":0,"total":1}`, strings.TrimSpace(out.String()))
	rows, err := support.ReadFinancialUsage(context.Background(), reader, now.Add(-time.Hour), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, rows, 1, "the already-open finance reader sees appended support evidence")
	require.Nil(t, rows[0].ActualCostU)
	require.Equal(t, "0.00002", *rows[0].QuoteCostU)
	month := rows[0].CompletedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01")
	report, err := finance.Calculate(finance.Snapshot{QuotaPerUnit: 500000, SupportSourceStatus: finance.SupportSourceAvailable, SupportUsages: rows}, nil, month, now)
	require.NoError(t, err)
	require.Len(t, report.CustomerServiceCosts, 1)
	require.Equal(t, "estimate", report.CustomerServiceCosts[0].Status)
	require.Equal(t, "0.000020", report.CustomerServiceEstimateTotal, "original cash quote is not discounted twice")
	require.Equal(t, "0.000000", report.CustomerServiceConfirmedTotal)
	require.Empty(t, report.Receipts, "support imports create no customer revenue or settlement lineage")
	require.Empty(t, report.TestCosts)
	out.Reset()
	require.NoError(t, runReceiptImport(context.Background(), args, path, &out, now))
	require.JSONEq(t, `{"imported":0,"skipped":1,"total":1}`, strings.TrimSpace(out.String()))
	require.NotContains(t, out.String(), "private-")
}

func TestImportCLILeaseBlocksBotOwnedStore(t *testing.T) {
	path, db := importCLIStore(t)
	now := time.Now().UTC()
	_, args := importCLIManifest(t, now)
	unlock, err := lockStore(path + ".runtime.lock")
	require.NoError(t, err)
	defer unlock()
	var out bytes.Buffer
	require.Error(t, runReceiptImport(context.Background(), args, path, &out, now))
	require.Empty(t, out.String())
	var count int64
	require.NoError(t, db.Table("support_calls").Count(&count).Error)
	require.Zero(t, count)
	require.False(t, db.Migrator().HasTable("support_receipt_import_audits"))
	unlock()
	require.NoError(t, runReceiptImport(context.Background(), args, path, &out, now))
}

func TestImportCLIRejectsUntrustedManifestWithoutWriting(t *testing.T) {
	for _, mode := range []string{"wrong_hash", "tampered", "relative_manifest", "directory", "unprotected", "oversize", "unknown_field", "duplicate_member", "trailing_json", "missing_operator", "zero_operator", "negative_operator", "invalid_operator", "invalid_batch"} {
		t.Run(mode, func(t *testing.T) {
			path, db := importCLIStore(t)
			now := time.Now().UTC()
			manifestPath, args := importCLIManifest(t, now)
			var replacement []byte
			switch mode {
			case "wrong_hash":
				args.SHA256 = strings.Repeat("0", 64)
			case "tampered":
				require.NoError(t, os.WriteFile(manifestPath, []byte("tampered-private-data"), 0600))
			case "relative_manifest":
				args.ManifestPath = "manifest.json"
			case "directory":
				args.ManifestPath = filepath.Dir(manifestPath)
			case "unprotected":
				publicTestManifest(t, manifestPath)
			case "oversize":
				replacement = bytes.Repeat([]byte("x"), (64<<10)+1)
			case "unknown_field":
				replacement = []byte(`{"release":"private-do-not-print","usages":[],"settlement_id":123}`)
			case "duplicate_member":
				replacement = []byte(`{"release":"a","release":"b","usages":[]}`)
			case "trailing_json":
				replacement = []byte(`{"release":"a","usages":[]} {}`)
			case "missing_operator":
				args.OperatorID = ""
			case "zero_operator":
				args.OperatorID = "0"
			case "negative_operator":
				args.OperatorID = "-1"
			case "invalid_operator":
				args.OperatorID = "private-invalid-operator"
			case "invalid_batch":
				replacement = []byte(`{"release":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","usages":[]}`)
			}
			if replacement != nil {
				require.NoError(t, os.WriteFile(manifestPath, replacement, 0600))
				hash := sha256.Sum256(replacement)
				args.SHA256 = hex.EncodeToString(hash[:])
			}
			var out bytes.Buffer
			err := runReceiptImport(context.Background(), args, path, &out, now)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private-")
			require.Empty(t, out.String())
			var count int64
			require.NoError(t, db.Table("support_calls").Count(&count).Error)
			require.Zero(t, count)
			require.False(t, db.Migrator().HasTable("support_receipt_import_audits"))
			unlock, err := lockStore(path + ".runtime.lock")
			require.NoError(t, err, "all failed paths release the lease")
			unlock()
		})
	}
}

func TestImportCLIRequiresExistingAbsoluteWALSupportStore(t *testing.T) {
	for _, mode := range []string{"empty", "relative", "missing", "directory", "not_wal", "incomplete_schema"} {
		t.Run(mode, func(t *testing.T) {
			path, db := importCLIStore(t)
			now := time.Now().UTC()
			_, args := importCLIManifest(t, now)
			switch mode {
			case "empty":
				path = ""
			case "relative":
				path = "support.sqlite"
			case "missing":
				path = filepath.Join(t.TempDir(), "does-not-exist.sqlite")
			case "directory":
				path = filepath.Dir(path)
			case "not_wal":
				require.NoError(t, db.Exec("PRAGMA journal_mode=DELETE").Error)
			case "incomplete_schema":
				require.NoError(t, db.Exec("DROP TABLE support_tickets").Error)
			}
			var out bytes.Buffer
			require.Error(t, runReceiptImport(context.Background(), args, path, &out, now))
			require.Empty(t, out.String())
			if mode == "missing" {
				_, err := os.Stat(path)
				require.True(t, os.IsNotExist(err))
			}
			if mode == "incomplete_schema" {
				require.False(t, db.Migrator().HasTable("support_tickets"), "NewStore migrations must roll back")
			}
		})
	}
}

func TestImportCLIRejectsSymlinkManifest(t *testing.T) {
	now := time.Now().UTC()
	manifest, args := importCLIManifest(t, now)
	alias := filepath.Join(t.TempDir(), "alias.json")
	if err := os.Symlink(manifest, alias); err != nil {
		t.Skip("symlink privilege unavailable")
	}
	args.ManifestPath = alias
	path, _ := importCLIStore(t)
	var out bytes.Buffer
	require.Error(t, runReceiptImport(context.Background(), args, path, &out, now))
}

func TestImportStoreLeaseExclusiveAndReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "support.sqlite.runtime.lock")
	unlock, err := lockStore(path)
	require.NoError(t, err)
	_, err = lockStore(path)
	require.Error(t, err)
	unlock()
	unlock()
	unlock, err = lockStore(path)
	require.NoError(t, err)
	unlock()
}

func TestImportCLIEnvironmentInterface(t *testing.T) {
	if os.Getenv("TASK4_IMPORT_ENV_HELPER") == "1" {
		os.Args = []string{os.Args[0]}
		main()
		os.Exit(0)
	}
	path, _ := importCLIStore(t)
	manifest, args := importCLIManifest(t, time.Now().UTC())
	cmd := exec.Command(os.Args[0], "-test.run=^TestImportCLIEnvironmentInterface$")
	cmd.Env = append(os.Environ(), "TASK4_IMPORT_ENV_HELPER=1", "SUPPORT_IMPORT_STORE="+path,
		"SUPPORT_IMPORT_MANIFEST="+manifest, "SUPPORT_IMPORT_SHA256="+args.SHA256, "SUPPORT_IMPORT_OPERATOR_ID=900007")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.JSONEq(t, `{"imported":1,"skipped":0,"total":1}`, strings.TrimSpace(string(output)))
}
