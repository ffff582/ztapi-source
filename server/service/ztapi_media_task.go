package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var errZTAPIMediaUsageUnconfirmed = errors.New("ZTAPI media usage is unavailable or outside frozen authority")

func IsZTAPIMediaRequest(info *relaycommon.RelayInfo) bool {
	return info != nil && !info.IsChannelTest && info.ZTAPIPublicationSnapshot != nil && info.ZTAPIPublicationSnapshot.Modality == model.ZTAPIModalityVideo
}

func IsZTAPIMediaBilling(info *relaycommon.RelayInfo) bool {
	if !IsZTAPIMediaRequest(info) {
		return false
	}
	_, ok := info.Billing.(*ztapiDurableBilling)
	return ok
}

func RecordZTAPIMediaSubmissionIdentity(info *relaycommon.RelayInfo, status int, requestID string) error {
	if !IsZTAPIMediaBilling(info) {
		return nil
	}
	if status < 200 || status >= 300 || !validZTAPIMediaEvidenceValue(requestID, 255) {
		return model.ErrZTAPIMediaTaskInvalid
	}
	session := info.Billing.(*ztapiDurableBilling)
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.row == nil || session.attempt == nil {
		return model.ErrZTAPIMediaTaskInvalid
	}
	return model.RecordZTAPIRequestAttemptResponse(session.row.OperationID, session.attempt.Attempt, session.attempt.ChannelID, status, requestID)
}

func ValidateZTAPIMediaTaskSubmission(info *relaycommon.RelayInfo, request relaycommon.TaskSubmitReq) error {
	if !IsZTAPIMediaBilling(info) {
		return nil
	}
	if _, err := ztapiVideoPriceSelector(info.ZTAPIPublicationSnapshot.MediaPriceContractJSON, request); err != nil {
		return err
	}
	_, _, err := ztapiVideoProtocolSelector(info, request)
	return err
}

func ZTAPIVideoMaximumReservation(info *relaycommon.RelayInfo, request relaycommon.TaskSubmitReq) (int, error) {
	if !IsZTAPIMediaRequest(info) || info.ZTAPIPublicationSnapshot.VideoProtocolContract == nil {
		return 0, model.ErrZTAPIMediaTaskInvalid
	}
	priceSelector, err := ztapiVideoPriceSelector(info.ZTAPIPublicationSnapshot.MediaPriceContractJSON, request)
	if err != nil {
		return 0, err
	}
	videoSelector, _, err := ztapiVideoProtocolSelector(info, request)
	if err != nil {
		return 0, err
	}
	price, err := types.ParseZTAPIMediaPriceContract(info.ZTAPIPublicationSnapshot.MediaPriceContractJSON)
	if err != nil {
		return 0, model.ErrZTAPIMediaTaskInvalid
	}
	if _, err = types.SelectZTAPIMediaPriceRuleFromContract(price, priceSelector); err != nil {
		return 0, model.ErrZTAPIMediaTaskInvalid
	}
	quotaPerUnit := strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64)
	_, maximumQuota, err := types.CalculateZTAPIVideoMaximumReservation(price, *info.ZTAPIPublicationSnapshot.VideoProtocolContract, videoSelector, quotaPerUnit)
	if err != nil {
		return 0, model.ErrZTAPIMediaTaskInvalid
	}
	return int(maximumQuota), nil
}

func PrepareZTAPIMediaTaskSubmission(info *relaycommon.RelayInfo, request relaycommon.TaskSubmitReq) (*model.ZTAPIMediaTask, error) {
	settlementID, err := ztapiMediaSettlementIdentity(info)
	if err != nil {
		return nil, err
	}
	selector, err := ztapiVideoPriceSelector(info.ZTAPIPublicationSnapshot.MediaPriceContractJSON, request)
	if err != nil {
		return nil, err
	}
	videoSelector, protocolJSON, err := ztapiVideoProtocolSelector(info, request)
	if err != nil {
		return nil, err
	}
	mediaTask, err := model.BeginZTAPIMediaTask(model.ZTAPIMediaTaskInput{
		PublicTaskID:  info.TaskRelayInfo.PublicTaskID,
		SettlementID:  settlementID,
		Selector:      selector,
		VideoSelector: videoSelector,
		QuotaPerUnit:  strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64),
		Action:        "generate", VideoProtocolContractJSON: protocolJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("begin durable ZTAPI media task: %w", err)
	}
	return mediaTask, nil
}

