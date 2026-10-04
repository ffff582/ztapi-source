package model

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func MigrateZTAPIAttemptBilling(db *gorm.DB) error {
	if db == nil {
		return ErrZTAPIAttemptBillingInvalid
	}
	return MigrateZTAPISupplierRefund(db)
}

func EnsureZTAPIAttemptBillingReviewsTx(tx *gorm.DB, parent *ZTAPIRequestSettlement) error {
	if tx == nil || tx.Statement == nil || parent == nil {
		return ErrZTAPIAttemptBillingInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return gorm.ErrInvalidTransaction
	}
	var row ZTAPIRequestSettlement
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, parent.ID).Error; err != nil {
		return err
	}
	if row.RequestID != parent.RequestID {
		return ErrZTAPIAttemptBillingConflict
	}
	var attempts []ZTAPIRequestAttempt
	query := tx.Where("settlement_id = ?", row.ID)
	if row.Status == ZTAPISettlementSettled {
		query = query.Where("attempt < ?", row.FinalAttempt)
	} else if row.Status != ZTAPISettlementPending && !(row.Status == ZTAPISettlementReleased && row.Dispatched) {
		return nil
	}
	if err := query.Find(&attempts).Error; err != nil {
		return err
	}
	for _, attempt := range attempts {
		review := ZTAPIAttemptBillingReview{SettlementID: row.ID, RequestID: row.RequestID, Attempt: attempt.Attempt, Status: "pending", PendingReason: "upstream_attempt_billing_unconfirmed"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&review).Error; err != nil {
			return err
		}
	}
	return nil
}

// Terminal financial facts resolve review work in the same transaction, never
// merely because a health event or HTTP response says the attempt succeeded.
func SyncZTAPIAttemptBillingReviewsTx(tx *gorm.DB, parent *ZTAPIRequestSettlement) error {
	if tx == nil || tx.Statement == nil || parent == nil {
		return ErrZTAPIAttemptBillingInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return gorm.ErrInvalidTransaction
	}
	var row ZTAPIRequestSettlement
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, parent.ID).Error; err != nil {
		return err
	}
	if row.RequestID != parent.RequestID {
		return ErrZTAPIAttemptBillingConflict
	}
	if row.Status == ZTAPISettlementReleased {
		verifiedAttempt := 0
		if row.Dispatched {
			var count int64
			if err := tx.Model(&ZTAPIPendingResolution{}).Where("settlement_id = ?", row.ID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				var task ZTAPIMediaTask
				if err := tx.Where("settlement_id = ? AND state = ? AND settlement_state = ? AND charge_disposition = ?", row.ID, ZTAPIMediaTaskFailed, ZTAPISettlementReleased, ZTAPIMediaChargeNoCharge).Take(&task).Error; err != nil {
					return err
				}
				count = 1
				verifiedAttempt = task.Attempt
			}
			if count != 1 {
				return ErrZTAPIAttemptBillingConflict
			}
		}
		query := tx.Model(&ZTAPIAttemptBillingReview{}).Where("request_id = ? AND status = ?", row.RequestID, "pending")
		if verifiedAttempt > 0 {
			query = query.Where("attempt = ?", verifiedAttempt)
		}
		return query.Updates(map[string]any{"status": "verified_nocharge", "pending_reason": ""}).Error
	}
	if row.Status != ZTAPISettlementSettled {
		return EnsureZTAPIAttemptBillingReviewsTx(tx, &row)
	}
	if err := EnsureZTAPIAttemptBillingReviewsTx(tx, &row); err != nil {
		return err
	}
	var charge ZTAPISupplierRefundCharge
	if err := tx.Where("request_id = ? AND attempt = ? AND billing_proof_id = 0", row.RequestID, row.FinalAttempt).Take(&charge).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var review ZTAPIAttemptBillingReview
	if err := tx.Where("request_id = ? AND attempt = ?", row.RequestID, row.FinalAttempt).Take(&review).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if review.AppliedProofID > 0 {
		var proof ZTAPIAttemptBillingProof
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&proof, review.AppliedProofID).Error; err != nil {
			return err
		}
		input, err := validateZTAPIAttemptProof(proof)
		if err != nil {
			return err
		}
		approval, priced, err := ztapiAttemptBillingApprovalTx(tx, &row, proof)
		if err != nil {
			return err
		}
		if approval.ApplyTarget != "pending_final" || input.Kind != "billed" || input.Attempt != row.FinalAttempt || int64(priced.ConsumeLog.Quota) != row.ChargedQuota {
			return ErrZTAPIAttemptBillingConflict
		}
		// Generic intent retries also reach this transaction. Late wire evidence
		// must invalidate their financial commit, not only the attempt worker.
		if overlap, err := overlappingZTAPIAttemptUsageTx(tx, &row, input, proof.ID); err != nil {
			return err
		} else if overlap {
			return ErrZTAPIAttemptBillingPending
		}
		if charge.UpstreamBillID != "" && charge.UpstreamBillID != input.UpstreamBillID {
			return ErrZTAPIAttemptBillingConflict
		}
		if err = tx.Model(&charge).UpdateColumn("upstream_bill_id", input.UpstreamBillID).Error; err != nil {
			return err
		}
		if err = tx.Model(&proof).Updates(map[string]any{"status": "applied", "charge_id": charge.ID, "pending_reason": ""}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&ZTAPIAttemptBillingReview{}).Where("request_id = ? AND attempt = ? AND status = ?", row.RequestID, row.FinalAttempt, "pending").Updates(map[string]any{"status": "verified_billed", "pending_reason": "", "charge_id": charge.ID}).Error
}

