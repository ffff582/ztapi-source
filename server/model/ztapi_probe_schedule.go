package model

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrZTAPIProbeLease = errors.New("ztapi probe lease conflict")

// Prices are upstream cost estimates in integer nanoUSD per million tokens,
// not customer quota or a claim about actual upstream debit.
type ZTAPIProbeTarget struct {
	ModelID                 int    `gorm:"not null;uniqueIndex:idx_zt_probe_hour,priority:1"`
	Stream                  bool   `gorm:"not null;uniqueIndex:idx_zt_probe_hour,priority:2"`
	SourceModel             string `gorm:"size:255;not null"`
	PublicModel             string `gorm:"size:255;not null"`
	EntryProtocol           string `gorm:"size:16;not null;default:''"`
	Protocol                string `gorm:"size:16;not null"`
	Modality                string `gorm:"size:16;not null;default:''"`
	Operation               string `gorm:"size:32;not null;default:''"`
	ProbePayloadJSON        string `gorm:"type:text;not null"`
	Generation              uint64 `gorm:"not null"`
	ConfigVersion           uint64 `gorm:"not null"`
	PublicationSnapshotID   int64  `gorm:"not null"`
	PriceSourceID           int64  `gorm:"not null"`
	PriceSourceVersion      uint64 `gorm:"not null"`
	InputNanoUSDPerMillion  int64  `gorm:"not null"`
	OutputNanoUSDPerMillion int64  `gorm:"not null"`
	FixedCostNanoUSD        int64  `gorm:"not null;default:0"`
}

type ZTAPIProbeJob struct {
	ID               string `gorm:"primaryKey;size:36"`
	ZTAPIProbeTarget `gorm:"embedded"`
	Hour             int64  `gorm:"not null;uniqueIndex:idx_zt_probe_hour,priority:3"`
	Source           string `gorm:"size:16;not null"`
	State            string `gorm:"size:16;not null;index:idx_zt_probe_due,priority:1"`
	ReadyAt          int64  `gorm:"not null;index:idx_zt_probe_due,priority:2"`
	LeaseToken       string `gorm:"size:36;not null"`
	LeaseUntil       int64  `gorm:"not null;index"`
	DispatchAt       int64  `gorm:"not null"`
	ReservedNanoUSD  int64  `gorm:"not null"`
	EstimateNanoUSD  int64  `gorm:"not null"`
	ExcessNanoUSD    int64  `gorm:"not null"`
	ResultCode       string `gorm:"size:64;not null"`
}

func (ZTAPIProbeJob) TableName() string { return "ztapi_probe_jobs" }

type ZTAPIProbeModeClock struct {
	ModelID        int   `gorm:"primaryKey;autoIncrement:false"`
	Stream         bool  `gorm:"primaryKey;autoIncrement:false"`
	LastDispatchAt int64 `gorm:"not null"`
}

func (ZTAPIProbeModeClock) TableName() string { return "ztapi_probe_mode_clocks" }

// This is a durable subdivision of the parent's R7 pool, never a new allowance.
type ZTAPIProbeBudget struct {
	ID               int    `gorm:"primaryKey;autoIncrement:false"`
	TransferID       string `gorm:"size:128;not null"`
	AllocatedNanoUSD int64  `gorm:"not null"`
	AccountedNanoUSD int64  `gorm:"not null"`
}

func (ZTAPIProbeBudget) TableName() string { return "ztapi_probe_budgets" }

// A bounded set of component statuses, not incidents or delivery receipts.
// The scheduler row also stores its durable round-robin model/mode cursor.
type ZTAPIHealthWorkerStatus struct {
	Component     string `json:"component" gorm:"primaryKey;size:32"`
	Code          string `json:"code" gorm:"size:64;not null"`
	Reference     string `json:"reference,omitempty" gorm:"size:64;not null"`
	UpdatedAt     int64  `json:"updated_at" gorm:"not null"`
	CursorModelID int    `json:"-" gorm:"not null"`
	CursorStream  bool   `json:"-" gorm:"not null"`
}