func BindZTAPIMediaTaskSubmission(info *relaycommon.RelayInfo, request relaycommon.TaskSubmitReq, upstreamTaskID string) (*model.ZTAPIMediaTask, error) {
	mediaTask, err := PrepareZTAPIMediaTaskSubmission(info, request)
	if err != nil {
		return nil, err
	}
	_, attempt, err := ztapiMediaBillingIdentity(info)
	if err != nil {
		return nil, err
	}
	mediaTask, err = model.MarkZTAPIMediaTaskSubmitted(mediaTask.PublicTaskID, upstreamTaskID, attempt)
	if err != nil {
		return nil, fmt.Errorf("bind durable ZTAPI media submission: %w", err)
	}
	return mediaTask, nil
}

func TagZTAPIMediaLegacyTask(task *model.Task, info *relaycommon.RelayInfo) error {
	if task == nil {
		return errors.New("durable ZTAPI media task is missing")
	}
	settlementID, _, err := ztapiMediaBillingIdentity(info)
	if err != nil {
		return err
	}
	mediaTask, err := model.GetZTAPIMediaTask(task.TaskID)
	if err != nil {
		return fmt.Errorf("load durable ZTAPI media task: %w", err)
	}
	if mediaTask.SettlementID != settlementID || mediaTask.UserID != task.UserId || mediaTask.TokenID != info.TokenId || mediaTask.UpstreamTaskID != task.PrivateData.UpstreamTaskID {
		return model.ErrZTAPIMediaTaskConflict
	}
	task.PrivateData.ZTAPIMediaManaged = true
	task.PrivateData.ZTAPIMediaSettlementID = settlementID
	return nil
}

func IsZTAPIMediaLegacyTask(task *model.Task) bool {
	return task != nil && task.PrivateData.ZTAPIMediaManaged
}

func ApplyZTAPIMediaPollingObservation(task *model.Task, result *relaycommon.TaskInfo) (bool, error) {
	mediaTask, managed, err := loadZTAPIMediaTaskForLegacy(task)
	if err != nil || !managed {
		return managed, err
	}
	if result == nil {
		return true, model.ErrZTAPIMediaTaskInvalid
	}

	switch model.TaskStatus(result.Status) {
	case model.TaskStatusSubmitted, model.TaskStatusQueued:
		return true, nil
	case model.TaskStatusInProgress:
		_, err = ensureZTAPIMediaTaskProcessing(mediaTask)
		return true, err
	case model.TaskStatusUnknown:
		if result.UpstreamTaskID != mediaTask.UpstreamTaskID || !validZTAPIMediaEvidenceValue(result.ProviderStatus, 128) ||
			!validZTAPIMediaEvidenceValue(result.UpstreamRequestID, 255) {
			return true, model.ErrZTAPIMediaTaskInvalid
		}
		mediaTask, err = prepareZTAPIMediaTerminal(mediaTask, model.ZTAPIMediaTaskUnknown)
		if err != nil {
			return true, err
		}
		resultJSON, marshalErr := common.Marshal(map[string]string{
			"provider_status": result.ProviderStatus, "upstream_request_id": result.UpstreamRequestID,
		})
		if marshalErr != nil {
			return true, marshalErr
		}
		usageJSON, usageErr := ztapiUnresolvedMediaUsageJSON(result)
		if usageErr != nil {
			return true, usageErr
		}
		_, err = model.ApplyZTAPIMediaTaskObservation(model.ZTAPIMediaTaskObservation{
			PublicTaskID: mediaTask.PublicTaskID, State: model.ZTAPIMediaTaskUnknown, Attempt: mediaTask.Attempt,
			UpstreamTaskID: mediaTask.UpstreamTaskID, ChargeDisposition: model.ZTAPIMediaChargeUnknown,
			UsageJSON: usageJSON, ChargeDimensionsJSON: `[]`, ResultMetadataJSON: string(resultJSON),
			FailureReason: "provider_state_unknown",
		})
		return true, err
	case model.TaskStatusSuccess, model.TaskStatusFailure:
		if model.TaskStatus(result.Status) == model.TaskStatusSuccess && strings.TrimSpace(result.Url) == "" {
			return true, errors.New("durable ZTAPI media result is missing")
		}
		state := model.ZTAPIMediaTaskSucceeded
		if model.TaskStatus(result.Status) == model.TaskStatusFailure {
			state = model.ZTAPIMediaTaskFailed
		}
		mediaTask, err = prepareZTAPIMediaTerminal(mediaTask, state)
		if err != nil {
			return true, err
		}
		observation, observationErr := buildZTAPIMediaTerminalObservation(mediaTask, state, result)
		if observationErr != nil {
			return true, observationErr
		}
		_, err = model.ApplyZTAPIMediaTaskObservation(observation)
		return true, err
	default:
		return true, fmt.Errorf("unsupported durable ZTAPI media task status %q", result.Status)
	}
}