func ReconcileZTAPIAttemptBillingReviews(limit int) error {
	if limit < 1 || limit > 1000 || DB == nil {
		return ErrZTAPIAttemptBillingInvalid
	}
	var rows []ZTAPIRequestSettlement
	err := DB.Where(`((status = ? OR (status = ? AND final_attempt > 1)) AND EXISTS
	(SELECT 1 FROM ztapi_request_attempts a WHERE a.settlement_id = ztapi_request_settlements.id
	AND (ztapi_request_settlements.status = ? OR a.attempt < ztapi_request_settlements.final_attempt)
	AND NOT EXISTS (SELECT 1 FROM ztapi_attempt_billing_reviews r WHERE r.request_id = ztapi_request_settlements.request_id AND r.attempt = a.attempt)))
	OR (status IN ? AND EXISTS (SELECT 1 FROM ztapi_attempt_billing_reviews r WHERE r.request_id = ztapi_request_settlements.request_id AND r.status = 'pending'
	AND (ztapi_request_settlements.status = ? OR r.attempt = ztapi_request_settlements.final_attempt)))`, ZTAPISettlementPending, ZTAPISettlementSettled, ZTAPISettlementPending, []string{ZTAPISettlementSettled, ZTAPISettlementReleased}, ZTAPISettlementReleased).Order("id").Limit(limit).Find(&rows).Error
	if err != nil {
		return err
	}
	for i := range rows {
		if err = ztapiSettlementTransaction(func(tx *gorm.DB) error { return SyncZTAPIAttemptBillingReviewsTx(tx, &rows[i]) }); err != nil {
			return err
		}
	}
	return nil
}

func ListZTAPIAttemptBillingReviews(status string, afterID uint, limit int) ([]ZTAPIAttemptBillingReview, error) {
	if limit < 1 || limit > 1000 || (status != "pending" && status != "verified_billed" && status != "verified_nocharge" && status != "all") {
		return nil, ErrZTAPIAttemptBillingInvalid
	}
	var rows []ZTAPIAttemptBillingReview
	q := DB.Where("id > ?", afterID)
	if status != "all" {
		q = q.Where("status = ?", status)
	}
	err := q.Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

func normalizeZTAPIAttemptBillingSubmission(input *ZTAPIAttemptBillingSubmission) error {
	if strings.TrimSpace(input.Source) == "" || len(input.Source) > 128 || strings.TrimSpace(input.ProofID) == "" || len(input.ProofID) > 256 || input.RequestID == "" || len(input.RequestID) > 128 || input.Attempt < 1 || input.Attempt > 2 || input.UserID <= 0 || input.ChannelID <= 0 || input.CredentialVersion == "" || len(input.CredentialVersion) > 128 || len(input.UpstreamRequestID) > 200 || len(input.UpstreamTaskID) > 200 || len(input.UpstreamBillID) > 256 || input.EvidenceReference == "" || len(input.EvidenceReference) > 512 || len(input.DistinctUsageReference) > 512 || len(input.Usage) > 6 || (input.Kind != "billed" && input.Kind != "nocharge") {
		return ErrZTAPIAttemptBillingInvalid
	}
	if input.UsageSemantic != "" && input.UsageSemantic != "openai" && input.UsageSemantic != "anthropic" && input.UsageSemantic != ZTAPIAttemptBillingUsageSemanticImage {
		return ErrZTAPIAttemptBillingInvalid
	}
	imageUsage := input.UsageSemantic == ZTAPIAttemptBillingUsageSemanticImage
	seen := map[string]bool{}
	positive := false
	for _, u := range input.Usage {
		switch u.Dimension {
		case "input_tokens", "output_tokens", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h",
			"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output":
		default:
			return ErrZTAPIAttemptBillingInvalid
		}
		if seen[u.Dimension] || u.Quantity < 0 || (!imageUsage && u.Quantity == 0) || u.Quantity > maxBalanceLedgerQuota {
			return ErrZTAPIAttemptBillingInvalid
		}
		positive = positive || u.Quantity > 0
		seen[u.Dimension] = true
	}
	if imageUsage {
		if input.Kind != "billed" || !positive || !validZTAPIImageAttemptDimensions(seen) ||
			len(input.PriceRuleIDs) != len(input.Usage) || len(input.RawUsageJSON) == 0 || len(input.RawUsageJSON) > 65536 ||
			!validZTAPICanonicalRawUsage(input.RawUsageJSON) {
			return ErrZTAPIAttemptBillingInvalid
		}
		for dimension := range seen {
			if ruleID := input.PriceRuleIDs[dimension]; !ztapiSupplierCodePattern.MatchString(ruleID) {
				return ErrZTAPIAttemptBillingInvalid
			}
		}
		if input.SelectedRuleID != "" && !ztapiSupplierCodePattern.MatchString(input.SelectedRuleID) {
			return ErrZTAPIAttemptBillingInvalid
		}
	} else if input.SelectedRuleID != "" || len(input.PriceRuleIDs) != 0 || input.RawUsageJSON != "" ||
		seen["text_input"] || seen["text_cached_input"] || seen["image_input"] || seen["image_cached_input"] || seen["image_output"] {
		return ErrZTAPIAttemptBillingInvalid
	}
	if input.Kind == "nocharge" && len(input.Usage) != 0 {
		return ErrZTAPIAttemptBillingInvalid
	}
	input.Usage = append([]ZTAPIAttemptBillingQuantity(nil), input.Usage...)
	sort.Slice(input.Usage, func(i, j int) bool { return input.Usage[i].Dimension < input.Usage[j].Dimension })
	return nil
}

func validZTAPIImageAttemptDimensions(seen map[string]bool) bool {
	if len(seen) == 2 {
		return seen["input_tokens"] && seen["output_tokens"]
	}
	if len(seen) == 3 {
		return seen["text_input"] && seen["image_input"] && seen["image_output"]
	}
	return len(seen) == 5 && seen["text_input"] && seen["text_cached_input"] && seen["image_input"] && seen["image_cached_input"] && seen["image_output"]
}

func validZTAPICanonicalRawUsage(raw string) bool {
	if common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil {
		return false
	}
	var value map[string]any
	if common.DecodeJsonStrict(strings.NewReader(raw), &value) != nil || value == nil {
		return false
	}
	canonical, err := common.Marshal(value)
	return err == nil && string(canonical) == raw
}

func SubmitZTAPIAttemptBilling(input ZTAPIAttemptBillingSubmission) (*ZTAPIAttemptBillingProof, error) {
	if err := normalizeZTAPIAttemptBillingSubmission(&input); err != nil {
		return nil, err
	}
	payload, err := common.Marshal(input)
	if err != nil {
		return nil, err
	}
	identity, _ := common.Marshal([]string{input.Source, input.ProofID})
	proof := ZTAPIAttemptBillingProof{ProofKey: ztapiSupplierRefundHash(string(identity)), Source: input.Source, ProofID: input.ProofID, RequestID: input.RequestID, Attempt: input.Attempt, SubmissionJSON: string(payload), PayloadHash: ztapiSupplierRefundHash(string(payload)), Status: "pending", PendingReason: "awaiting_approval"}
	err = ztapiSettlementTransaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&proof).Error; e != nil {
			return e
		}
		var saved ZTAPIAttemptBillingProof
		if e := tx.Where("proof_key = ?", proof.ProofKey).Take(&saved).Error; e != nil {
			return e
		}
		if saved.PayloadHash != proof.PayloadHash || saved.SubmissionJSON != proof.SubmissionJSON {
			return ErrZTAPIAttemptBillingConflict
		}
		proof = saved
		return nil
	})
	return &proof, err
}

