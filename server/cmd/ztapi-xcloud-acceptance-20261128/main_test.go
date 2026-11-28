package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var testNow = time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

func testInput() acceptanceInput {
	return acceptanceInput{BatchID: batchID, AttemptID: "verify-gpt62sol-001", OperatorID: 1,
		SourceModel: "gpt-6.2-sol", ChannelID: 7, Group: "gpt-plus",
		Expected:       expectedCAS{ConfigID: 200, Version: 2, SourceID: 300},
		KeyReferenceID: "limited-gpt-plus-001", LimitProofID: "supplier-limit-001",
		LimitProofSHA256: strings.Repeat("a", 64), EvidenceDir: filepath.Join(os.TempDir(), "evidence", "attempt-001")}
}

func testReservation(in acceptanceInput) reservation {
	claimDirectory := "/private/claims"
	if filepath.IsAbs(in.ReservationFile) {
		claimDirectory = filepath.Dir(in.ReservationFile)
	}
	credentialDigest := sha256.Sum256([]byte("fixture-secret-key"))
	return reservation{BatchID: in.BatchID, AttemptID: in.AttemptID, OperatorID: in.OperatorID,
		SourceModel: in.SourceModel, ChannelID: in.ChannelID, Group: in.Group, Expected: in.Expected,
		KeyReferenceID: in.KeyReferenceID, LimitProofID: in.LimitProofID, LimitProofSHA256: in.LimitProofSHA256,
		CredentialSHA256: hex.EncodeToString(credentialDigest[:]), ClaimDirectory: claimDirectory,
		ReservationID: "journal-reservation-001", State: "reserved", CapU: "0.50", BatchLimitU: "10", PerModelLimitU: "1",
		ReservedAt: testNow.Add(-time.Minute), ExpiresAt: testNow.Add(time.Hour)}
}

func jsonBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := common.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeAcceptanceStrict(t *testing.T) {
	in := testInput()
	raw := string(jsonBytes(t, in))
	if _, err := decodeInput(strings.NewReader(raw), "stage"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"unknown":        strings.Replace(raw, `"operator_id":1`, `"operator_id":1,"key":"secret"`, 1),
		"duplicate":      strings.Replace(raw, `"operator_id":1`, `"operator_id":1,"operator_id":1`, 1),
		"case":           strings.Replace(raw, `"batch_id"`, `"BATCH_ID"`, 1),
		"nested_case":    strings.Replace(raw, `"config_id"`, `"CONFIG_ID"`, 1),
		"missing_cas":    strings.Replace(raw, `"snapshot_id":0,`, "", 1),
		"null_cas":       strings.Replace(raw, `"snapshot_id":0`, `"snapshot_id":null`, 1),
		"other_operator": strings.Replace(raw, `"operator_id":1`, `"operator_id":2`, 1),
		"near_sku":       strings.Replace(raw, "gpt-6.2-sol", "gpt-6.2-sol-latest", 1),
		"wrong_group":    strings.Replace(raw, "gpt-plus", "new-vip", 1),
		"wrong_batch":    strings.Replace(raw, batchID, "other-batch", 1),
		"traversal":      strings.Replace(raw, "verify-gpt62sol-001", "../attempt", 1),
		"trailing":       raw + `{}`,
		"oversize":       raw + strings.Repeat(" ", inputLimit),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeInput(strings.NewReader(data), "stage"); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	in.ReservationFile, in.ReservationSHA256 = filepath.Join(os.TempDir(), "reservation.json"), strings.Repeat("b", 64)
	if _, err := decodeInput(bytes.NewReader(jsonBytes(t, in)), "stage"); err == nil {
		t.Fatal("stage accepted reservation")
	}
	if _, err := decodeInput(bytes.NewReader(jsonBytes(t, in)), "verify-paid"); err != nil {
		t.Fatal(err)
	}
	in.Expected.ConfigID = 0
	if _, err := decodeInput(bytes.NewReader(jsonBytes(t, in)), "verify-paid"); err == nil {
		t.Fatal("verify accepted new config")
	}
}

func TestReservationBoundAndExact(t *testing.T) {
	in := testInput()
	if err := validateReservation(in, testReservation(in), testNow); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*reservation){
		"credential_digest":            func(r *reservation) { r.CredentialSHA256 = "not-a-key-fingerprint" },
		"relative_claim_directory":     func(r *reservation) { r.ClaimDirectory = "relative" },
		"noncanonical_claim_directory": func(r *reservation) { r.ClaimDirectory = "/private/../claims" },
		"cap_zero":                     func(r *reservation) { r.CapU = "0" },
		"cap_negative":                 func(r *reservation) { r.CapU = "-0.1" },
		"cap_over":                     func(r *reservation) { r.CapU = "1.0001" },
		"cap_exponent":                 func(r *reservation) { r.CapU = "1e-1" },
		"batch_limit":                  func(r *reservation) { r.BatchLimitU = "11" },
		"per_model_limit":              func(r *reservation) { r.PerModelLimitU = "2" },
		"expired":                      func(r *reservation) { r.ExpiresAt = testNow },
		"future":                       func(r *reservation) { r.ReservedAt = testNow.Add(time.Second) },
		"state":                        func(r *reservation) { r.State = "released" },
		"attempt":                      func(r *reservation) { r.AttemptID = "other-attempt" },
		"key":                          func(r *reservation) { r.KeyReferenceID = "other-key" },
		"group":                        func(r *reservation) { r.Group = "new-vip" },
		"source":                       func(r *reservation) { r.SourceModel = "gpt-6.2-astra" },
		"cas":                          func(r *reservation) { r.Expected.Version++ },
		"proof":                        func(r *reservation) { r.LimitProofSHA256 = strings.Repeat("c", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			r := testReservation(in)
			mutate(&r)
			if validateReservation(in, r, testNow) == nil {
				t.Fatal("unsafe reservation accepted")
			}
		})
	}
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestStageHTTPFixtures(t *testing.T) {
	pricing, err := os.ReadFile(filepath.Join("..", "..", "model", "ztapi_xcloud_catalog_20261128.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := boundedClient(fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "www.xcloudtoken.com" || r.Method != "GET" {
			t.Fatal("unexpected target")
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("unbounded context")
		}
		if r.URL.Path == "/api/pricing" {
			if r.Header.Get("Authorization") != "" {
				t.Fatal("credential sent to pricing")
			}
			return response(200, string(pricing)), nil
		}
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Fatal("incorrect discovery request")
		}
		return response(200, `{"object":"list","data":[{"id":"gpt-6.2-sol","object":"model"}]}`), nil
	}))
	ids, _, err := fetchStage(context.Background(), client, "fixture-key")
	if err != nil || len(ids) != 1 || ids[0] != "gpt-6.2-sol" || calls != 2 {
		t.Fatalf("calls=%d ids=%v err=%v", calls, ids, err)
	}
	for name, body := range map[string]string{
		"empty": `{"data":[]}`, "duplicate": `{"data":[{"id":"x"},{"id":"x"}]}`,
		"duplicate_member": `{"data":[],"data":[{"id":"x"}]}`, "wrong_type": `{"data":[{"id":1}]}`,
		"large": strings.Repeat("x", responseLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			c := boundedClient(fixtureTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/pricing" {
					return response(200, string(pricing)), nil
				}
				return response(200, body), nil
			}))
			if _, _, err := fetchStage(context.Background(), c, "fixture-key"); err == nil {
				t.Fatal("unsafe models response accepted")
			}
		})
	}
	for _, code := range []int{302, 401, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			calls := 0
			c := boundedClient(fixtureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				res := response(code, "provider-secret")
				res.Header.Set("Location", "https://elsewhere.invalid/")
				return res, nil
			}))
			if _, _, err := fetchStage(context.Background(), c, "fixture-key"); err == nil || calls != 1 {
				t.Fatal("redirect/error accepted")
			}
		})
	}
	c := boundedClient(fixtureTransport(func(r *http.Request) (*http.Response, error) { return response(200, `{}`), nil }))
	if _, _, err := fetchStage(context.Background(), c, "fixture-key"); err == nil {
		t.Fatal("invalid live quote accepted")
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("operator filesystem safety is exercised on Linux/WSL")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writePrivate(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedFilesAndAttemptReplay(t *testing.T) {
	dir := privateDir(t)
	key := filepath.Join(dir, "key")
	writePrivate(t, key, []byte("fixture-key\n"))
	if _, err := readProtectedFile(key, 4096); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedFile(link, 4096); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := readProtectedFile(key, 2); err == nil {
		t.Fatal("oversized file accepted")
	}
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedFile(key, 4096); err == nil {
		t.Fatal("public key file accepted")
	}
	if err := os.Chmod(key, 0600); err != nil {
		t.Fatal(err)
	}
	in := testInput()
	in.EvidenceDir = filepath.Join(dir, "attempt")
	in.ReservationFile = filepath.Join(dir, "reservation.json")
	proof := testReservation(in)
	if err := createAttempt(in, "verify-paid", key, &proof); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(in.EvidenceDir, "result.json", map[string]any{"state": "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(in.EvidenceDir, "result.json", map[string]any{"state": "pass"}); err == nil {
		t.Fatal("artifact overwritten")
	}
	in.EvidenceDir = filepath.Join(dir, "different-evidence-dir")
	if err := createAttempt(in, "verify-paid", key, &proof); err == nil {
		t.Fatal("replay accepted with new evidence directory")
	}
	if _, err := os.Stat(in.EvidenceDir); !os.IsNotExist(err) {
		t.Fatal("replay created directory")
	}
}

func TestAttemptClaimSurvivesCredentialDirectoryChange(t *testing.T) {
	dir := privateDir(t)
	in := testInput()
	in.EvidenceDir = filepath.Join(dir, "first-evidence")
	in.ReservationFile = filepath.Join(dir, "reservation.json")
	proof := testReservation(in)
	var keys []string
	for _, name := range []string{"first-key-dir", "copied-key-dir"} {
		parent := filepath.Join(dir, name)
		if err := os.Mkdir(parent, 0700); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(parent, "key")
		writePrivate(t, key, []byte("fixture-secret-key"))
		keys = append(keys, key)
	}
	if err := createAttempt(in, "verify-paid", keys[0], &proof); err != nil {
		t.Fatal(err)
	}
	in.EvidenceDir = filepath.Join(dir, "second-evidence")
	if err := createAttempt(in, "verify-paid", keys[1], &proof); err == nil {
		t.Fatal("same immutable reservation replayed after copying credential to another directory")
	}
}

func TestRunRejectsCredentialSubstitutionBeforeDB(t *testing.T) {
	dir := privateDir(t)
	in := testInput()
	in.EvidenceDir = filepath.Join(dir, "evidence")
	in.ReservationFile = filepath.Join(dir, "reservation.json")
	raw := jsonBytes(t, testReservation(in))
	sum := sha256.Sum256(raw)
	in.ReservationSHA256 = hex.EncodeToString(sum[:])
	writePrivate(t, in.ReservationFile, raw)
	key := filepath.Join(dir, "key")
	writePrivate(t, key, []byte("fixture-other-key"))
	t.Setenv("XCLOUD_ACCEPTANCE_KEY_FILE", key)
	t.Setenv("SQL_DSN", "fixture-dsn")
	calls := 0
	h := hooks{Now: func() time.Time { return testNow }, OpenDB: func(string) (*gorm.DB, error) { calls++; return nil, errors.New("must not open") }}
	var out bytes.Buffer
	if run([]string{"--verify-paid"}, bytes.NewReader(jsonBytes(t, in)), &out, h) == 0 || calls != 0 {
		t.Fatalf("unbound credential reached database or dispatch: calls=%d", calls)
	}
	if _, err := os.Stat(in.EvidenceDir); !os.IsNotExist(err) {
		t.Fatal("unbound credential claimed an attempt")
	}
}

func testDB(t *testing.T, in acceptanceInput) *gorm.DB {
	t.Helper()
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", "offline-acceptance-fixture-master-key-20261128")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.Channel{}, &model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}); err != nil {
		t.Fatal(err)
	}
	public, provider, protocol, err := model.ZTAPIXcloudPublicIdentity20261128(in.SourceModel)
	if err != nil {
		t.Fatal(err)
	}
	base := "https://www.xcloudtoken.com"
	for _, row := range []any{
		&model.User{Id: 1, Role: common.RoleRootUser, Status: common.UserStatusEnabled, Username: "fixture-root"},
		&model.Channel{Id: in.ChannelID, Type: constant.ChannelTypeOpenAI, Key: "production-key-never-change", Status: common.ChannelStatusEnabled, ZTAPIManaged: true, BaseURL: &base, Group: "default", Models: in.SourceModel},
		&model.ZTAPIModelConfig{ID: in.Expected.ConfigID, Version: in.Expected.Version, PublicationSnapshotID: in.Expected.SnapshotID, SourceModel: in.SourceModel, PublicName: &public, ProviderFamily: provider, Protocol: protocol, EnabledGroups: `["default"]`},
		&model.ZTAPIModelPriceSource{ID: in.Expected.SourceID, ModelConfigID: in.Expected.ConfigID, SourceModel: in.SourceModel, SpendTier: in.Group, PricePolicy: string(model.ZTAPIPricePolicyXcloud20261128), Version: 1, CreatedAt: testNow.Unix()},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestPreflightRootRouteAndCAS(t *testing.T) {
	in := testInput()
	for name, mutate := range map[string]func(*gorm.DB, *acceptanceInput){
		"valid": func(*gorm.DB, *acceptanceInput) {},
		"not_root": func(db *gorm.DB, _ *acceptanceInput) {
			db.Model(&model.User{}).Where("id = 1").Update("role", common.RoleAdminUser)
		},
		"disabled_root":  func(db *gorm.DB, _ *acceptanceInput) { db.Model(&model.User{}).Where("id = 1").Update("status", 2) },
		"stale_version":  func(_ *gorm.DB, r *acceptanceInput) { r.Expected.Version++ },
		"stale_snapshot": func(_ *gorm.DB, r *acceptanceInput) { r.Expected.SnapshotID++ },
		"stale_source":   func(_ *gorm.DB, r *acceptanceInput) { r.Expected.SourceID++ },
		"different_source": func(db *gorm.DB, _ *acceptanceInput) {
			db.Model(&model.ZTAPIModelConfig{}).Where("id = 200").Update("source_model", "gpt-6.2-astra")
		},
		"mapped_old": func(db *gorm.DB, _ *acceptanceInput) {
			db.Model(&model.Channel{}).Where("id = 7").Update("model_mapping", `{"gpt-6.2-sol":"gpt-6.1-sol"}`)
		},
		"wrong_host": func(db *gorm.DB, _ *acceptanceInput) {
			db.Model(&model.Channel{}).Where("id = 7").Update("base_url", "https://elsewhere.invalid")
		},
		"unmanaged": func(db *gorm.DB, _ *acceptanceInput) {
			db.Model(&model.Channel{}).Where("id = 7").Update("ztapi_managed", false)
		},
		"duplicate_source": func(db *gorm.DB, r *acceptanceInput) {
			if err := db.Migrator().DropIndex(&model.ZTAPIModelConfig{}, "idx_ztapi_model_configs_source_model"); err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.ZTAPIModelConfig{ID: 201, SourceModel: r.SourceModel, Version: 1}).Error; err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := in
			db := testDB(t, r)
			mutate(db, &r)
			err := preflight(db, r, "verify-paid")
			if (err == nil) != (name == "valid") {
				t.Fatalf("preflight err=%v", err)
			}
		})
	}
}

func TestRunArchivesReceiptsAndNeverRetries(t *testing.T) {
	for _, scenario := range []string{"pass", "partial_failure", "nil_result", "zero_http", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			dir := privateDir(t)
			in := testInput()
			in.EvidenceDir = filepath.Join(dir, "evidence")
			in.ReservationFile = filepath.Join(dir, "reservation.json")
			proof := jsonBytes(t, testReservation(in))
			sum := sha256.Sum256(proof)
			in.ReservationSHA256 = hex.EncodeToString(sum[:])
			writePrivate(t, in.ReservationFile, proof)
			keyPath := filepath.Join(dir, "key")
			writePrivate(t, keyPath, []byte("fixture-secret-key"))
			t.Setenv("XCLOUD_ACCEPTANCE_KEY_FILE", keyPath)
			t.Setenv("SQL_DSN", "fixture-secret-dsn")
			db := testDB(t, in)
			calls := 0
			h := hooks{OpenDB: func(string) (*gorm.DB, error) { return db, nil }, Now: func() time.Time { return testNow },
				Verify: func(_ context.Context, channel int, source string, operator int, key string) (*model.ZTAPIModelVerification, error) {
					calls++
					if key != "fixture-secret-key" || channel != 7 || source != in.SourceModel || operator != 1 {
						t.Fatal("wrong verify arguments")
					}
					if scenario == "panic" {
						panic("fixture-secret-key")
					}
					if scenario == "nil_result" {
						return nil, errors.New("fixture-secret-key fixture-secret-dsn")
					}
					v := &model.ZTAPIModelVerification{ID: 900, ModelConfigID: in.Expected.ConfigID, ChannelID: 7, NonStreamingPassed: true, StreamingRequired: true, StreamingPassed: true, UsageReconciled: true,
						ProbeReceipts: []model.ZTAPIModelProbeReceipt20261128{
							{Mode: "nonstream", StatusCode: 200, RequestID: "req-one", UsageJSON: `{"input_tokens":2,"output_tokens":4}`, Complete: true},
							{Mode: "stream", StatusCode: 200, RequestID: "req-two", UsageJSON: `{"output_tokens":4,"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"output_tokens":4}}}`, Complete: true},
						}}
					if scenario == "partial_failure" {
						v.ProbeReceipts[1].Complete = false
						return v, errors.New("fixture-secret-key")
					}
					if scenario == "zero_http" {
						v.ProbeReceipts[1].StatusCode = 0
					}
					return v, nil
				}}
			var out bytes.Buffer
			exit := run([]string{"--verify-paid"}, bytes.NewReader(jsonBytes(t, in)), &out, h)
			if (exit == 0) != (scenario == "pass") || calls != 1 {
				t.Fatalf("exit=%d calls=%d out=%s", exit, calls, out.String())
			}
			raw, err := os.ReadFile(filepath.Join(in.EvidenceDir, "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("fixture-secret")) || strings.Contains(out.String(), "fixture-secret") {
				t.Fatal("secret leaked")
			}
			if !bytes.Contains(raw, []byte(`"supplier_billing_cap_enforced":false`)) {
				t.Fatal("missing limit disclaimer")
			}
			if scenario == "pass" || scenario == "partial_failure" || scenario == "zero_http" {
				if !bytes.Contains(raw, []byte(`"verification_id":900`)) || !bytes.Contains(raw, []byte(`"request_id":"req-two"`)) || !bytes.Contains(raw, []byte(`billing_usage`)) {
					t.Fatal("lost verification or stream receipt")
				}
			}
			out.Reset()
			in.EvidenceDir = filepath.Join(dir, "retry")
			if run([]string{"--verify-paid"}, bytes.NewReader(jsonBytes(t, in)), &out, h) == 0 || calls != 1 {
				t.Fatal("automatic/replayed paid call")
			}
		})
	}
}

func TestSafeReceiptArchivalBoundary(t *testing.T) {
	input := []model.ZTAPIModelProbeReceipt20261128{
		{Mode: "nonstream", StatusCode: 200, RequestID: "req-" + strings.Repeat("x", 250), Complete: true,
			UsageJSON: `{"output_tokens":0,"billing_usage":{"source":"claude_messages","semantic":"anthropic","claude_usage":{"output_tokens":4,"cache_creation":{"ephemeral_1h_input_tokens":5}}}}`},
		{Mode: "stream", StatusCode: 200, RequestID: "fixture-secret-key", Complete: true,
			UsageJSON: `{"input_tokens":3,"text":"sensitive-text","key":{},"billing_usage":{"source":"untrusted","semantic":"anthropic","claude_usage":{"output_tokens":-1}}}`},
	}
	got := safeReceipts(input, "fixture-secret-key", "fixture-dsn")
	if len(got) != 2 || got[0].RequestID != input[0].RequestID || !got[0].Complete || !strings.Contains(got[0].UsageJSON, `"output_tokens":4`) {
		t.Fatal("lost safe native metadata")
	}
	if got[1].Complete || got[1].RequestID != "" || strings.Contains(got[1].UsageJSON, "sensitive-text") || strings.Contains(got[1].UsageJSON, "untrusted") || strings.Contains(got[1].UsageJSON, `"key"`) || !strings.Contains(got[1].UsageJSON, `"input_tokens":3`) {
		t.Fatal("unsafe metadata retained or safe partial lost")
	}
	if input[1].RequestID != "fixture-secret-key" || !input[1].Complete {
		t.Fatal("mutated receipt input")
	}
}

func TestRunStageUsesOnlyFetchedIDs(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(strconv.FormatBool(failed), func(t *testing.T) {
			dir := privateDir(t)
			in := testInput()
			in.EvidenceDir = filepath.Join(dir, "stage")
			keyPath := filepath.Join(dir, "key")
			writePrivate(t, keyPath, []byte("fixture-secret-key"))
			t.Setenv("XCLOUD_ACCEPTANCE_KEY_FILE", keyPath)
			t.Setenv("SQL_DSN", "fixture-dsn")
			db := testDB(t, in)
			calls := 0
			pricing, err := os.ReadFile(filepath.Join("..", "..", "model", "ztapi_xcloud_catalog_20261128.json"))
			if err != nil {
				t.Fatal(err)
			}
			h := hooks{OpenDB: func(string) (*gorm.DB, error) { return db, nil }, Now: func() time.Time { return testNow },
				Client: boundedClient(fixtureTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/api/pricing" {
						return response(200, string(pricing)), nil
					}
					return response(200, `{"data":[{"id":"gpt-6.2-sol"},{"id":"other-upstream-model"}]}`), nil
				})),
				Stage: func(operator int, req model.ZTAPIXcloudStage20261128, ids []string, at time.Time) (*model.ZTAPIXcloudStaged20261128, error) {
					calls++
					if operator != 1 || req.ConfigID != 200 || req.Version != 2 || req.SourceID != 300 || req.ChannelID != 7 || req.Group != in.Group || req.SourceModel != in.SourceModel || len(ids) != 2 || ids[1] != "other-upstream-model" || at.IsZero() {
						t.Fatal("stage arguments not pinned to server fetch/CAS")
					}
					channel, err := model.GetChannelById(7, true)
					if err != nil || channel.Key != "production-key-never-change" {
						t.Fatal("production credential changed")
					}
					if failed {
						return nil, errors.New("fixture-secret-key")
					}
					return &model.ZTAPIXcloudStaged20261128{Config: model.ZTAPIModelConfig{ID: 200, Version: 3, SourceModel: in.SourceModel}, Source: model.ZTAPIModelPriceSource{ID: 301, ModelConfigID: 200, SourceModel: in.SourceModel, SpendTier: in.Group, PricePolicy: string(model.ZTAPIPricePolicyXcloud20261128)}, DiscoveryID: 400}, nil
				}}
			var out bytes.Buffer
			if exit := run([]string{"--stage"}, bytes.NewReader(jsonBytes(t, in)), &out, h); (exit == 0) == failed || calls != 1 {
				t.Fatalf("exit=%d calls=%d out=%s", exit, calls, out.String())
			}
			raw, err := os.ReadFile(filepath.Join(in.EvidenceDir, "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("fixture-secret-key")) || bytes.Contains(raw, []byte("production-key-never-change")) {
				t.Fatal("credential leaked")
			}
			if !failed && (!bytes.Contains(raw, []byte(`"source_id":301`)) || !bytes.Contains(raw, []byte(`"key_reference_id":"limited-gpt-plus-001"`)) || !bytes.Contains(raw, []byte(`"group":"gpt-plus"`))) {
				t.Fatal("incomplete stage metadata")
			}
		})
	}
}