func buildZTAPIMediaTerminalObservation(mediaTask *model.ZTAPIMediaTask, state model.ZTAPIMediaTaskState, result *relaycommon.TaskInfo) (model.ZTAPIMediaTaskObservation, error) {
	invalid := model.ZTAPIMediaTaskObservation{}
	if mediaTask == nil || result == nil || result.UpstreamTaskID != mediaTask.UpstreamTaskID ||
		!validZTAPIMediaEvidenceValue(result.ProviderStatus, 128) || !validZTAPIMediaEvidenceValue(result.UpstreamRequestID, 255) {
		return invalid, model.ErrZTAPIMediaTaskInvalid
	}
	metadata := map[string]any{
		"provider_status": result.ProviderStatus, "upstream_request_id": result.UpstreamRequestID,
		"result_available": strings.TrimSpace(result.Url) != "",
	}
	if state == model.ZTAPIMediaTaskSucceeded {
		resolution, duration := result.ResultMetadata["resolution"], result.ResultMetadata["duration"]
		if !validZTAPIMediaEvidenceValue(resolution, 32) || !validZTAPIMediaEvidenceValue(duration, 32) {
			return invalid, model.ErrZTAPIMediaTaskInvalid
		}
		metadata["resolution"], metadata["duration"] = resolution, duration
	}
	metadataJSON, err := common.Marshal(metadata)
	if err != nil {
		return invalid, err
	}
	unresolvedUsageJSON, err := ztapiUnresolvedMediaUsageJSON(result)
	if err != nil {
		return invalid, err
	}
	base := model.ZTAPIMediaTaskObservation{
		PublicTaskID: mediaTask.PublicTaskID, State: state, Attempt: mediaTask.Attempt,
		UpstreamTaskID: mediaTask.UpstreamTaskID, ChargeDisposition: model.ZTAPIMediaChargeUnknown,
		UsageJSON: unresolvedUsageJSON, ChargeDimensionsJSON: `[]`, ResultMetadataJSON: string(metadataJSON),
	}
	if state == model.ZTAPIMediaTaskFailed {
		base.FailureReason = "provider_failed_billing_unconfirmed"
	}
	actual, usageJSON, dimensionsJSON, evidence, err := calculateZTAPIVideoTerminalCharge(mediaTask, result)
	if err != nil {
		if errors.Is(err, errZTAPIMediaUsageUnconfirmed) {
			return base, nil
		}
		return invalid, err
	}
	base.ChargeDisposition = model.ZTAPIMediaChargeKnown
	base.ActualQuota = actual
	base.UsageJSON = usageJSON
	base.ChargeDimensionsJSON = dimensionsJSON
	base.SettlementEvidence = evidence
	if state == model.ZTAPIMediaTaskFailed {
		base.FailureReason = "provider_failed"
	}
	return base, nil
}

func ztapiUnresolvedMediaUsageJSON(result *relaycommon.TaskInfo) (string, error) {
	// A string retains numeric lexemes, JSON types and whitespace exactly;
	// observed dimensions remain separate from confirmed charge dimensions.
	evidence := struct {
		RawUsageJSON       string            `json:"raw_usage_json,omitempty"`
		ObservedDimensions map[string]string `json:"observed_dimensions,omitempty"`
	}{result.ResultMetadata["raw_usage_json"], result.UsageDimensions}
	raw, err := common.Marshal(evidence)
	return string(raw), err
}