func ztapiAttemptBillingPendTx(tx *gorm.DB, p *ZTAPIAttemptBillingProof, reason string) error {
	if err := tx.Model(&ZTAPIAttemptBillingProof{}).Where("id = ?", p.ID).Updates(map[string]any{"pending_reason": reason}).Error; err != nil {
		return err
	}
	return tx.Model(&ZTAPIAttemptBillingReview{}).Where("request_id = ? AND attempt = ? AND status = ?", p.RequestID, p.Attempt, "pending").Update("pending_reason", reason).Error
}

func validateZTAPIAttemptProof(p ZTAPIAttemptBillingProof) (ZTAPIAttemptBillingSubmission, error) {
	var input ZTAPIAttemptBillingSubmission
	if p.PayloadHash != ztapiSupplierRefundHash(p.SubmissionJSON) || common.UnmarshalJsonStr(p.SubmissionJSON, &input) != nil || input.RequestID != p.RequestID || input.Attempt != p.Attempt || input.Source != p.Source || input.ProofID != p.ProofID {
		return input, ErrZTAPIAttemptBillingConflict
	}
	return input, normalizeZTAPIAttemptBillingSubmission(&input)
}

func validateZTAPIAttemptLineageTx(tx *gorm.DB, row *ZTAPIRequestSettlement, input ZTAPIAttemptBillingSubmission) error {
	if input.UserID != row.UserID {
		return ErrZTAPIAttemptBillingConflict
	}
	var attempt ZTAPIRequestAttempt
	if err := tx.Where("settlement_id = ? AND attempt = ?", row.ID, input.Attempt).Take(&attempt).Error; err != nil {
		return err
	}
	if attempt.ChannelID != input.ChannelID || attempt.CredentialVersion != input.CredentialVersion || attempt.UpstreamRequestID == "" || attempt.UpstreamRequestID != input.UpstreamRequestID {
		return ErrZTAPIAttemptBillingConflict
	}
	if input.UpstreamTaskID != "" {
		var task ZTAPIMediaTask
		if err := tx.Where("settlement_id = ? AND attempt = ? AND channel_id = ? AND credential_version = ? AND upstream_task_id = ?", row.ID, input.Attempt, input.ChannelID, input.CredentialVersion, input.UpstreamTaskID).Take(&task).Error; err != nil {
			return ErrZTAPIAttemptBillingConflict
		}
	}
	return nil
}