func TestRejectInvalidModeAndTamperedReservationBeforeDB(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"--publish"}, {"--restore"}, {"--stage", "--verify-paid"}, {"--stage=true"}} {
		if run(args, strings.NewReader("{}"), &out, hooks{}) == 0 {
			t.Fatal("unsafe mode accepted")
		}
	}
	dir := privateDir(t)
	in := testInput()
	in.EvidenceDir = filepath.Join(dir, "evidence")
	in.ReservationFile = filepath.Join(dir, "reservation.json")
	writePrivate(t, in.ReservationFile, jsonBytes(t, testReservation(in)))
	in.ReservationSHA256 = strings.Repeat("f", 64)
	keyPath := filepath.Join(dir, "key")
	writePrivate(t, keyPath, []byte("fixture-secret-key"))
	t.Setenv("XCLOUD_ACCEPTANCE_KEY_FILE", keyPath)
	t.Setenv("SQL_DSN", "fixture-dsn")
	calls := 0
	h := hooks{Now: func() time.Time { return testNow }, OpenDB: func(string) (*gorm.DB, error) { calls++; return nil, errors.New("must not open") }}
	if run([]string{"--verify-paid"}, bytes.NewReader(jsonBytes(t, in)), &out, h) == 0 || calls != 0 {
		t.Fatal("tampered reservation reached DB")
	}
	if _, err := os.Stat(in.EvidenceDir); !os.IsNotExist(err) {
		t.Fatal("tampered reservation created attempt")
	}
}