func calculateZTAPIVideoTerminalCharge(mediaTask *model.ZTAPIMediaTask, result *relaycommon.TaskInfo) (int64, string, string, model.ZTAPISettlementEvidence, error) {
	var settlement model.ZTAPIRequestSettlement
	if model.DB == nil || model.DB.First(&settlement, mediaTask.SettlementID).Error != nil || settlement.ID != mediaTask.SettlementID {
		return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
	}
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	var priceSelector types.ZTAPIMediaPriceSelector
	var videoSelector types.ZTAPIVideoSelector
	if common.UnmarshalJsonStr(settlement.PriceSnapshotJSON, &snapshot) != nil ||
		common.UnmarshalJsonStr(mediaTask.SelectorJSON, &priceSelector) != nil ||
		common.UnmarshalJsonStr(mediaTask.VideoSelectorJSON, &videoSelector) != nil {
		return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
	}
	protocol, canonicalProtocol, err := types.ParseZTAPIVideoProtocolContract(mediaTask.VideoProtocolContractJSON)
	if err != nil || canonicalProtocol != mediaTask.VideoProtocolContractJSON {
		return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
	}
	price, err := types.ParseZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
	if err != nil || types.ValidateZTAPIVideoPriceProtocolCompatibility(price, protocol) != nil {
		return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
	}
	rule, err := types.SelectZTAPIMediaPriceRuleFromContract(price, priceSelector)
	if err != nil || len(rule.SaleUSD) != 1 {
		return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
	}
	if _, unresolved := result.ResultMetadata["raw_usage_json"]; unresolved {
		return 0, "", "", model.ZTAPISettlementEvidence{}, errZTAPIMediaUsageUnconfirmed
	}
	if len(result.UsageDimensions) != len(protocol.Usage.Fields) {
		return 0, "", "", model.ZTAPISettlementEvidence{}, errZTAPIMediaUsageUnconfirmed
	}
	authority, ok := protocol.FindReservationAuthority(videoSelector)
	if !ok {
		return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
	}
	quotaPerUnit, err := decimal.NewFromString(mediaTask.QuotaPerUnit)
	if err != nil || !quotaPerUnit.IsPositive() || quotaPerUnit.String() != mediaTask.QuotaPerUnit {
		return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
	}
	usage := make(map[string]int64, len(result.UsageDimensions))
	dimensions := make([]model.ZTAPISupplierRefundDimension, 0, len(result.UsageDimensions))
	actualQuota := int64(0)
	for name := range protocol.Usage.Fields {
		rawQuantity, observed := result.UsageDimensions[name]
		rawMaximum, admitted := authority.MaximumDimensions[name]
		rawPrice, priced := rule.SaleUSD[name]
		quantity, quantityErr := strconv.ParseInt(rawQuantity, 10, 64)
		maximum, maximumErr := strconv.ParseInt(rawMaximum, 10, 64)
		priceValue, priceErr := decimal.NewFromString(rawPrice)
		if !admitted || !priced || maximumErr != nil || priceErr != nil || maximum < 0 ||
			strconv.FormatInt(maximum, 10) != rawMaximum {
			return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
		}
		if !observed || quantityErr != nil || quantity < 0 || quantity > maximum || strconv.FormatInt(quantity, 10) != rawQuantity {
			return 0, "", "", model.ZTAPISettlementEvidence{}, errZTAPIMediaUsageUnconfirmed
		}
		unitQuota := priceValue.Mul(quotaPerUnit).Div(decimal.NewFromInt(1_000_000))
		charged := decimal.NewFromInt(quantity).Mul(unitQuota).Round(0).IntPart()
		if charged < 0 || charged > settlement.InitialReservedQuota-actualQuota {
			return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
		}
		actualQuota += charged
		usage[name] = quantity
		dimensions = append(dimensions, model.ZTAPISupplierRefundDimension{Dimension: name, Units: rawQuantity, UnitQuota: unitQuota.String(), ChargedQuota: charged})
	}
	usageJSON, err := common.Marshal(usage)
	if err != nil {
		return 0, "", "", model.ZTAPISettlementEvidence{}, err
	}
	dimensionsJSON, err := common.Marshal(dimensions)
	if err != nil {
		return 0, "", "", model.ZTAPISettlementEvidence{}, err
	}
	var attempt model.ZTAPIRequestAttempt
	if model.DB.Where("settlement_id = ? AND attempt = ?", settlement.ID, mediaTask.Attempt).Take(&attempt).Error != nil {
		return 0, "", "", model.ZTAPISettlementEvidence{}, model.ErrZTAPIMediaTaskInvalid
	}
	evidence := model.ZTAPISettlementEvidence{FinalAttempt: mediaTask.Attempt, ConsumeLog: model.Log{
		Type: model.LogTypeConsume, UserId: settlement.UserID, TokenId: settlement.TokenID,
		RequestId: settlement.RequestID, ModelName: settlement.PublicModel, Quota: int(actualQuota),
		ChannelId: attempt.ChannelID, CreatedAt: settlement.CreatedAt.Unix(), UpstreamRequestId: attempt.UpstreamRequestID,
	}}
	return actualQuota, string(usageJSON), string(dimensionsJSON), evidence, nil
}