func validateZTAPIAttemptPrice(row *ZTAPIRequestSettlement, input ZTAPIAttemptBillingSubmission, priced *ZTAPIAttemptBillingPriced) (int64, error) {
	expectedDimensions := len(input.Usage)
	if input.UsageSemantic == ZTAPIAttemptBillingUsageSemanticImage {
		expectedDimensions = 0
		for _, usage := range input.Usage {
			if usage.Quantity > 0 {
				expectedDimensions++
			}
		}
	}
	if len(priced.Dimensions) != expectedDimensions || len(priced.Dimensions) == 0 {
		return 0, ErrZTAPIAttemptBillingPending
	}
	units := map[string]int64{}
	for _, u := range input.Usage {
		if u.Quantity > 0 || input.UsageSemantic != ZTAPIAttemptBillingUsageSemanticImage {
			units[u.Dimension] = u.Quantity
		}
	}
	var total int64
	for i := range priced.Dimensions {
		d := &priced.Dimensions[i]
		q, ok := units[d.Dimension]
		n, err := strconv.ParseInt(d.Units, 10, 64)
		if !ok || err != nil || n != q || d.ChargedQuota < 0 || d.ChargedQuota > maxBalanceLedgerQuota-total {
			return 0, ErrZTAPIAttemptBillingConflict
		}
		delete(units, d.Dimension)
		total += d.ChargedQuota
		d.TokenChargedQuota = 0
	}
	if len(units) != 0 {
		return 0, ErrZTAPIAttemptBillingConflict
	}
	if _, err := planZTAPISupplierRefund(total, 0, priced.Dimensions, nil, "full", nil); err != nil {
		return 0, err
	}
	l := &priced.ConsumeLog
	if l.Id != 0 || l.Type != LogTypeConsume || l.UserId != row.UserID || l.TokenId != row.TokenID || l.RequestId != row.RequestID || l.ModelName != row.PublicModel || l.ChannelId != input.ChannelID || int64(l.Quota) != total || l.CreatedAt <= 0 || l.UseTime < 0 {
		return 0, ErrZTAPIAttemptBillingConflict
	}
	var in, out int64
	for _, u := range input.Usage {
		if u.Dimension == "output_tokens" || u.Dimension == "image_output" {
			out += u.Quantity
		} else {
			in += u.Quantity
		}
	}
	if int64(l.PromptTokens) != in || int64(l.CompletionTokens) != out {
		return 0, ErrZTAPIAttemptBillingConflict
	}
	l.UpstreamRequestId = input.UpstreamRequestID
	if err := sanitizeZTAPISettlementLogMetadata(l); err != nil {
		return 0, err
	}
	return total, nil
}

// A different proof or channel does not establish distinct upstream usage.
// Call only while holding the canonical request lock, including before debit.
func overlappingZTAPIAttemptUsageTx(tx *gorm.DB, row *ZTAPIRequestSettlement, input ZTAPIAttemptBillingSubmission, proofID uint) (bool, error) {
	if input.UpstreamTaskID != "" {
		var taskCount int64
		if err := tx.Model(&ZTAPIMediaTask{}).Where("upstream_task_id = ? AND credential_version = ?", input.UpstreamTaskID, input.CredentialVersion).Count(&taskCount).Error; err != nil {
			return false, err
		}
		if taskCount != 1 {
			return true, nil
		}
		var chargeCount int64
		if err := tx.Model(&ZTAPISupplierRefundCharge{}).
			Where("upstream_task_id = ? AND credential_version = ? AND (request_id <> ? OR attempt <> ?)", input.UpstreamTaskID, input.CredentialVersion, row.RequestID, input.Attempt).
			Count(&chargeCount).Error; err != nil {
			return false, err
		}
		if chargeCount != 0 {
			return true, nil
		}
	}
	var attempts []ZTAPIRequestAttempt
	if err := tx.Where("settlement_id = ? AND attempt <> ?", row.ID, input.Attempt).Find(&attempts).Error; err != nil {
		return false, err
	}
	for _, attempt := range attempts {
		if input.UpstreamRequestID != "" && attempt.UpstreamRequestID == input.UpstreamRequestID {
			return true, nil
		}
	}
	var charges []ZTAPISupplierRefundCharge
	if err := tx.Where("request_id = ? AND attempt <> ?", row.RequestID, input.Attempt).Find(&charges).Error; err != nil {
		return false, err
	}
	for _, charge := range charges {
		if (input.UpstreamBillID != "" && charge.UpstreamBillID == input.UpstreamBillID) || (input.UpstreamRequestID != "" && charge.UpstreamRequestID == input.UpstreamRequestID) {
			return true, nil
		}
	}
	identity, _ := common.Marshal([]string{input.Source, input.UpstreamBillID})
	var count int64
	err := tx.Model(&ZTAPIAttemptBillingApproval{}).Where("usage_identity_key = ? AND proof_id <> ?", ztapiSupplierRefundHash(string(identity)), proofID).Count(&count).Error
	return count != 0, err
}

