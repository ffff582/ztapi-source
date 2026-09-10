package service

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type ztapiDurableBilling struct {
	mu             sync.Mutex
	row            *model.ZTAPIRequestSettlement
	info           *relaycommon.RelayInfo
	usageJSON      string
	dimensionsJSON string
	attempt        *model.ZTAPIRequestAttempt
}

func newZTAPIDurableBilling(info *relaycommon.RelayInfo, amount int) (*ztapiDurableBilling, *types.NewAPIError) {
	if info == nil || info.ZTAPIPublicationSnapshot == nil {
		return nil, types.NewError(model.ErrZTAPISettlementInvalid, types.ErrorCodeInvalidRequest)
	}
	if info.UserSetting.BillingPreference == "subscription_only" {
		return nil, types.NewErrorWithStatusCode(errors.New("This model currently supports wallet billing only"), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if info.RequestId == "" {
		info.RequestId = common.GetUUID()
	}
	operationID := common.GetUUID()
	var row *model.ZTAPIRequestSettlement
	var err error
	if info.ZTAPIPublicationSnapshot.Modality == "image" {
		selectorJSON, selectorErr := ztapiImageReservationSelector(info.Request)
		if selectorErr != nil {
			return nil, types.NewErrorWithStatusCode(selectorErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		selector, canonicalSelector, selectorErr := canonicalZTAPIMediaSelector(selectorJSON)
		if selectorErr != nil || canonicalSelector != selectorJSON || info.ZTAPIPublicationSnapshot.ImageProtocolContract == nil {
			return nil, types.NewErrorWithStatusCode(model.ErrZTAPISettlementInvalid, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		protocol, protocolJSON, protocolErr := types.SealZTAPIImageProtocolContract(info.ZTAPIPublicationSnapshot.ImageProtocolContract.Clone())
		if protocolErr != nil || protocol.EvidenceHash != info.ZTAPIPublicationSnapshot.ImageProtocolContract.EvidenceHash {
			return nil, types.NewErrorWithStatusCode(model.ErrZTAPISettlementInvalid, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		quotaPerUnit, quotaErr := canonicalZTAPIQuotaPerUnit(common.QuotaPerUnit)
		if quotaErr != nil {
			return nil, types.NewErrorWithStatusCode(quotaErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		quotaPerUnitDecimal, quotaErr := parseCanonicalZTAPIQuotaPerUnit(quotaPerUnit)
		if quotaErr != nil {
			return nil, types.NewErrorWithStatusCode(quotaErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		maximumDimensions, maximumQuota, deriveErr := deriveZTAPIImageMaximumReservation(
			info.ZTAPIPublicationSnapshot.MediaPriceContractJSON, protocol, selector, quotaPerUnitDecimal,
		)
		if deriveErr != nil {
			return nil, types.NewErrorWithStatusCode(deriveErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		snapshot, marshalErr := common.Marshal(info.ZTAPIPublicationSnapshot)
		if marshalErr != nil {
			return nil, types.NewError(marshalErr, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		row, err = BeginZTAPIMediaReservation(ZTAPIMediaReservationInput{
			OperationID: operationID, RequestID: info.RequestId, UserID: info.UserId, TokenID: info.TokenId,
			PublicModel: info.OriginModelName, PriceSnapshotJSON: string(snapshot), SelectorJSON: canonicalSelector,
			ProtocolContractJSON: protocolJSON, ProtocolEvidenceHash: protocol.EvidenceHash, QuotaPerUnit: quotaPerUnit,
			MaximumDimensions: maximumDimensions, MaximumQuota: maximumQuota,
		})
	} else {
		snapshot, marshalErr := common.Marshal(info.ZTAPIPublicationSnapshot)
		if marshalErr != nil {
			return nil, types.NewError(marshalErr, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		}
		row, err = model.BeginZTAPIRequestSettlement(model.ZTAPIRequestSettlement{OperationID: operationID, RequestID: info.RequestId, UserID: info.UserId, TokenID: info.TokenId, TokenUnlimited: info.TokenUnlimited, PublicModel: info.OriginModelName, PriceSnapshotJSON: string(snapshot), ReservedQuota: int64(amount)})
	}
	if err != nil {
		if errors.Is(err, model.ErrInsufficientTokenQuota) || errors.Is(err, model.ErrInsufficientUserQuota) || errors.Is(err, model.ErrBalanceLedgerNegativeBalance) {
			return nil, types.NewErrorWithStatusCode(errors.New("Insufficient wallet or API key quota"), types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry())
		}
		common.SysError(fmt.Sprintf("ZTAPI reservation failed request_id=%s: %v", info.RequestId, err))
		return nil, types.NewErrorWithStatusCode(errors.New("Billing is temporarily unavailable. Please retry later."), types.ErrorCodeQueryDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	info.FinalPreConsumedQuota = int(row.ReservedQuota)
	info.BillingSource = BillingSourceWallet
	return &ztapiDurableBilling{row: row, info: info}, nil
}

func ztapiImageReservationSelector(request any) (string, error) {
	image, ok := request.(*dto.ImageRequest)
	if !ok || image == nil || image.N == nil || *image.N == 0 || image.Size == "" || image.Quality == "" || image.ResponseFormat == "" {
		return "", model.ErrZTAPISettlementInvalid
	}
	raw, err := common.Marshal(map[string]any{
		"modality": "image", "n": *image.N, "quality": image.Quality,
		"response_format": image.ResponseFormat, "size": image.Size,
	})
	return string(raw), err
}

func (s *ztapiDurableBilling) GetPreConsumedQuota() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int(s.row.ReservedQuota)
}
func (s *ztapiDurableBilling) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.row.Status == model.ZTAPISettlementReserved
}
func (s *ztapiDurableBilling) Reserve(target int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := model.ReserveZTAPIRequestSettlement(s.row.OperationID, int64(target))
	if err != nil {
		return err
	}
	s.row = row
	s.info.FinalPreConsumedQuota = int(row.ReservedQuota)
	return nil
}

func (s *ztapiDurableBilling) Settle(actual int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A bare quota amount is not enough evidence to finalize managed funds.
	if s.usageJSON == "" || s.dimensionsJSON == "" {
		return model.ErrZTAPISettlementPending
	}
	row, err := model.FinalizeZTAPIRequestSettlement(s.row.OperationID, int64(actual), s.usageJSON, s.dimensionsJSON)
	if err != nil {
		return err
	}
	s.row = row
	return nil
}

func (s *ztapiDurableBilling) Refund(c *gin.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row.Status == model.ZTAPISettlementSettled || s.row.Status == model.ZTAPISettlementReleased || s.row.Status == model.ZTAPISettlementPending {
		return nil
	}
	var row *model.ZTAPIRequestSettlement
	var err error
	if s.row.Dispatched {
		row, err = model.PendZTAPIRequestSettlement(s.row.OperationID, "{}", `["upstream_billing_unconfirmed"]`)
	} else {
		row, err = model.ReleaseZTAPIRequestSettlement(s.row.OperationID)
	}
	if err != nil {
		return err
	}
	s.row = row
	return nil
}

func MarkZTAPIBillingDispatched(info *relaycommon.RelayInfo) error {
	if info == nil {
		return nil
	}
	s, ok := info.Billing.(*ztapiDurableBilling)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := model.MarkZTAPIRequestDispatched(s.row.OperationID); err != nil {
		return err
	}
	s.row.Dispatched = true
	return nil
}

func BeginZTAPIBillingAttempt(info *relaycommon.RelayInfo, path string) error {
	if info == nil {
		return nil
	}
	s, ok := info.Billing.(*ztapiDurableBilling)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credential := fmt.Sprintf("%x", sha256.Sum256([]byte(info.ApiKey)))
	attempt, err := model.BeginZTAPIRequestAttempt(s.row.OperationID, info.ChannelId, credential, path)
	if err != nil {
		return err
	}
	s.attempt = attempt
	s.row.Dispatched = true
	return nil
}

func ObserveZTAPIBillingResponse(info *relaycommon.RelayInfo, response *http.Response) error {
	if info == nil || response == nil {
		return nil
	}
	s, ok := info.Billing.(*ztapiDurableBilling)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempt == nil {
		return model.ErrZTAPISettlementInvalid
	}
	id := ""
	for _, name := range []string{"x-request-id", "request-id", "x-goog-request-id", "x-amzn-requestid"} {
		if v := response.Header.Get(name); v != "" {
			id = v
			break
		}
	}
	return model.RecordZTAPIRequestAttemptResponse(s.row.OperationID, s.attempt.Attempt, s.attempt.ChannelID, response.StatusCode, id)
}

func pendZTAPIBilling(info *relaycommon.RelayInfo, usage any, missing []string) error {
	s, ok := info.Billing.(*ztapiDurableBilling)
	if !ok {
		return model.ErrZTAPISettlementInvalid
	}
	raw, err := common.Marshal(usage)
	if err != nil {
		return err
	}
	reasons, err := common.Marshal(missing)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := model.PendZTAPIRequestSettlement(s.row.OperationID, string(raw), string(reasons))
	if err != nil {
		return err
	}
	s.row = row
	return nil
}

func FinalizeZTAPIImageBilling(info *relaycommon.RelayInfo, evidence relaycommon.ZTAPIMediaUsageEvidence) error {
	s, ok := info.Billing.(*ztapiDurableBilling)
	if !ok {
		return model.ErrZTAPISettlementInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempt == nil {
		return model.ErrZTAPISettlementInvalid
	}
	if err := model.RecordZTAPIRequestAttemptResponse(s.row.OperationID, s.attempt.Attempt, s.attempt.ChannelID, http.StatusOK, evidence.UpstreamRequestID); err != nil {
		return err
	}
	row, err := FinalizeZTAPIImageSettlement(s.row.OperationID, evidence, s.attempt.Attempt)
	if err != nil {
		return err
	}
	s.row = row
	s.usageJSON = row.UsageJSON
	s.dimensionsJSON = row.ChargeDimensionsJSON
	return nil
}

func PendZTAPIImageBilling(info *relaycommon.RelayInfo, evidence relaycommon.ZTAPIMediaUsageEvidence, reason string) error {
	s, ok := info.Billing.(*ztapiDurableBilling)
	if !ok {
		return model.ErrZTAPISettlementInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempt == nil {
		return model.ErrZTAPISettlementInvalid
	}
	upstreamRequestID := evidence.UpstreamRequestID
	if validated, _ := info.GetZTAPIImageSettlementEvidence(); validated != nil {
		upstreamRequestID = validated.UpstreamRequestID
	}
	lineage := model.ZTAPIPendingSettlementLineage{
		FinalAttempt: s.attempt.Attempt, ChannelID: s.attempt.ChannelID, UpstreamRequestID: upstreamRequestID,
	}
	row, err := pendZTAPIImageSettlement(s.row.OperationID, evidence, reason, &lineage)
	if row != nil {
		s.row = row
	}
	return err
}

func IsZTAPIDurableBilling(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	_, ok := info.Billing.(*ztapiDurableBilling)
	return ok
}

// Finalizing the HTTP request also hands unfinished dispatched holds to the
// durable queue. It never interprets an HTTP failure as supplier non-billing.
func FinishZTAPIBilling(c *gin.Context, info *relaycommon.RelayInfo) error {
	if info == nil {
		return nil
	}
	s, ok := info.Billing.(*ztapiDurableBilling)
	if !ok {
		return nil
	}
	return s.Refund(c)
}