func validZTAPIMediaEvidenceValue(value string, max int) bool {
	return value != "" && len(value) <= max && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func ApplyZTAPIMediaTimeout(task *model.Task, _ string) (bool, error) {
	mediaTask, managed, err := loadZTAPIMediaTaskForLegacy(task)
	if err != nil || !managed {
		return managed, err
	}
	mediaTask, err = prepareZTAPIMediaTerminal(mediaTask, model.ZTAPIMediaTaskUnknown)
	if err != nil {
		return true, err
	}
	_, err = model.ApplyZTAPIMediaTaskObservation(model.ZTAPIMediaTaskObservation{
		PublicTaskID: mediaTask.PublicTaskID, State: model.ZTAPIMediaTaskUnknown,
		Attempt: mediaTask.Attempt, UpstreamTaskID: mediaTask.UpstreamTaskID,
		ChargeDisposition: model.ZTAPIMediaChargeUnknown,
		UsageJSON:         `{"provider_status":"timeout"}`, ChargeDimensionsJSON: "[]",
		ResultMetadataJSON: "{}", FailureReason: "polling_timeout_billing_unconfirmed",
	})
	return true, err
}

func ztapiMediaBillingIdentity(info *relaycommon.RelayInfo) (uint, int, error) {
	settlementID, err := ztapiMediaSettlementIdentity(info)
	if err != nil {
		return 0, 0, err
	}
	session := info.Billing.(*ztapiDurableBilling)
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.attempt == nil || session.attempt.Attempt < 1 {
		return 0, 0, errors.New("durable ZTAPI media billing identity is incomplete")
	}
	return settlementID, session.attempt.Attempt, nil
}

func ztapiMediaSettlementIdentity(info *relaycommon.RelayInfo) (uint, error) {
	if !IsZTAPIMediaBilling(info) || info.TaskRelayInfo == nil || strings.TrimSpace(info.TaskRelayInfo.PublicTaskID) == "" {
		return 0, errors.New("durable ZTAPI media billing identity is missing")
	}
	session := info.Billing.(*ztapiDurableBilling)
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.row == nil || session.row.ID == 0 {
		return 0, errors.New("durable ZTAPI media billing identity is incomplete")
	}
	return session.row.ID, nil
}

func ztapiVideoPriceSelector(raw string, request relaycommon.TaskSubmitReq) (model.ZTAPIMediaPriceSelector, error) {
	contract, err := types.ParseZTAPIMediaPriceContract(raw)
	if err != nil || contract.Modality != model.ZTAPIModalityVideo || len(contract.Rules) == 0 {
		return model.ZTAPIMediaPriceSelector{}, model.ErrZTAPIMediaTaskInvalid
	}
	conditions := make(map[string]string, len(contract.Rules[0].Conditions))
	for key := range contract.Rules[0].Conditions {
		switch key {
		case "contains_video_input":
			if strings.TrimSpace(request.Image) != "" || len(request.Images) > 0 {
				return model.ZTAPIMediaPriceSelector{}, errors.New("durable ZTAPI media input contract is unverified")
			}
			conditions[key] = strconv.FormatBool(strings.TrimSpace(request.InputReference) != "")
		case "resolution":
			resolution := strings.TrimSpace(request.Size)
			if value, ok := request.Metadata["resolution"].(string); ok && strings.TrimSpace(value) != "" {
				resolution = strings.TrimSpace(value)
			}
			if resolution == "" {
				return model.ZTAPIMediaPriceSelector{}, errors.New("durable ZTAPI media resolution is missing")
			}
			conditions[key] = strings.ToLower(resolution)
		default:
			return model.ZTAPIMediaPriceSelector{}, model.ErrZTAPIMediaTaskInvalid
		}
	}
	selector := model.ZTAPIMediaPriceSelector{Modality: model.ZTAPIModalityVideo, Conditions: conditions}
	if _, err = model.SelectZTAPIMediaPriceRule(raw, selector); err != nil {
		return model.ZTAPIMediaPriceSelector{}, err
	}
	return selector, nil
}

func ztapiVideoProtocolSelector(info *relaycommon.RelayInfo, request relaycommon.TaskSubmitReq) (types.ZTAPIVideoSelector, string, error) {
	if info == nil || info.ZTAPIPublicationSnapshot == nil || info.ZTAPIPublicationSnapshot.VideoProtocolContract == nil {
		return types.ZTAPIVideoSelector{}, "", errors.New("frozen ZTAPI video protocol contract is required")
	}
	sealed, canonical, err := types.SealZTAPIVideoProtocolContract(*info.ZTAPIPublicationSnapshot.VideoProtocolContract)
	if err != nil {
		return types.ZTAPIVideoSelector{}, "", errors.New("frozen ZTAPI video protocol contract is invalid")
	}
	if info.UpstreamModelName == "" || sealed.ProviderModel != info.UpstreamModelName {
		return types.ZTAPIVideoSelector{}, "", errors.New("frozen ZTAPI video protocol model does not match selected upstream model")
	}
	if strings.TrimSpace(request.Image) != "" || len(request.Images) > 0 {
		return types.ZTAPIVideoSelector{}, "", errors.New("image input is not an evidenced video input")
	}
	resolution := strings.ToLower(strings.TrimSpace(request.Size))
	if value, ok := request.Metadata["resolution"].(string); ok && strings.TrimSpace(value) != "" {
		resolution = strings.ToLower(strings.TrimSpace(value))
	}
	duration := request.Duration
	if duration == 0 && strings.TrimSpace(request.Seconds) != "" {
		duration, err = strconv.Atoi(strings.TrimSpace(request.Seconds))
		if err != nil {
			return types.ZTAPIVideoSelector{}, "", errors.New("video duration is invalid")
		}
	}
	selector := types.ZTAPIVideoSelector{
		Resolution: resolution, DurationSeconds: duration,
		ContainsVideoInput: strings.TrimSpace(request.InputReference) != "",
	}
	if _, ok := sealed.FindReservationAuthority(selector); !ok {
		return types.ZTAPIVideoSelector{}, "", errors.New("video selector is not admitted by the frozen protocol contract")
	}
	return selector, canonical, nil
}

func loadZTAPIMediaTaskForLegacy(task *model.Task) (*model.ZTAPIMediaTask, bool, error) {
	if task == nil || !task.PrivateData.ZTAPIMediaManaged {
		return nil, false, nil
	}
	mediaTask, err := model.GetZTAPIMediaTask(task.TaskID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, true, model.ErrZTAPIMediaTaskConflict
		}
		return nil, true, err
	}
	if mediaTask.SettlementID != task.PrivateData.ZTAPIMediaSettlementID || mediaTask.UserID != task.UserId || mediaTask.TokenID != task.PrivateData.TokenId || mediaTask.UpstreamTaskID != task.GetUpstreamTaskID() {
		return nil, true, model.ErrZTAPIMediaTaskConflict
	}
	return mediaTask, true, nil
}

func ensureZTAPIMediaTaskProcessing(task *model.ZTAPIMediaTask) (*model.ZTAPIMediaTask, error) {
	if task.State == model.ZTAPIMediaTaskProcessing {
		return task, nil
	}
	if task.State != model.ZTAPIMediaTaskSubmitted {
		return nil, model.ErrZTAPIMediaTaskConflict
	}
	return model.ApplyZTAPIMediaTaskObservation(model.ZTAPIMediaTaskObservation{
		PublicTaskID: task.PublicTaskID, State: model.ZTAPIMediaTaskProcessing,
		Attempt: task.Attempt, UpstreamTaskID: task.UpstreamTaskID,
	})
}

func prepareZTAPIMediaTerminal(task *model.ZTAPIMediaTask, target model.ZTAPIMediaTaskState) (*model.ZTAPIMediaTask, error) {
	switch task.State {
	case model.ZTAPIMediaTaskSubmitted:
		return ensureZTAPIMediaTaskProcessing(task)
	case model.ZTAPIMediaTaskProcessing, model.ZTAPIMediaTaskUnknown, target:
		return task, nil
	default:
		return nil, model.ErrZTAPIMediaTaskConflict
	}
}