func ApproveZTAPIAttemptBilling(proofID uint, operatorID int, reference string, pricer ZTAPIAttemptBillingPricer) error {
	if operatorID <= 0 || strings.TrimSpace(reference) == "" || len(reference) > 512 {
		return ErrZTAPISupplierRefundUnauthorized
	}
	var initial ZTAPIAttemptBillingProof
	if err := DB.First(&initial, proofID).Error; err != nil {
		return err
	}
	pending := false
	resolvePendingNoCharge := false
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		var row ZTAPIRequestSettlement
		rowErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", initial.RequestID).Take(&row).Error
		if rowErr != nil && !errors.Is(rowErr, gorm.ErrRecordNotFound) {
			return rowErr
		}
		var user *User
		var token *Token
		var ownerErr error
		if rowErr == nil {
			user, token, ownerErr = ztapiSettlementOwners(tx, &row)
		}
		var operator User
		if e := tx.First(&operator, operatorID).Error; e != nil || operator.Status != common.UserStatusEnabled || !common.HasAdminPermission(operator.Role, common.PermissionFinanceWrite) {
			return ErrZTAPISupplierRefundUnauthorized
		}
		var proof ZTAPIAttemptBillingProof
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&proof, proofID).Error; e != nil {
			return e
		}
		pend := func(reason string) error { pending = true; return ztapiAttemptBillingPendTx(tx, &proof, reason) }
		input, e := validateZTAPIAttemptProof(proof)
		if e != nil || proof.RequestID != initial.RequestID {
			return pend("evidence_conflict")
		}
		if rowErr != nil {
			return pend("original_request_missing")
		}
		if ownerErr != nil {
			return pend("original_owner_missing")
		}
		if user.DeletedAt.Valid || token.DeletedAt.Valid {
			return pend("original_owner_deleted")
		}
		var existing ZTAPIAttemptBillingApproval
		if e = tx.Where("proof_id = ?", proofID).Take(&existing).Error; e == nil {
			if existing.OperatorID != operatorID || existing.VerificationReference != reference || existing.PayloadHash != proof.PayloadHash || existing.PricedHash != ztapiAttemptBillingApprovalDigest(existing) {
				return ErrZTAPIAttemptBillingConflict
			}
			resolvePendingNoCharge = existing.ApplyTarget == "pending_nocharge"
			return nil
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if e = EnsureZTAPIAttemptBillingReviewsTx(tx, &row); e != nil {
			return e
		}
		target := "additional_attempt"
		if row.Status == ZTAPISettlementPending && input.Kind == "nocharge" {
			if exists, intentErr := HasZTAPISettlementFinalizationIntentTx(tx, row.RequestID); intentErr != nil {
				return intentErr
			} else if exists {
				return pend("existing_finalization_intent")
			}
			target = "pending_nocharge"
			resolvePendingNoCharge = true
		} else if row.Status == ZTAPISettlementPending && input.Kind == "billed" {
			var last ZTAPIRequestAttempt
			if e = tx.Where("settlement_id = ?", row.ID).Order("attempt DESC").Take(&last).Error; e != nil {
				return e
			}
			if input.Attempt != last.Attempt {
				return pend("unsupported_parent_scope")
			}
			if exists, e := HasZTAPISettlementFinalizationIntentTx(tx, row.RequestID); e != nil {
				return e
			} else if exists {
				return pend("existing_finalization_intent")
			}
			target = "pending_final"
		} else if row.Status != ZTAPISettlementSettled || row.FinalAttempt != 2 || input.Attempt != 1 {
			return pend("unsupported_parent_scope")
		}
		if e = validateZTAPIAttemptLineageTx(tx, &row, input); e != nil {
			return pend("original_lineage_mismatch")
		}
		var review ZTAPIAttemptBillingReview
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ? AND attempt = ?", row.RequestID, input.Attempt).Take(&review).Error; e != nil {
			return e
		}
		if review.Status != "pending" || review.AppliedProofID != 0 {
			return pend("approval_conflict")
		}
		approval := ZTAPIAttemptBillingApproval{ProofID: proof.ID, ReviewID: review.ID, OperatorID: operatorID, VerificationReference: reference, PayloadHash: proof.PayloadHash, PriceSnapshotHash: ztapiSupplierRefundHash(row.PriceSnapshotJSON), PricedJSON: "{}", ApplyTarget: target}
		if target == "pending_final" {
			approval.PendingUsageJSON, approval.PendingMissingDimensionsJSON = row.UsageJSON, row.MissingDimensionsJSON
		}
		var priced ZTAPIAttemptBillingPriced
		if input.Kind == "billed" {
			if strings.TrimSpace(input.UpstreamBillID) == "" || strings.TrimSpace(input.DistinctUsageReference) == "" || len(input.Usage) == 0 {
				return pend("missing_distinct_usage_evidence")
			}
			if overlap, err := overlappingZTAPIAttemptUsageTx(tx, &row, input, proof.ID); err != nil {
				return err
			} else if overlap {
				return pend("overlapping_billed_usage")
			}
			if pricer == nil {
				return pend("billing_dimensions_pending")
			}
			priced, e = pricer(row, input)
			if e != nil {
				return pend("billing_dimensions_pending")
			}
			if _, e = validateZTAPIAttemptPrice(&row, input, &priced); e != nil {
				return pend("billing_dimensions_pending")
			}
			raw, e := common.Marshal(priced)
			if e != nil || len(raw) > 65536 {
				return pend("billing_dimensions_pending")
			}
			approval.PricedJSON = string(raw)
			identity, _ := common.Marshal([]string{input.Source, input.UpstreamBillID})
			key := ztapiSupplierRefundHash(string(identity))
			approval.UsageIdentityKey = &key
		}
		approval.PricedHash = ztapiAttemptBillingApprovalDigest(approval)
		if e = tx.Create(&approval).Error; e != nil {
			return e
		}
		if target == "pending_final" {
			if e = prepareZTAPIApprovedFinalIntentTx(tx, &row, input, priced); e != nil {
				return e
			}
		}
		status := "approved"
		reviewStatus := "pending"
		reason := "billing_application_pending"
		if input.Kind == "nocharge" {
			reviewStatus = "verified_nocharge"
			if target == "pending_nocharge" {
				reason = "nocharge_release_pending"
			} else {
				status = "completed"
				reason = ""
			}
		}
		if e = tx.Model(&proof).Updates(map[string]any{"status": status, "pending_reason": reason}).Error; e != nil {
			return e
		}
		return tx.Model(&review).Updates(map[string]any{"status": reviewStatus, "pending_reason": reason, "applied_proof_id": proof.ID}).Error
	})
	if err == nil && pending {
		return ErrZTAPIAttemptBillingPending
	}
	if err == nil && resolvePendingNoCharge {
		_, err = resolveZTAPIPendingFromApprovedNoChargeAttempts(initial.RequestID, operatorID)
	}
	return err
}