func (ZTAPIHealthWorkerStatus) TableName() string { return "ztapi_health_worker_statuses" }

func (s *ZTAPIProbeStore) RecordStatus(ctx context.Context, component, code, reference string, now time.Time) error {
	switch component {
	case "probe", "alert", "unpublish", "coverage", "worker", "scheduler":
	default:
		return errors.New("invalid worker status component")
	}
	if len(code) == 0 || len(code) > 64 || len(reference) > 64 {
		return errors.New("invalid worker status")
	}
	for _, ch := range code {
		if (ch < 'a' || ch > 'z') && ch != '_' && (ch < '0' || ch > '9') {
			return errors.New("invalid worker status code")
		}
	}
	row := ZTAPIHealthWorkerStatus{Component: component, Code: code, Reference: reference, UpdatedAt: now.Unix()}
	return s.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "component"}}, DoUpdates: clause.AssignmentColumns([]string{"code", "reference", "updated_at"})}).Create(&row).Error
}

func (s *ZTAPIProbeStore) Statuses(ctx context.Context) ([]ZTAPIHealthWorkerStatus, error) {
	var statuses []ZTAPIHealthWorkerStatus
	err := s.DB.WithContext(ctx).Order("component ASC").Limit(6).Find(&statuses).Error
	return statuses, err
}

func ZTAPIProbeMigrationTypes() []any {
	return []any{&ZTAPIProbeJob{}, &ZTAPIProbeModeClock{}, &ZTAPIProbeBudget{}, &ZTAPIHealthWorkerStatus{}}
}

// InitializeZTAPIProbeAllocation grants only money ALREADY reserved by parent
// from the same global R7 pool. With a filesystem R7 ledger, durably reserve the
// full allocation there BEFORE this SQL grant, using the same stable transfer
// ID. Keep that file reservation on ANY ambiguous SQL result; retry this grant
// idempotently. Never release it merely because applied=false or SQL timed out.
// A shared SQL pool can instead transfer within the same parent transaction.
// The worker never calls this function or creates customer quota.
// Repeating the same transfer is a no-op; changing it cannot reset spent money.
func InitializeZTAPIProbeAllocation(ctx context.Context, tx *gorm.DB, transferID string, nanoUSD int64) (applied bool, err error) {
	if tx == nil || transferID == "" || len(transferID) > 128 || nanoUSD < 0 || nanoUSD > 30_000_000_000 {
		return false, errors.New("invalid parent probe allocation")
	}
	row := ZTAPIProbeBudget{ID: 1, TransferID: transferID, AllocatedNanoUSD: nanoUSD}
	r := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if r.Error != nil {
		return false, r.Error
	}
	if r.RowsAffected == 1 {
		return true, nil
	}
	var existing ZTAPIProbeBudget
	if err = tx.WithContext(ctx).First(&existing, 1).Error; err != nil {
		return false, err
	}
	if existing.TransferID != transferID || existing.AllocatedNanoUSD != nanoUSD {
		return false, errors.New("probe allocation already initialized; cannot reset")
	}
	return false, nil
}

type ZTAPIProbeInputSample struct {
	ModelID             int
	Source              string
	InputTokens         int64
	ConsumptionNanoUSD  int64
	ConsumptionPositive bool
}