func TestPreflightOnlyLocksTargetForPaidVerification(t *testing.T) {
	for _, mode := range []string{"stage", "verify-paid"} {
		t.Run(mode, func(t *testing.T) {
			in := testInput()
			db := testDB(t, in)
			locked := 0
			inventory := 0
			err := db.Callback().Query().Before("gorm:query").Register("acceptance_lock_scope", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Model.(*model.ZTAPIModelConfig); !ok {
					if _, ok = tx.Statement.Dest.(*model.ZTAPIModelConfig); !ok {
						if _, ok = tx.Statement.Dest.(*[]model.ZTAPIModelConfig); !ok {
							return
						}
					}
				}
				if lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking); ok && lock.Strength == "UPDATE" {
					locked++
					where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
					if !ok || len(where.Exprs) != 1 {
						t.Fatal("unbounded config lock")
					}
					expr, ok := where.Exprs[0].(clause.Expr)
					if !ok || expr.SQL != "id = ?" || len(expr.Vars) != 1 || expr.Vars[0] != in.Expected.ConfigID {
						t.Fatal("config lock is not exact target")
					}
				} else if _, ok := tx.Statement.Dest.(*[]model.ZTAPIModelConfig); ok {
					inventory++
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Transaction(func(tx *gorm.DB) error { return preflight(tx, in, mode) }); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "verify-paid" {
				want = 1
			}
			if locked != want || inventory != 1 {
				t.Fatalf("locked=%d inventory=%d", locked, inventory)
			}
		})
	}
}