func resolveZTAPIPendingFromApprovedNoChargeAttempts(requestID string, operatorID int) (bool, error) {
	var row ZTAPIRequestSettlement
	if err := DB.Where("request_id = ?", requestID).Take(&row).Error; err != nil {
		return false, err
	}
	if row.Status != ZTAPISettlementPending && row.Status != ZTAPISettlementReleased {
		return false, ErrZTAPIAttemptBillingPending
	}
	var attempts []ZTAPIRequestAttempt
	if err := DB.Where("settlement_id = ?", row.ID).Order("attempt").Find(&attempts).Error; err != nil {
		return false, err
	}
	if len(attempts) < 1 || len(attempts) > 2 {
		return false, ErrZTAPIAttemptBillingConflict
	}
	aggregate := ZTAPINoChargeProof{Source: "approved-attempt-billing", VerificationReference: "all dispatched attempts have approved supplier no-charge evidence"}
	identity := make([]string, 0, len(attempts)*2)
	proofIDs := make([]uint, 0, len(attempts))
	for _, attempt := range attempts {
		var review ZTAPIAttemptBillingReview
		if err := DB.Where("request_id = ? AND attempt = ?", row.RequestID, attempt.Attempt).Take(&review).Error; err != nil {
			return false, err
		}
		if review.Status != "verified_nocharge" || review.AppliedProofID == 0 {
			return false, nil
		}
		var proof ZTAPIAttemptBillingProof
		if err := DB.First(&proof, review.AppliedProofID).Error; err != nil {
			return false, err
		}
		input, err := validateZTAPIAttemptProof(proof)
		if err != nil {
			return false, err
		}
		approval, _, err := ztapiAttemptBillingApprovalTx(DB, &row, proof)
		if err != nil {
			return false, err
		}
		if input.Kind != "nocharge" || approval.ApplyTarget != "pending_nocharge" || input.Attempt != attempt.Attempt || input.ChannelID != attempt.ChannelID || input.CredentialVersion != attempt.CredentialVersion || input.UpstreamRequestID != attempt.UpstreamRequestID {
			return false, ErrZTAPIAttemptBillingConflict
		}
		if operatorID == 0 {
			operatorID = approval.OperatorID
		}
		aggregate.Attempts = append(aggregate.Attempts, ZTAPINoChargeAttempt{Attempt: attempt.Attempt, ChannelID: attempt.ChannelID, CredentialVersion: attempt.CredentialVersion, UpstreamRequestID: attempt.UpstreamRequestID, VerificationReference: approval.VerificationReference})
		identity = append(identity, proof.ProofKey, approval.PricedHash)
		proofIDs = append(proofIDs, proof.ID)
	}
	rawIdentity, _ := common.Marshal(identity)
	aggregate.ProofID = ztapiSupplierRefundHash(string(rawIdentity))
	if _, err := resolveZTAPIPendingNoCharge(row.ID, operatorID, aggregate, false); err != nil {
		return false, err
	}
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		result := tx.Model(&ZTAPIAttemptBillingProof{}).Where("id IN ? AND status = ?", proofIDs, "approved").Updates(map[string]any{"status": "completed", "pending_reason": ""})
		return result.Error
	})
	return err == nil, err
}

func ztapiAttemptBillingApprovalDigest(a ZTAPIAttemptBillingApproval) string {
	payload, _ := common.Marshal([]any{a.ProofID, a.ReviewID, a.OperatorID, a.VerificationReference, a.PayloadHash, a.PriceSnapshotHash, a.ApplyTarget, a.PricedJSON, a.UsageIdentityKey, a.PendingUsageJSON, a.PendingMissingDimensionsJSON})
	return ztapiSupplierRefundHash(string(payload))
}

func ztapiAttemptBillingApprovalTx(tx *gorm.DB, row *ZTAPIRequestSettlement, p ZTAPIAttemptBillingProof) (ZTAPIAttemptBillingApproval, ZTAPIAttemptBillingPriced, error) {
	var approval ZTAPIAttemptBillingApproval
	var priced ZTAPIAttemptBillingPriced
	if err := tx.Where("proof_id = ?", p.ID).Take(&approval).Error; err != nil {
		return approval, priced, err
	}
	if approval.OperatorID <= 0 || strings.TrimSpace(approval.VerificationReference) == "" || approval.PayloadHash != p.PayloadHash || approval.PriceSnapshotHash != ztapiSupplierRefundHash(row.PriceSnapshotJSON) || approval.PricedHash != ztapiAttemptBillingApprovalDigest(approval) || common.UnmarshalJsonStr(approval.PricedJSON, &priced) != nil {
		return approval, priced, ErrZTAPIAttemptBillingConflict
	}
	return approval, priced, nil
}

// Only the trusted approval transaction calls this. It never opens a public
// reserved state and commits the approval, immutable intent and marker together.
func prepareZTAPIApprovedFinalIntentTx(tx *gorm.DB, row *ZTAPIRequestSettlement, input ZTAPIAttemptBillingSubmission, priced ZTAPIAttemptBillingPriced) error {
	if row.Status != ZTAPISettlementPending {
		return ErrZTAPIAttemptBillingConflict
	}
	actual, err := validateZTAPIAttemptPrice(row, input, &priced)
	if err != nil {
		return err
	}
	evidence, err := canonicalZTAPISettlementEvidenceTx(tx, row, actual, ZTAPISettlementEvidence{FinalAttempt: input.Attempt, ConsumeLog: priced.ConsumeLog})
	if err != nil {
		return err
	}
	encodedDims, _ := common.Marshal(priced.Dimensions)
	dims, err := normalizeZTAPIChargeDimensions(string(encodedDims), actual, 0)
	if err != nil {
		return err
	}
	usage, _ := common.Marshal(struct {
		UsageSemantic string                        `json:"usage_semantic"`
		Dimensions    []ZTAPIAttemptBillingQuantity `json:"dimensions"`
	}{input.UsageSemantic, input.Usage})
	payload, err := common.Marshal(evidence)
	if err != nil {
		return err
	}
	intent := ZTAPISettlementFinalizationIntent{OperationID: row.OperationID, RequestID: row.RequestID, ActualQuota: actual, UsageJSON: string(usage), ChargeDimensionsJSON: dims, EvidenceJSON: string(payload), CreatedAt: time.Now().Unix()}
	intent.PayloadSHA256 = ztapiSettlementIntentDigest(&intent)
	if err = tx.Create(&intent).Error; err != nil {
		return err
	}
	return tx.Model(&ZTAPIRequestSettlement{}).Where("id = ? AND status = ?", row.ID, ZTAPISettlementPending).Updates(map[string]any{"usage_json": intent.UsageJSON, "missing_dimensions_json": `["settlement_retry_required"]`}).Error
}