func ZTAPIProbeP95Input(modelID int, samples []ZTAPIProbeInputSample) int64 {
	values := make([]int64, 0, len(samples))
	for _, sample := range samples {
		if sample.ModelID == modelID && sample.Source == "real" && sample.InputTokens > 0 && (sample.ConsumptionNanoUSD > 0 || sample.ConsumptionPositive) {
			values = append(values, sample.InputTokens)
		}
	}
	if len(values) == 0 {
		return 2000
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[(95*len(values)+99)/100-1]
}

func ZTAPIProbeEstimateNanoUSD(input, output, inputPrice, outputPrice int64) (int64, error) {
	if input < 0 || output < 0 || inputPrice < 0 || outputPrice < 0 {
		return 0, errors.New("negative probe cost operand")
	}
	n := new(big.Int).Mul(big.NewInt(input), big.NewInt(inputPrice))
	n.Add(n, new(big.Int).Mul(big.NewInt(output), big.NewInt(outputPrice)))
	n.Add(n, big.NewInt(999999)).Quo(n, big.NewInt(1000000))
	if !n.IsInt64() {
		return 0, errors.New("probe cost overflow")
	}
	return n.Int64(), nil
}

type ZTAPIProbeAdmission struct {
	Target       ZTAPIProbeTarget
	Active       bool
	RealCoverage bool
	Samples      []ZTAPIProbeInputSample
}

// Check must use tx to lock/recheck the active publication, generation and
// current prices and query VALID REAL coverage for this model AND mode since
// the supplied cutoff, up to dispatch time. Unknown/probe traffic is not valid
// coverage. Samples must be bounded. It must not perform network side effects.
type ZTAPIProbeCheck func(context.Context, *gorm.DB, ZTAPIProbeTarget, time.Time) (ZTAPIProbeAdmission, error)

type ZTAPIProbeStore struct{ DB *gorm.DB }

func (s *ZTAPIProbeStore) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	if s == nil || s.DB == nil {
		return errors.New("probe database unavailable")
	}
	for attempt := 0; ; attempt++ {
		err := s.DB.WithContext(ctx).Transaction(fn)
		if err == nil || attempt >= 5 {
			return err
		}
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "locked") && !strings.Contains(message, "deadlock") && !strings.Contains(message, "serialize access") {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func validZTAPIProbeTarget(t ZTAPIProbeTarget) bool {
	entryProtocol := strings.TrimSpace(t.EntryProtocol)
	if entryProtocol == "" {
		entryProtocol = strings.TrimSpace(t.Protocol)
	}
	embedding := t.Protocol == "embeddings" && ZTAPIModelModality(t.SourceModel) == ZTAPIModalityEmbedding && !t.Stream && t.InputNanoUSDPerMillion > 0 && t.OutputNanoUSDPerMillion == 0
	image := t.Protocol == "images" && t.Modality == ZTAPIModalityImage && t.Operation == "image_generate"
	video := t.Protocol == "video-tasks" && t.Modality == ZTAPIModalityVideo && t.Operation == "video_submit"
	media := (image || video) && !t.Stream && t.InputNanoUSDPerMillion == 0 && t.OutputNanoUSDPerMillion == 0 &&
		t.FixedCostNanoUSD > 0 && t.FixedCostNanoUSD <= 30_000_000_000 && len(t.ProbePayloadJSON) <= 16*1024 && json.Valid([]byte(t.ProbePayloadJSON))
	text := (t.Protocol == "chat" || t.Protocol == "responses" || embedding) && t.Modality == "" && t.Operation == "" && t.ProbePayloadJSON == "" && t.FixedCostNanoUSD == 0
	return t.ModelID > 0 && t.Generation > 0 && t.ConfigVersion > 0 && entryProtocol != "" && len(entryProtocol) <= 16 && t.PublicModel != "" && len(t.PublicModel) <= 255 &&
		t.SourceModel != "" && len(t.SourceModel) <= 255 && (text || media) &&
		t.InputNanoUSDPerMillion >= 0 && t.OutputNanoUSDPerMillion >= 0
}

func (s *ZTAPIProbeStore) Enqueue(ctx context.Context, target ZTAPIProbeTarget, now time.Time) (job ZTAPIProbeJob, err error) {
	if !validZTAPIProbeTarget(target) {
		return job, errors.New("invalid probe target")
	}
	job = ZTAPIProbeJob{ID: uuid.NewString(), ZTAPIProbeTarget: target, Hour: now.Unix() / 3600, Source: "probe", State: "queued", ReadyAt: now.UnixMilli()}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; err != nil {
			return err
		}
		job = ZTAPIProbeJob{}
		return tx.Where("model_id = ? AND stream = ? AND hour = ?", target.ModelID, target.Stream, now.Unix()/3600).Take(&job).Error
	})
	return
}

