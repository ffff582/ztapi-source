// This direct operator command has no migration, publishing or restore mode.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	batchID       = "xcloud-sync-20261128"
	upstream      = "https://www.xcloudtoken.com"
	inputLimit    = 32 << 10
	responseLimit = 4 << 20
)

var (
	errRejected       = errors.New("operator acceptance rejected")
	identifier        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,95}$`)
	attemptIdentifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)
	receiptIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
	digestPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	amountPattern     = regexp.MustCompile(`^(0|1)(\.[0-9]{1,8})?$`)
)

type expectedCAS struct {
	ConfigID   int    `json:"config_id"`
	Version    uint64 `json:"version"`
	SnapshotID int64  `json:"snapshot_id"`
	SourceID   int64  `json:"source_id"`
}

type acceptanceInput struct {
	BatchID           string      `json:"batch_id"`
	AttemptID         string      `json:"attempt_id"`
	OperatorID        int         `json:"operator_id"`
	SourceModel       string      `json:"source_model"`
	ChannelID         int         `json:"channel_id"`
	Group             string      `json:"group"`
	Expected          expectedCAS `json:"expected"`
	KeyReferenceID    string      `json:"key_reference_id"`
	LimitProofID      string      `json:"limit_proof_id"`
	LimitProofSHA256  string      `json:"limit_proof_sha256"`
	EvidenceDir       string      `json:"evidence_dir"`
	ReservationFile   string      `json:"reservation_file,omitempty"`
	ReservationSHA256 string      `json:"reservation_sha256,omitempty"`
}

// A protected external journal attestation, not a supplier billing-cap mechanism.
type reservation struct {
	BatchID          string      `json:"batch_id"`
	AttemptID        string      `json:"attempt_id"`
	OperatorID       int         `json:"operator_id"`
	SourceModel      string      `json:"source_model"`
	ChannelID        int         `json:"channel_id"`
	Group            string      `json:"group"`
	Expected         expectedCAS `json:"expected"`
	KeyReferenceID   string      `json:"key_reference_id"`
	LimitProofID     string      `json:"limit_proof_id"`
	LimitProofSHA256 string      `json:"limit_proof_sha256"`
	CredentialSHA256 string      `json:"credential_sha256"`
	ClaimDirectory   string      `json:"claim_directory"`
	ReservationID    string      `json:"reservation_id"`
	State            string      `json:"state"`
	CapU             string      `json:"cap_u"`
	BatchLimitU      string      `json:"batch_limit_u"`
	PerModelLimitU   string      `json:"per_model_limit_u"`
	ReservedAt       time.Time   `json:"reserved_at"`
	ExpiresAt        time.Time   `json:"expires_at"`
}

type outcome struct {
	State                      string                                 `json:"state"`
	BatchID                    string                                 `json:"batch_id"`
	AttemptID                  string                                 `json:"attempt_id"`
	Mode                       string                                 `json:"mode"`
	SourceModel                string                                 `json:"source_model"`
	ChannelID                  int                                    `json:"channel_id"`
	Group                      string                                 `json:"group"`
	KeyReferenceID             string                                 `json:"key_reference_id"`
	LimitProofID               string                                 `json:"limit_proof_id"`
	LimitProofSHA256           string                                 `json:"limit_proof_sha256"`
	ReservationID              string                                 `json:"reservation_id,omitempty"`
	ReservationSHA256          string                                 `json:"reservation_sha256,omitempty"`
	ConfigID                   int                                    `json:"config_id,omitempty"`
	Version                    uint64                                 `json:"version,omitempty"`
	SnapshotID                 int64                                  `json:"snapshot_id"`
	SourceID                   int64                                  `json:"source_id,omitempty"`
	DiscoveryID                int64                                  `json:"discovery_id,omitempty"`
	VerificationID             int64                                  `json:"verification_id,omitempty"`
	ProbeReceipts              []model.ZTAPIModelProbeReceipt20261128 `json:"probe_receipts"`
	SupplierBillingCapEnforced bool                                   `json:"supplier_billing_cap_enforced"`
	FinishedAt                 time.Time                              `json:"finished_at"`
}

type hooks struct {
	OpenDB func(string) (*gorm.DB, error)
	Client *http.Client
	Stage  func(int, model.ZTAPIXcloudStage20261128, []string, time.Time) (*model.ZTAPIXcloudStaged20261128, error)
	Verify func(context.Context, int, string, int, string) (*model.ZTAPIModelVerification, error)
	Now    func() time.Time
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, hooks{
		OpenDB: func(dsn string) (*gorm.DB, error) {
			common.UsingMySQL, common.UsingSQLite, common.UsingPostgreSQL = true, false, false
			return gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		}, Client: boundedClient(nil), Stage: model.StageZTAPIXcloud20261128,
		Verify: service.VerifyZTAPIXcloud20261128WithCredential, Now: time.Now,
	}))
}

func boundedRead(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || len(b) == 0 || int64(len(b)) > limit {
		return nil, errRejected
	}
	return b, nil
}

func strictObject(raw []byte, dst any, required, optional string) error {
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return errRejected
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(raw, &fields) != nil || fields == nil {
		return errRejected
	}
	allowed := strings.Fields(required + " " + optional)
	for key, value := range fields {
		if !slices.Contains(allowed, key) || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errRejected
		}
	}
	for _, key := range strings.Fields(required) {
		if _, ok := fields[key]; !ok {
			return errRejected
		}
	}
	if expected, ok := fields["expected"]; ok {
		var cas expectedCAS
		if strictObject(expected, &cas, "config_id version snapshot_id source_id", "") != nil {
			return errRejected
		}
	}
	if common.DecodeJsonStrict(bytes.NewReader(raw), dst) != nil {
		return errRejected
	}
	return nil
}

func decodeInput(r io.Reader, mode string) (acceptanceInput, error) {
	var in acceptanceInput
	raw, err := boundedRead(r, inputLimit)
	if err != nil || (mode != "stage" && mode != "verify-paid") {
		return in, errRejected
	}
	required := "batch_id attempt_id operator_id source_model channel_id group expected key_reference_id limit_proof_id limit_proof_sha256 evidence_dir"
	if mode == "verify-paid" {
		required += " reservation_file reservation_sha256"
	}
	if strictObject(raw, &in, required, "") != nil {
		return in, errRejected
	}
	group, ok := model.ZTAPIXcloudChannelGroup20261128(in.ChannelID)
	_, quoteErr := model.ZTAPIXcloudQuoteFor20261128(in.SourceModel, in.Group, 0)
	if in.BatchID != batchID || in.OperatorID != 1 || !attemptIdentifier.MatchString(in.AttemptID) ||
		!ok || group != in.Group || quoteErr != nil || !identifier.MatchString(in.KeyReferenceID) ||
		!identifier.MatchString(in.LimitProofID) || !digestPattern.MatchString(in.LimitProofSHA256) ||
		!filepath.IsAbs(in.EvidenceDir) || filepath.Clean(in.EvidenceDir) != in.EvidenceDir ||
		in.Expected.ConfigID < 0 || in.Expected.SnapshotID < 0 || in.Expected.SourceID < 0 {
		return in, errRejected
	}
	if in.Expected.ConfigID == 0 {
		if mode != "stage" || in.Expected != (expectedCAS{}) {
			return in, errRejected
		}
	} else if in.Expected.Version == 0 {
		return in, errRejected
	}
	if mode == "verify-paid" && (in.Expected.SourceID == 0 || !filepath.IsAbs(in.ReservationFile) || !digestPattern.MatchString(in.ReservationSHA256)) {
		return in, errRejected
	}
	return in, nil
}

func validateReservation(in acceptanceInput, r reservation, now time.Time) error {
	cap, ok := new(big.Rat).SetString(r.CapU)
	if !amountPattern.MatchString(r.CapU) || !ok || cap.Sign() <= 0 || cap.Cmp(big.NewRat(1, 1)) > 0 ||
		!digestPattern.MatchString(r.CredentialSHA256) || !filepath.IsAbs(r.ClaimDirectory) || filepath.Clean(r.ClaimDirectory) != r.ClaimDirectory ||
		r.BatchLimitU != "10" || r.PerModelLimitU != "1" || r.State != "reserved" ||
		!identifier.MatchString(r.ReservationID) || r.BatchID != in.BatchID || r.AttemptID != in.AttemptID ||
		r.OperatorID != in.OperatorID || r.SourceModel != in.SourceModel || r.ChannelID != in.ChannelID ||
		r.Group != in.Group || r.Expected != in.Expected || r.KeyReferenceID != in.KeyReferenceID ||
		r.LimitProofID != in.LimitProofID || r.LimitProofSHA256 != in.LimitProofSHA256 ||
		r.ReservedAt.IsZero() || r.ReservedAt.After(now) || !r.ExpiresAt.After(now) || !r.ExpiresAt.After(r.ReservedAt) {
		return errRejected
	}
	return nil
}

func boundedClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = &http.Transport{Proxy: nil, DisableCompression: true,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func fetchStage(ctx context.Context, client *http.Client, key string) ([]string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	get := func(path, credential string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream+path, nil)
		if err != nil {
			return nil, errRejected
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, errRejected
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, errRejected
		}
		return boundedRead(res.Body, responseLimit)
	}
	raw, err := get("/api/pricing", "")
	if err != nil || model.ValidateZTAPIXcloudLiveQuote20261128(raw) != nil {
		return nil, time.Time{}, errRejected
	}
	raw, err = get("/v1/models", key)
	if err != nil || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return nil, time.Time{}, errRejected
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &list) != nil || len(list.Data) == 0 || len(list.Data) > 10000 {
		return nil, time.Time{}, errRejected
	}
	ids := make([]string, 0, len(list.Data))
	seen := make(map[string]bool)
	for _, row := range list.Data {
		if row.ID == "" || len(row.ID) > 256 || strings.TrimSpace(row.ID) != row.ID || strings.ContainsAny(row.ID, "\r\n\x00") || seen[row.ID] {
			return nil, time.Time{}, errRejected
		}
		seen[row.ID] = true
		ids = append(ids, row.ID)
	}
	return ids, time.Now().UTC(), nil
}

// Linux ownership and canonical private parents are mandatory. No Windows ACL
// approximation: run this operator command under Linux/WSL with private files.
func privateInfo(path string, directory bool) (os.FileInfo, error) {
	if runtime.GOOS != "linux" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errRejected
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errRejected
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm()&0077 != 0 {
		return nil, errRejected
	}
	stat := reflect.ValueOf(info.Sys())
	if stat.Kind() != reflect.Pointer || stat.IsNil() {
		return nil, errRejected
	}
	uid := stat.Elem().FieldByName("Uid")
	if !uid.IsValid() || !uid.CanUint() || uid.Uint() != uint64(os.Getuid()) {
		return nil, errRejected
	}
	return info, nil
}

func readProtectedFile(path string, limit int64) ([]byte, error) {
	if _, err := privateInfo(filepath.Dir(path), true); err != nil {
		return nil, errRejected
	}
	before, err := privateInfo(path, false)
	if err != nil || before.Size() > limit {
		return nil, errRejected
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errRejected
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errRejected
	}
	data, err := boundedRead(f, limit)
	after, statErr := privateInfo(path, false)
	if err != nil || statErr != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, errRejected
	}
	return data, nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return errRejected
	}
	defer f.Close()
	return f.Sync()
}

// Link commits a fully synced temporary file without overwriting any artifact.
func atomicJSON(dir, name string, value any) error {
	raw, err := common.Marshal(value)
	if err != nil {
		return errRejected
	}
	f, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return errRejected
	}
	temp := f.Name()
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errRejected
	}
	if err = os.Link(temp, filepath.Join(dir, name)); err != nil {
		return errRejected
	}
	if err = os.Remove(temp); err != nil {
		return errRejected
	}
	return syncDir(dir)
}

func createAttempt(in acceptanceInput, mode, keyPath string, proof *reservation) error {
	if _, err := privateInfo(filepath.Dir(in.EvidenceDir), true); err != nil {
		return errRejected
	}
	if _, err := privateInfo(filepath.Dir(keyPath), true); err != nil {
		return errRejected
	}
	if _, err := os.Lstat(in.EvidenceDir); !os.IsNotExist(err) {
		return errRejected
	}
	claimDirectory := filepath.Dir(keyPath)
	if mode == "verify-paid" {
		if proof == nil {
			return errRejected
		}
		claimDirectory = proof.ClaimDirectory
		if _, err := privateInfo(claimDirectory, true); err != nil {
			return errRejected
		}
	}
	// The immutable reservation fixes this namespace even when its credential
	// or receipt files are copied elsewhere. Never remove a consumed claim.
	lock := filepath.Join(claimDirectory, "."+batchID+"-"+in.AttemptID+".lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errRejected
	}
	data, err := common.Marshal(struct {
		Mode  string          `json:"mode"`
		Input acceptanceInput `json:"input"`
	}{mode, in})
	if err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || syncDir(filepath.Dir(lock)) != nil {
		return errRejected
	}
	if os.Mkdir(in.EvidenceDir, 0700) != nil || syncDir(filepath.Dir(in.EvidenceDir)) != nil {
		return errRejected
	}
	return atomicJSON(in.EvidenceDir, "request.json", struct {
		Mode                       string          `json:"mode"`
		Input                      acceptanceInput `json:"input"`
		Reservation                *reservation    `json:"reservation,omitempty"`
		SupplierBillingCapEnforced bool            `json:"supplier_billing_cap_enforced"`
	}{mode, in, proof, false})
}

func preflight(db *gorm.DB, in acceptanceInput, mode string) error {
	// Take the current-row lock before any consistent read establishes a MySQL
	// repeatable-read snapshot, including when a concurrent Stage just committed.
	if mode == "verify-paid" {
		var target model.ZTAPIModelConfig
		if db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", in.Expected.ConfigID).First(&target).Error != nil ||
			target.SourceModel != in.SourceModel || target.Version != in.Expected.Version || target.PublicationSnapshotID != in.Expected.SnapshotID {
			return errRejected
		}
	}
	var user model.User
	if db.Select("id", "role", "status").First(&user, in.OperatorID).Error != nil || user.Id != 1 || user.Role != common.RoleRootUser || user.Status != common.UserStatusEnabled {
		return errRejected
	}
	// Identity inventory is deliberately unlocked. Publication independently
	// rechecks global alias ownership; only this SKU stays locked during a probe.
	var configs []model.ZTAPIModelConfig
	if db.Find(&configs).Error != nil {
		return errRejected
	}
	plans, err := model.BuildZTAPIXcloudIdentityPlan20261128(configs)
	if err != nil {
		return errRejected
	}
	idx := slices.IndexFunc(plans, func(p model.ZTAPIXcloudIdentityPlan20261128) bool { return p.SourceModel == in.SourceModel })
	if idx < 0 || plans[idx].ExistingConfigID != in.Expected.ConfigID || plans[idx].ExistingVersion != in.Expected.Version {
		return errRejected
	}
	var channel model.Channel
	if db.Select("id", "type", "status", "base_url", "group", "model_mapping", "ztapi_managed").First(&channel, in.ChannelID).Error != nil ||
		!channel.ZTAPIManaged || channel.Status != common.ChannelStatusEnabled || channel.Type != constant.ChannelTypeOpenAI || !slices.Contains(channel.GetGroups(), "default") {
		return errRejected
	}
	mapping := map[string]string{}
	if raw := strings.TrimSpace(channel.GetModelMapping()); raw != "" {
		if common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil || common.UnmarshalJsonStr(raw, &mapping) != nil {
			return errRejected
		}
	}
	physical := in.SourceModel
	seen := map[string]bool{}
	for mapping[physical] != "" && mapping[physical] != physical {
		if seen[physical] {
			return errRejected
		}
		seen[physical] = true
		physical = mapping[physical]
	}
	if model.ValidateZTAPIXcloudRoute20261128(in.SourceModel, channel.Id, channel.GetBaseURL(), physical) != nil {
		return errRejected
	}
	if in.Expected.ConfigID == 0 {
		if mode != "stage" {
			return errRejected
		}
		return nil
	}
	idx = slices.IndexFunc(configs, func(c model.ZTAPIModelConfig) bool { return c.ID == in.Expected.ConfigID })
	if idx < 0 || configs[idx].SourceModel != in.SourceModel || configs[idx].PublicationSnapshotID != in.Expected.SnapshotID {
		return errRejected
	}
	var latest model.ZTAPIModelPriceSource
	if db.Where("model_config_id = ?", in.Expected.ConfigID).Order("version DESC, id DESC").Limit(1).Find(&latest).Error != nil || latest.ID != in.Expected.SourceID {
		return errRejected
	}
	if mode == "verify-paid" && (latest.SourceModel != in.SourceModel || latest.SpendTier != in.Group || latest.PricePolicy != string(model.ZTAPIPricePolicyXcloud20261128)) {
		return errRejected
	}
	return nil
}

func containsSecret(v any, secrets ...string) bool {
	raw, err := common.Marshal(v)
	if err != nil {
		return true
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		encoded, _ := common.Marshal(secret)
		if bytes.Contains(raw, []byte(secret)) || (len(encoded) > 2 && bytes.Contains(raw, encoded[1:len(encoded)-1])) {
			return true
		}
	}
	return false
}

// Defense in depth at the archival boundary. Preserve native counters, never
// derive totals, infer cache TTL, or serialize arbitrary provider strings.
func safeReceipts(receipts []model.ZTAPIModelProbeReceipt20261128, key, dsn string) []model.ZTAPIModelProbeReceipt20261128 {
	result := make([]model.ZTAPIModelProbeReceipt20261128, 0, len(receipts))
	for _, r := range receipts {
		if r.Mode != "stream" && r.Mode != "nonstream" {
			r.Mode = "unknown"
			r.Complete = false
		}
		if r.StatusCode < 0 || r.StatusCode > 599 {
			r.StatusCode = 0
			r.Complete = false
		}
		if r.RequestID != "" && (!receiptIdentifier.MatchString(r.RequestID) || common.RedactSecrets(r.RequestID) != r.RequestID || strings.HasPrefix(strings.ToLower(r.RequestID), "sk-") || containsSecret(r.RequestID, key, dsn)) {
			r.RequestID = ""
			r.Complete = false
		}
		if r.UsageJSON != "" {
			var usage map[string]any
			if len(r.UsageJSON) > inputLimit || common.RejectDuplicateJsonObjectMembers(strings.NewReader(r.UsageJSON)) != nil || common.DecodeJsonStrict(strings.NewReader(r.UsageJSON), &usage) != nil || usage == nil {
				r.UsageJSON = ""
				r.Complete = false
			} else {
				safe, complete := safeUsage(usage, "")
				if !complete {
					r.Complete = false
				}
				raw, _ := common.Marshal(safe)
				r.UsageJSON = string(raw)
			}
		}
		if r.StatusCode == 0 || r.RequestID == "" || r.UsageJSON == "" {
			r.Complete = false
		}
		result = append(result, r)
	}
	return result
}

func safeUsage(input map[string]any, prefix string) (map[string]any, bool) {
	safe := map[string]any{}
	complete := true
	const numeric = " prompt_tokens completion_tokens total_tokens input_tokens output_tokens cache_read_input_tokens cache_creation_input_tokens cache_write_tokens cache_write_5m_tokens cache_write_1h_tokens claude_cache_creation_5_m_tokens claude_cache_creation_1_h_tokens cache_creation.ephemeral_5m_input_tokens cache_creation.ephemeral_1h_input_tokens prompt_tokens_details.cached_tokens prompt_tokens_details.cache_creation_tokens prompt_tokens_details.cache_creation.ephemeral_5m_input_tokens prompt_tokens_details.cache_creation.ephemeral_1h_input_tokens input_tokens_details.cached_tokens input_tokens_details.cache_creation_tokens input_tokens_details.cache_creation.ephemeral_5m_input_tokens input_tokens_details.cache_creation.ephemeral_1h_input_tokens completion_tokens_details.reasoning_tokens output_tokens_details.reasoning_tokens billing_usage.claude_usage.input_tokens billing_usage.claude_usage.output_tokens billing_usage.claude_usage.cache_read_input_tokens billing_usage.claude_usage.cache_creation_input_tokens billing_usage.claude_usage.cache_creation.ephemeral_5m_input_tokens billing_usage.claude_usage.cache_creation.ephemeral_1h_input_tokens billing_usage.claude_usage.claude_cache_creation_5_m_tokens billing_usage.claude_usage.claude_cache_creation_1_h_tokens "
	for name, value := range input {
		path := prefix + name
		if child, ok := value.(map[string]any); ok {
			if !strings.Contains(numeric, " "+path+".") && path != "billing_usage" {
				complete = false
				continue
			}
			filtered, valid := safeUsage(child, path+".")
			if len(filtered) > 0 {
				safe[name] = filtered
			}
			complete = complete && valid
			continue
		}
		if number, ok := value.(json.Number); ok && strings.Contains(numeric, " "+path+" ") {
			quantity, err := strconv.ParseInt(string(number), 10, 64)
			if err == nil && quantity >= 0 {
				safe[name] = quantity
				continue
			}
		}
		if (path == "billing_usage.source" && value == "claude_messages") || (path == "billing_usage.semantic" && value == "anthropic") {
			safe[name] = value
			continue
		}
		complete = false
	}
	return safe, complete
}

func verificationPassed(v *model.ZTAPIModelVerification, in acceptanceInput, receipts []model.ZTAPIModelProbeReceipt20261128) bool {
	if v == nil || v.ID <= 0 || v.ModelConfigID != in.Expected.ConfigID || v.ChannelID != in.ChannelID || !v.NonStreamingPassed || !v.StreamingRequired || !v.StreamingPassed || !v.UsageReconciled || len(receipts) != 2 {
		return false
	}
	for i, r := range receipts {
		if r.Mode != []string{"nonstream", "stream"}[i] || !r.Complete || r.StatusCode < 200 || r.StatusCode >= 300 || r.RequestID == "" || r.UsageJSON == "" || r.UsageJSON == "{}" {
			return false
		}
	}
	return true
}

func run(args []string, stdin io.Reader, stdout io.Writer, h hooks) (exit int) {
	write := func(value any) {
		raw, err := common.Marshal(value)
		if err == nil {
			_, _ = stdout.Write(append(raw, '\n'))
		}
	}
	reject := func() int { write(map[string]string{"state": "rejected"}); return 1 }
	if len(args) != 1 || (args[0] != "--stage" && args[0] != "--verify-paid") {
		return reject()
	}
	mode := strings.TrimPrefix(args[0], "--")
	in, err := decodeInput(stdin, mode)
	if err != nil {
		return reject()
	}
	keyPath, dsn := os.Getenv("XCLOUD_ACCEPTANCE_KEY_FILE"), os.Getenv("SQL_DSN")
	rawKey, err := readProtectedFile(keyPath, 4096)
	if err != nil || dsn == "" {
		return reject()
	}
	key := strings.TrimSuffix(strings.TrimSuffix(string(rawKey), "\n"), "\r")
	if len(key) < 8 || strings.ContainsAny(key, " \t\r\n\x00") {
		return reject()
	}
	var proof *reservation
	if mode == "verify-paid" {
		raw, err := readProtectedFile(in.ReservationFile, inputLimit)
		sum := sha256.Sum256(raw)
		if err != nil || hex.EncodeToString(sum[:]) != in.ReservationSHA256 {
			return reject()
		}
		proof = &reservation{}
		if strictObject(raw, proof, "batch_id attempt_id operator_id source_model channel_id group expected key_reference_id limit_proof_id limit_proof_sha256 credential_sha256 claim_directory reservation_id state cap_u batch_limit_u per_model_limit_u reserved_at expires_at", "") != nil || validateReservation(in, *proof, h.Now().UTC()) != nil {
			return reject()
		}
		credentialDigest := sha256.Sum256([]byte(key))
		if hex.EncodeToString(credentialDigest[:]) != proof.CredentialSHA256 {
			return reject()
		}
	}
	if containsSecret(in, key, dsn) || containsSecret(proof, key, dsn) || createAttempt(in, mode, keyPath, proof) != nil {
		return reject()
	}
	result := outcome{State: "operation_unknown", BatchID: batchID, AttemptID: in.AttemptID, Mode: mode,
		SourceModel: in.SourceModel, ChannelID: in.ChannelID, Group: in.Group, KeyReferenceID: in.KeyReferenceID,
		LimitProofID: in.LimitProofID, LimitProofSHA256: in.LimitProofSHA256, ReservationSHA256: in.ReservationSHA256,
		ConfigID: in.Expected.ConfigID, Version: in.Expected.Version, SnapshotID: in.Expected.SnapshotID, SourceID: in.Expected.SourceID,
		ProbeReceipts: []model.ZTAPIModelProbeReceipt20261128{}}
	if proof != nil {
		result.ReservationID = proof.ReservationID
	}
	exit = 1
	defer func() {
		if recover() != nil {
			result.State = "operation_unknown"
			exit = 1
		}
		result.FinishedAt = h.Now().UTC()
		if atomicJSON(in.EvidenceDir, "result.json", result) != nil {
			result.State = "archive_failed"
			exit = 1
		}
		write(result)
	}()
	db, err := h.OpenDB(dsn)
	if err != nil || db == nil {
		result.State = "database_unavailable"
		return exit
	}
	connection, err := db.DB()
	if err != nil {
		return exit
	}
	defer connection.Close()
	connection.SetMaxOpenConns(1)
	originalDB, originalLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	defer func() { model.DB, model.LOG_DB = originalDB, originalLogDB }()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if preflight(db.WithContext(ctx), in, mode) != nil {
		result.State = "preflight_failed"
		return exit
	}
	if mode == "stage" {
		ids, fetchedAt, err := fetchStage(ctx, h.Client, key)
		if err != nil || !slices.Contains(ids, in.SourceModel) {
			result.State = "fetch_failed"
			return exit
		}
		staged, err := h.Stage(in.OperatorID, model.ZTAPIXcloudStage20261128{SourceModel: in.SourceModel, Group: in.Group, ChannelID: in.ChannelID,
			ConfigID: in.Expected.ConfigID, Version: in.Expected.Version, SnapshotID: in.Expected.SnapshotID, SourceID: in.Expected.SourceID}, ids, fetchedAt)
		if err != nil || staged == nil {
			return exit
		}
		result.ConfigID, result.Version, result.SnapshotID, result.SourceID, result.DiscoveryID = staged.Config.ID, staged.Config.Version, staged.Config.PublicationSnapshotID, staged.Source.ID, staged.DiscoveryID
		result.State = "staged"
		return 0
	}
	var verification *model.ZTAPIModelVerification
	var verifyErr error
	// Keep the source/config CAS locked through service reads and persistence.
	// A failed probe still commits its verification record; it is not a pass.
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if preflight(tx, in, mode) != nil || validateReservation(in, *proof, h.Now().UTC()) != nil {
			result.State = "preflight_failed"
			return errRejected
		}
		model.DB = tx
		defer func() { model.DB = db }()
		verification, verifyErr = h.Verify(ctx, in.ChannelID, in.SourceModel, in.OperatorID, key)
		if verification != nil {
			result.VerificationID = verification.ID
			result.ProbeReceipts = safeReceipts(verification.ProbeReceipts, key, dsn)
		}
		return nil
	})
	if err != nil {
		return exit
	}
	result.State = "verification_failed"
	if verifyErr == nil && verificationPassed(verification, in, result.ProbeReceipts) {
		result.State = "verified"
		return 0
	}
	return exit
}