func ProcessZTAPIAttemptBilling(proofID uint, enqueue ZTAPIAttemptBillingLogEnqueuer) error {
	var initial ZTAPIAttemptBillingProof
	if err := DB.First(&initial, proofID).Error; err != nil {
		return err
	}
	var parent *ZTAPIRequestSettlement
	pending := false
	resumeFinal := false
	resumeNoCharge := false
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		var row ZTAPIRequestSettlement
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", initial.RequestID).Take(&row).Error; e != nil {
			return e
		}
		parent = &row
		user, token, ownerErr := ztapiSettlementOwners(tx, &row)
		var proof ZTAPIAttemptBillingProof
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&proof, proofID).Error; e != nil {
			return e
		}
		pend := func(reason string) error { pending = true; return ztapiAttemptBillingPendTx(tx, &proof, reason) }
		input, e := validateZTAPIAttemptProof(proof)
		if e != nil || proof.RequestID != initial.RequestID {
			return pend("evidence_conflict")
		}
		approval, priced, e := ztapiAttemptBillingApprovalTx(tx, &row, proof)
		if e != nil {
			return pend("approval_conflict")
		}
		if approval.ApplyTarget == "pending_nocharge" {
			resumeNoCharge = true
			return nil
		}
		if proof.Status == "completed" || proof.Status == "applied" {
			return nil
		}
		if ownerErr != nil {
			return pend("original_owner_missing")
		}
		if user.DeletedAt.Valid || token.DeletedAt.Valid {
			return pend("original_owner_deleted")
		}
		if input.Kind == "billed" {
			if overlap, err := overlappingZTAPIAttemptUsageTx(tx, &row, input, proof.ID); err != nil {
				return err
			} else if overlap {
				return pend("overlapping_billed_usage")
			}
		}
		if approval.ApplyTarget == "pending_final" {
			if row.Status == ZTAPISettlementPending && row.MissingDimensionsJSON == `["settlement_retry_required"]` {
				intent, saved, e := loadZTAPISettlementFinalizationIntentTx(tx, &row)
				if e != nil {
					return e
				}
				if intent.ActualQuota != int64(priced.ConsumeLog.Quota) || saved.FinalAttempt != input.Attempt || matchZTAPISettlementLogEvidence(saved.ConsumeLog, priced.ConsumeLog) != nil {
					return pend("approval_conflict")
				}
				resumeFinal = true
				return nil
			}
			if row.Status == ZTAPISettlementSettled && row.FinalAttempt == input.Attempt {
				return SyncZTAPIAttemptBillingReviewsTx(tx, &row)
			}
			return pend("unsupported_parent_scope")
		}
		if input.Kind != "billed" || approval.ApplyTarget != "additional_attempt" || row.Status != ZTAPISettlementSettled || row.FinalAttempt != 2 || input.Attempt != 1 {
			return pend("unsupported_parent_scope")
		}
		if e = validateZTAPIAttemptLineageTx(tx, &row, input); e != nil {
			return pend("original_lineage_mismatch")
		}
		actual, e := validateZTAPIAttemptPrice(&row, input, &priced)
		if e != nil {
			return pend("billing_dimensions_pending")
		}
		if enqueue == nil {
			return pend("billing_log_unavailable")
		}
		var review ZTAPIAttemptBillingReview
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&review, approval.ReviewID).Error; e != nil {
			return e
		}
		if review.RequestID != row.RequestID || review.Attempt != input.Attempt || review.AppliedProofID != proof.ID || review.Status != "pending" {
			return pend("approval_conflict")
		}
		ledger, e := ztapiSettlementWalletDeltaTx(tx, user, -actual, row.RequestID, "attempt-billing:"+proof.ProofKey, "verified distinct supplier attempt usage", BalanceLedgerSourceUsageSettlement, true)
		if e != nil {
			return e
		}
		tokenCharged := int64(0)
		if !row.TokenUnlimited {
			delta, e := ztapiSettlementTokenDeltaTx(tx, token, -actual, true)
			if e != nil {
				return e
			}
			tokenCharged = -delta
		}
		if int64(user.UsedQuota) > maxBalanceLedgerQuota-actual {
			return ErrBalanceLedgerOverflow
		}
		if actual > 0 {
			update := tx.Unscoped().Model(&User{}).Where("id = ?", row.UserID).UpdateColumn("used_quota", gorm.Expr("used_quota + ?", actual))
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return ErrZTAPIAttemptBillingConflict
			}
		}
		raw, _ := common.Marshal(priced.Dimensions)
		dims, e := normalizeZTAPIChargeDimensions(string(raw), actual, tokenCharged)
		if e != nil {
			return e
		}
		operationID := "attempt-bill:" + ztapiSupplierRefundHash(proof.ProofKey)[:48]
		charge := ZTAPISupplierRefundCharge{RequestID: row.RequestID, SettlementID: row.ID, UserID: row.UserID, TokenID: row.TokenID, Attempt: input.Attempt, ChannelID: input.ChannelID, CredentialVersion: input.CredentialVersion, UpstreamRequestID: input.UpstreamRequestID, UpstreamTaskID: input.UpstreamTaskID, UpstreamBillID: input.UpstreamBillID, PriceSnapshotJSON: row.PriceSnapshotJSON, DimensionsJSON: dims, ChargedQuota: actual, TokenChargedQuota: tokenCharged, ProgressJSON: "[]", BillingProofID: proof.ID, BillingOperationID: operationID}
		if ledger != nil {
			charge.OriginalLedgerID = ledger.ID
		}
		if e = tx.Create(&charge).Error; e != nil {
			return e
		}
		if e = enqueue(tx, &row, &charge, operationID, priced.ConsumeLog); e != nil {
			return e
		}
		var outbox ZTAPISettlementLogOutbox
		if e = tx.Where("operation_id = ?", operationID).Take(&outbox).Error; e != nil {
			return e
		}
		if !outbox.ChannelStatsApplied {
			return ErrZTAPISettlementLogConflict
		}
		shadow := row
		shadow.OperationID, shadow.FinalAttempt, shadow.ChargedQuota = operationID, input.Attempt, actual
		if e = validateZTAPISettlementReplayEvidenceTx(tx, &shadow, &ZTAPISettlementEvidence{FinalAttempt: input.Attempt, ConsumeLog: priced.ConsumeLog}); e != nil {
			return e
		}
		if e = tx.Model(&proof).Updates(map[string]any{"status": "applied", "pending_reason": "", "charge_id": charge.ID}).Error; e != nil {
			return e
		}
		if e = tx.Model(&review).Updates(map[string]any{"status": "verified_billed", "pending_reason": "", "charge_id": charge.ID}).Error; e != nil {
			return e
		}
		row.CacheSyncPending = true
		return tx.Model(&ZTAPIRequestSettlement{}).Where("id = ?", row.ID).Update("cache_sync_pending", true).Error
	})
	if err != nil {
		return err
	}
	if pending {
		return ErrZTAPIAttemptBillingPending
	}
	if resumeFinal {
		if _, err := finalizeZTAPIRequestSettlement(parent.OperationID, 0, "{}", "[]", nil, true); err != nil {
			return err
		}
		return ProcessZTAPIAttemptBilling(proofID, enqueue)
	}
	if resumeNoCharge {
		resolved, err := resolveZTAPIPendingFromApprovedNoChargeAttempts(initial.RequestID, 0)
		if err != nil {
			return err
		}
		if !resolved {
			return ErrZTAPIAttemptBillingPending
		}
		return nil
	}
	if parent != nil {
		// Refresh the revision changed by the transaction before cache CAS.
		if e := DB.First(parent, parent.ID).Error; e != nil {
			return e
		}
		syncZTAPISettlementCaches(parent)
		if parent.CacheSyncPending {
			return ErrBalanceLedgerCacheSync
		}
	}
	return ztapiSettlementTransaction(func(tx *gorm.DB) error {
		return tx.Model(&ZTAPIAttemptBillingProof{}).Where("id = ? AND status = ?", proofID, "applied").Update("status", "completed").Error
	})
}