func validZTAPIProbeLease(lease time.Duration) bool {
	return lease >= time.Second && lease <= 5*time.Minute
}

func (s *ZTAPIProbeStore) Claim(ctx context.Context, now time.Time, lease time.Duration) (*ZTAPIProbeJob, error) {
	if !validZTAPIProbeLease(lease) {
		return nil, errors.New("invalid probe lease duration")
	}
	var candidate ZTAPIProbeJob
	err := s.DB.WithContext(ctx).Where("(state = ? AND ready_at <= ?) OR (state = ? AND lease_until <= ?)", "queued", now.UnixMilli(), "claimed", now.UnixMilli()).Order("ready_at ASC, id ASC").Take(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	token := uuid.NewString()
	r := s.DB.WithContext(ctx).Model(&ZTAPIProbeJob{}).Where("id = ? AND ((state = ? AND ready_at <= ?) OR (state = ? AND lease_until <= ?))", candidate.ID, "queued", now.UnixMilli(), "claimed", now.UnixMilli()).Updates(map[string]any{"state": "claimed", "lease_token": token, "lease_until": now.Add(lease).UnixMilli()})
	if r.Error != nil {
		return nil, r.Error
	}
	if r.RowsAffected == 0 {
		return nil, nil
	}
	candidate.State, candidate.LeaseToken, candidate.LeaseUntil = "claimed", token, now.Add(lease).UnixMilli()
	return &candidate, nil
}

func lockZTAPIProbeBudget(tx *gorm.DB) (ZTAPIProbeBudget, error) {
	var budget ZTAPIProbeBudget
	r := tx.Model(&ZTAPIProbeBudget{}).Where("id = ?", 1).UpdateColumn("accounted_nano_usd", gorm.Expr("accounted_nano_usd"))
	if r.Error != nil {
		return budget, r.Error
	}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&budget, 1).Error
	return budget, err
}

func (s *ZTAPIProbeStore) Dispatch(ctx context.Context, id, token string, now time.Time, lease time.Duration, check ZTAPIProbeCheck) (job ZTAPIProbeJob, send bool, err error) {
	if check == nil || !validZTAPIProbeLease(lease) {
		return job, false, errors.New("probe dispatch configuration missing")
	}
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		send = false
		budget, err := lockZTAPIProbeBudget(tx)
		if err != nil {
			return err
		}
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "id = ?", id).Error; err != nil {
			return err
		}
		if job.State != "claimed" || job.LeaseToken != token || job.LeaseUntil <= now.UnixMilli() {
			return ErrZTAPIProbeLease
		}
		admission, err := check(ctx, tx, job.ZTAPIProbeTarget, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if !admission.Active || admission.RealCoverage || admission.Target != job.ZTAPIProbeTarget || job.Source != "probe" {
			job.State, job.ResultCode, job.LeaseUntil = "cancelled", "coverage_or_publication_changed", 0
			return tx.Save(&job).Error
		}
		var clock ZTAPIProbeModeClock
		err = tx.Where("model_id = ? AND stream = ?", job.ModelID, job.Stream).Take(&clock).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if clock.LastDispatchAt != 0 && now.UnixMilli() < clock.LastDispatchAt+time.Hour.Milliseconds() {
			job.State, job.ReadyAt, job.LeaseUntil = "queued", clock.LastDispatchAt+time.Hour.Milliseconds(), 0
			return tx.Save(&job).Error
		}
		reservation := job.FixedCostNanoUSD
		if reservation == 0 {
			reservation, err = ZTAPIProbeEstimateNanoUSD(ZTAPIProbeP95Input(job.ModelID, admission.Samples), 1024, job.InputNanoUSDPerMillion, job.OutputNanoUSDPerMillion)
			if err != nil {
				return err
			}
		}
		if budget.AllocatedNanoUSD <= 0 || budget.AccountedNanoUSD > budget.AllocatedNanoUSD || reservation > budget.AllocatedNanoUSD-budget.AccountedNanoUSD {
			job.State, job.ResultCode, job.LeaseUntil = "cancelled", "budget_exhausted", 0
			return tx.Save(&job).Error
		}
		job.State, job.DispatchAt, job.LeaseUntil = "dispatching", now.UnixMilli(), now.Add(lease).UnixMilli()
		job.ReservedNanoUSD, job.EstimateNanoUSD = reservation, reservation
		if err = tx.Save(&job).Error; err != nil {
			return err
		}
		clock = ZTAPIProbeModeClock{ModelID: job.ModelID, Stream: job.Stream, LastDispatchAt: now.UnixMilli()}
		if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model_id"}, {Name: "stream"}}, DoUpdates: clause.AssignmentColumns([]string{"last_dispatch_at"})}).Create(&clock).Error; err != nil {
			return err
		}
		if err = tx.Model(&ZTAPIProbeBudget{}).Where("id = ?", 1).UpdateColumn("accounted_nano_usd", budget.AccountedNanoUSD+reservation).Error; err != nil {
			return err
		}
		send = true
		return nil
	})
	return job, send && err == nil, err
}

