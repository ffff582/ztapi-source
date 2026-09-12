package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

func postZTAPIDurableTextQuota(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) bool {
	session, managed := info.Billing.(*ztapiDurableBilling)
	if !managed {
		return false
	}
	session.mu.Lock()
	frozenJSON := session.row.PriceSnapshotJSON
	session.mu.Unlock()
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	if err := common.UnmarshalJsonStr(frozenJSON, &snapshot); err != nil {
		logger.LogError(c, "managed billing snapshot cannot be decoded")
		_ = pendZTAPIBilling(info, usage, []string{"invalid_frozen_price"})
		return true
	}
	check := relaycommon.ValidateZTAPIUsageDimensions(c, ztapiUsageValidationInfo(info, &snapshot), usage)
	if check.Pending {
		missing := append(append([]string{}, check.MissingDimensions...), check.InvalidDimensions...)
		if check.UsageMissing {
			missing = append(missing, "upstream_usage_missing")
		}
		if err := pendZTAPIBilling(info, usage, missing); err != nil {
			logger.LogError(c, "managed billing pending handoff failed: "+err.Error())
		}
		return true
	}
	quota, dims, err := calculateZTAPIChargeDimensions(check)
	if err != nil {
		logger.LogError(c, "managed charge calculation requires reconciliation: "+err.Error())
		_ = pendZTAPIBilling(info, usage, []string{"charge_calculation_invalid"})
		return true
	}
	rawUsage, err := common.Marshal(usage)
	if err != nil {
		return true
	}
	rawDimensions, err := common.Marshal(dims)
	if err != nil {
		return true
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.row.Status != model.ZTAPISettlementReserved {
		return true
	}
	if session.attempt == nil {
		row, err := model.PendZTAPIRequestSettlement(session.row.OperationID, string(rawUsage), `["dispatch_evidence_missing"]`)
		if err == nil {
			session.row = row
		} else {
			logger.LogError(c, "managed dispatch evidence persistence failed: "+err.Error())
		}
		return true
	}
	for _, attempt := range relaycommon.SnapshotZTAPIBillingAttempts(c) {
		if !attempt.Dispatched {
			continue
		}
		if err = model.RecordZTAPIRequestAttemptResponse(session.row.OperationID, attempt.Index, attempt.ChannelID, attempt.HTTPStatus, attempt.UpstreamRequestID); err != nil {
			logger.LogError(c, "managed attempt evidence requires reconciliation: "+err.Error())
			row, pendingErr := model.PendZTAPIRequestSettlement(session.row.OperationID, string(rawUsage), `["attempt_evidence_conflict"]`)
			if pendingErr == nil {
				session.row = row
			}
			return true
		}
	}
	other, _ := common.Marshal(map[string]any{"billing_source": "wallet", "billing_status": "settled", "billing_dimensions": check.Dimensions, "publication_version": snapshot.Version, "price_source_version": snapshot.PriceSourceVersion, "usage_semantic": check.UsageSemantic})
	elapsed := 0
	if !info.StartTime.IsZero() {
		elapsed = max(0, int(time.Since(info.StartTime).Seconds()))
	}
	logEntry := model.Log{UserId: info.UserId, TokenId: info.TokenId, RequestId: info.RequestId, ChannelId: session.attempt.ChannelID, ModelName: info.OriginModelName, Type: model.LogTypeConsume, Quota: quota, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, CreatedAt: time.Now().Unix(), Username: c.GetString("username"), TokenName: c.GetString("token_name"), IsStream: info.IsStream, Group: info.UsingGroup, UseTime: elapsed, Other: string(other)}
	row, err := model.FinalizeZTAPIRequestSettlementWithEvidence(session.row.OperationID, int64(quota), string(rawUsage), string(rawDimensions), model.ZTAPISettlementEvidence{FinalAttempt: session.attempt.Attempt, ConsumeLog: logEntry})
	if err != nil {
		logger.LogError(c, fmt.Sprintf("managed settlement failed request=%s: %v", info.RequestId, err))
		pending, pendingErr := model.PendZTAPIRequestSettlement(session.row.OperationID, string(rawUsage), `["settlement_retry_required"]`)
		if pendingErr == nil {
			session.row = pending
		}
		return true
	}
	session.row = row
	session.usageJSON = string(rawUsage)
	session.dimensionsJSON = string(rawDimensions)
	// Durable workers also replay this outbox after process or log-DB failures.
	if _, err = model.ProcessPendingZTAPISettlementLogs(1); err != nil {
		logger.LogError(c, "managed billing log delivery deferred: "+err.Error())
	}
	return true
}

func ztapiUsageValidationInfo(info *relaycommon.RelayInfo, snapshot *relaycommon.ZTAPIPublicationSnapshot) *relaycommon.RelayInfo {
	if info == nil {
		return nil
	}
	return &relaycommon.RelayInfo{
		FinalRequestRelayFormat:  info.GetFinalRequestRelayFormat(),
		ResponsesUsageInfo:       info.ResponsesUsageInfo,
		ZTAPIPublicationSnapshot: snapshot,
	}
}