func RetryZTAPIAttemptBillings(limit int, enqueue ZTAPIAttemptBillingLogEnqueuer) error {
	if limit < 1 || limit > 1000 || DB == nil {
		return ErrZTAPIAttemptBillingInvalid
	}
	var proofs []ZTAPIAttemptBillingProof
	if err := DB.Where("status IN ?", []string{"approved", "applied"}).Order("last_attempt_at,id").Limit(limit).Find(&proofs).Error; err != nil {
		return err
	}
	var failures []error
	for _, p := range proofs {
		if err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
			return tx.Model(&ZTAPIAttemptBillingProof{}).Where("id = ?", p.ID).UpdateColumn("last_attempt_at", time.Now().UnixNano()).Error
		}); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := ProcessZTAPIAttemptBilling(p.ID, enqueue); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func validateZTAPIAdditionalChargeTx(tx *gorm.DB, parent *ZTAPIRequestSettlement, charge *ZTAPISupplierRefundCharge) error {
	var proof ZTAPIAttemptBillingProof
	if err := tx.First(&proof, charge.BillingProofID).Error; err != nil {
		return err
	}
	input, err := validateZTAPIAttemptProof(proof)
	if err != nil {
		return err
	}
	_, priced, err := ztapiAttemptBillingApprovalTx(tx, parent, proof)
	if err != nil {
		return err
	}
	actual, err := validateZTAPIAttemptPrice(parent, input, &priced)
	if err != nil {
		return err
	}
	raw, _ := common.Marshal(priced.Dimensions)
	dims, err := normalizeZTAPIChargeDimensions(string(raw), actual, charge.TokenChargedQuota)
	if err != nil {
		return err
	}
	if charge.ChargedQuota != actual || charge.DimensionsJSON != dims || charge.Attempt != input.Attempt || charge.ChannelID != input.ChannelID || charge.CredentialVersion != input.CredentialVersion || charge.UpstreamRequestID != input.UpstreamRequestID || charge.UpstreamTaskID != input.UpstreamTaskID || charge.UpstreamBillID != input.UpstreamBillID || proof.ChargeID != charge.ID || charge.BillingOperationID != "attempt-bill:"+ztapiSupplierRefundHash(proof.ProofKey)[:48] {
		return ErrZTAPIAttemptBillingConflict
	}
	if actual > 0 {
		var ledger BalanceLedger
		if err := tx.First(&ledger, charge.OriginalLedgerID).Error; err != nil {
			return err
		}
		if ledger.RequestID != parent.RequestID || ledger.UserID != parent.UserID || ledger.Delta != -actual || ledger.IdempotencyKey != "attempt-billing:"+proof.ProofKey {
			return ErrZTAPIAttemptBillingConflict
		}
	}
	return nil
}