// Finish only raises estimates, retaining uncertain reservations across restarts.
// Completion is operational bookkeeping, NOT another engine health outcome.
func (s *ZTAPIProbeStore) Finish(ctx context.Context, id, token, state, code string, observedNanoUSD int64) error {
	if (state != "completed" && state != "unknown") || observedNanoUSD < 0 || len(code) > 64 {
		return errors.New("invalid probe completion")
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		budget, err := lockZTAPIProbeBudget(tx)
		if err != nil {
			return err
		}
		var job ZTAPIProbeJob
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "id = ?", id).Error; err != nil {
			return err
		}
		if job.LeaseToken != token {
			return ErrZTAPIProbeLease
		}
		if job.State != "dispatching" && job.State != "completed" && job.State != "unknown" {
			return ErrZTAPIProbeLease
		}
		if observedNanoUSD > job.EstimateNanoUSD {
			delta := observedNanoUSD - job.EstimateNanoUSD
			next := int64(math.MaxInt64)
			// Saturation is permanently exhausted, never a wrapped/reusable cap.
			if budget.AccountedNanoUSD <= math.MaxInt64-delta {
				next = budget.AccountedNanoUSD + delta
			}
			if err = tx.Model(&ZTAPIProbeBudget{}).Where("id = ?", 1).UpdateColumn("accounted_nano_usd", next).Error; err != nil {
				return err
			}
			job.EstimateNanoUSD, job.ExcessNanoUSD = observedNanoUSD, observedNanoUSD-job.ReservedNanoUSD
		}
		if job.State == "dispatching" {
			job.State, job.ResultCode, job.LeaseUntil = state, code, 0
		}
		return tx.Save(&job).Error
	})
}

func (s *ZTAPIProbeStore) ExpireDispatches(ctx context.Context, now time.Time, limit int) ([]ZTAPIProbeJob, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid probe sweep limit")
	}
	var jobs []ZTAPIProbeJob
	err := s.DB.WithContext(ctx).Where("state = ? AND lease_until <= ?", "dispatching", now.UnixMilli()).Order("lease_until ASC").Limit(limit).Find(&jobs).Error
	if err != nil {
		return nil, err
	}
	expired := make([]ZTAPIProbeJob, 0, len(jobs))
	for _, job := range jobs {
		r := s.DB.WithContext(ctx).Model(&ZTAPIProbeJob{}).Where("id = ? AND state = ? AND lease_until <= ?", job.ID, "dispatching", now.UnixMilli()).Updates(map[string]any{"state": "unknown", "result_code": "abandoned_dispatch", "lease_until": 0})
		if r.Error != nil {
			return expired, r.Error
		}
		if r.RowsAffected == 1 {
			job.State, job.ResultCode, job.LeaseUntil = "unknown", "abandoned_dispatch", 0
			expired = append(expired, job)
		}
	}
	return expired, nil
}

func (s *ZTAPIProbeStore) Budget(ctx context.Context) (budget ZTAPIProbeBudget, err error) {
	err = s.DB.WithContext(ctx).First(&budget, 1).Error
	return
}
