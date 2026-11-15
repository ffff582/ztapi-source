package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service/openaicompat"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const workbenchRelayHoldKey = "ztapi_workbench_native_hold"
const workbenchPilotPolicyKey = "ztapi_workbench_native_pilot_policy"

func BindZTAPIWorkbenchPilotPolicy(c *gin.Context, policy model.ZTAPIWorkbenchPilotPolicy) {
	c.Set(workbenchPilotPolicyKey, policy)
}

func ZTAPIWorkbenchPilotPolicies(c *gin.Context) []model.ZTAPIWorkbenchPilotPolicy {
	if value, ok := c.Get(workbenchPilotPolicyKey); ok {
		if p, ok := value.(model.ZTAPIWorkbenchPilotPolicy); ok {
			return []model.ZTAPIWorkbenchPilotPolicy{p}
		}
	}
	return nil
}

var workbenchRelayUUID = regexp.MustCompile(`^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)

type workbenchRelayHold struct{ task, hold string }

// BindZTAPIWorkbenchRelayHold is an explicit opt-in by an authenticated route.
// Ordinary relay requests cannot opt in by supplying client headers alone.
func BindZTAPIWorkbenchRelayHold(c *gin.Context, task, hold string) {
	c.Set(workbenchRelayHoldKey, workbenchRelayHold{task, hold})
}

func IsZTAPIWorkbenchRelay(c *gin.Context) bool {
	if c == nil {
		return false
	}
	_, ok := c.Get(workbenchRelayHoldKey)
	return ok
}

func workbenchRelayError(status int) *types.NewAPIError {
	return types.NewErrorWithStatusCode(errors.New("workbench reservation unavailable"), types.ErrorCodeInvalidRequest, status, types.ErrOptionWithSkipRetry())
}

func adoptZTAPIWorkbenchHold(c *gin.Context, amount int, info *relaycommon.RelayInfo) *types.NewAPIError {
	value, _ := c.Get(workbenchRelayHoldKey)
	bound, ok := value.(workbenchRelayHold)
	if !ok || !workbenchRelayUUID.MatchString(bound.task) || !workbenchRelayUUID.MatchString(bound.hold) || info == nil ||
		info.UserId < 1 || info.TokenId < 1 || info.TokenUnlimited || info.Billing != nil || amount < 0 || info.IsChannelTest ||
		info.RelayMode != relayconstant.RelayModeResponses || info.UserSetting.BillingPreference == "subscription_only" {
		return workbenchRelayError(http.StatusBadRequest)
	}
	price := info.ZTAPIPublicationSnapshot
	if price == nil || price.Modality != model.ZTAPIModalityText || price.PublicationID <= 0 || price.Version == 0 ||
		price.PublicName != info.OriginModelName || price.SourceModel == "" || len(price.AllowedChannelIDs) == 0 {
		return workbenchRelayError(http.StatusConflict)
	}
	var row model.ZTAPIRequestSettlement
	if model.DB == nil {
		return workbenchRelayError(http.StatusServiceUnavailable)
	}
	err := model.DB.Where("operation_id = ? AND workbench_task_id = ? AND user_id = ? AND token_id = ? AND public_model = ?", bound.hold, bound.task, info.UserId, info.TokenId, price.PublicName).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return workbenchRelayError(http.StatusConflict)
	}
	if err != nil {
		return workbenchRelayError(http.StatusServiceUnavailable)
	}
	if row.OperationID != bound.hold || row.WorkbenchTaskID != bound.task || row.Status != model.ZTAPISettlementReserved || row.Dispatched || row.TokenUnlimited ||
		row.TokenReservedQuota != row.ReservedQuota || row.WorkbenchOutboundMax < 1 || row.WorkbenchOutboundMax > ZTAPIWorkbenchMaxWire ||
		row.WorkbenchOutputMax < 1 || row.WorkbenchOutputMax > 4096 {
		return workbenchRelayError(http.StatusConflict)
	}
	if int64(amount) > row.ReservedQuota {
		return workbenchRelayError(http.StatusPaymentRequired)
	}
	frozen, err := common.Marshal(price)
	if err != nil || string(frozen) != row.PriceSnapshotJSON {
		return workbenchRelayError(http.StatusConflict)
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil || storage.Size() > 196608 {
		return workbenchRelayError(http.StatusBadRequest)
	}
	body, err := storage.Bytes()
	if err != nil {
		return workbenchRelayError(http.StatusConflict)
	}
	digest := common.HashZTAPIKey(string(body))
	if row.WorkbenchBodySHA256 == "" && row.WorkbenchStage == "continuation" {
		output, parseErr := ZTAPIWorkbenchWireOutput(body, price.PublicName)
		if parseErr != nil || output > row.WorkbenchOutputMax {
			return workbenchRelayError(http.StatusConflict)
		}
		if err := model.BindZTAPIWorkbenchContinuation(bound.task, bound.hold, info.UserId, info.TokenId, digest, string(frozen), int64(len(body))); err != nil {
			return workbenchRelayError(http.StatusConflict)
		}
		row.WorkbenchBodySHA256 = digest
	}
	if digest != row.WorkbenchBodySHA256 {
		return workbenchRelayError(http.StatusConflict)
	}
	info.RequestId = row.RequestID
	info.FinalPreConsumedQuota = int(row.ReservedQuota)
	info.BillingSource = BillingSourceWallet
	policy, _ := c.Get(workbenchPilotPolicyKey)
	pilot, _ := policy.(model.ZTAPIWorkbenchPilotPolicy)
	info.Billing = &ztapiDurableBilling{row: &row, info: info, pilot: pilot}
	return nil
}

// CheckZTAPIWorkbenchOutbound runs against the final, rewritten HTTP request
// before an attempt is recorded. Only approved text-only wire shapes are allowed.
func CheckZTAPIWorkbenchOutbound(info *relaycommon.RelayInfo, req *http.Request) error {
	if info == nil {
		return model.ErrZTAPISettlementInvalid
	}
	s, ok := info.Billing.(*ztapiDurableBilling)
	if !ok || s.row.WorkbenchTaskID == "" {
		return model.ErrZTAPISettlementInvalid
	}
	if req == nil || req.URL == nil || info.ChannelMeta == nil || info.ZTAPIPublicationSnapshot == nil ||
		req.Method != http.MethodPost || req.URL.RawPath != "" || req.URL.RawQuery != "" || req.URL.ForceQuery || req.URL.Fragment != "" || req.Body == nil ||
		!strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("workbench outbound route or content type rejected: %w", model.ErrZTAPISettlementInvalid)
	}
	source := info.ZTAPIPublicationSnapshot.SourceModel
	upstream := info.UpstreamModelName
	if upstream == "" {
		return model.ErrZTAPISettlementInvalid
	}
	chatMode := openaicompat.ShouldResponsesUseChatCompletions(upstream)
	path := "/v1/responses"
	if chatMode {
		path = "/v1/chat/completions"
	}
	if req.URL.String() != relaycommon.GetFullRequestURL(info.ChannelBaseUrl, path, info.ChannelType) {
		return fmt.Errorf("workbench outbound route differs from selected channel: %w", model.ErrZTAPISettlementInvalid)
	}
	limit := int64(s.row.WorkbenchOutboundMax)
	if req.ContentLength > limit {
		return model.ErrZTAPIWorkbenchBudget
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, limit+1))
	closeErr := req.Body.Close()
	if err != nil || closeErr != nil || len(data) == 0 || int64(len(data)) > limit {
		return model.ErrZTAPIWorkbenchBudget
	}
	var output int
	if chatMode {
		var chat struct {
			Model       string          `json:"model"`
			Messages    json.RawMessage `json:"messages"`
			Stream      bool            `json:"stream"`
			MaxTokens   int             `json:"max_tokens"`
			MaxComplete int             `json:"max_completion_tokens"`
			Tools       json.RawMessage `json:"tools"`
			ToolChoice  string          `json:"tool_choice"`
			Parallel    *bool           `json:"parallel_tool_calls"`
			Options     struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if !strings.HasPrefix(source, "claude-") || !openaicompat.ShouldResponsesUseChatCompletions(source) ||
			!decodeWorkbenchWire(data, &chat) || chat.Model != upstream || !chat.Stream || !chat.Options.IncludeUsage ||
			(chat.MaxTokens != 0 && chat.MaxComplete != 0) || !workbenchToolChoice(chat.ToolChoice) ||
			!workbenchToolDeclarations(chat.Tools, false) || !workbenchTextInput(chat.Messages, false) {
			return fmt.Errorf("workbench converted chat shape rejected: %w", model.ErrZTAPISettlementInvalid)
		}
		output = max(chat.MaxTokens, chat.MaxComplete)
	} else {
		if openaicompat.ShouldResponsesUseChatCompletions(source) {
			return model.ErrZTAPISettlementInvalid
		}
		output, err = ZTAPIWorkbenchWireOutput(data, upstream)
		if err != nil {
			return err
		}
	}
	if output > s.row.WorkbenchOutputMax {
		return fmt.Errorf("workbench outbound output limit exceeds hold: %w", model.ErrZTAPIWorkbenchBudget)
	}
	if output < 1 {
		return fmt.Errorf("workbench outbound controls rejected: %w", model.ErrZTAPISettlementInvalid)
	}
	if s.pilot.BatchID != "" {
		if source != "claude-fable-5" || upstream != source || s.pilot.UserID != s.row.UserID ||
			s.pilot.ChannelID != info.ChannelId || !chatMode || req.URL.Path != "/v1/chat/completions" {
			return model.ErrZTAPISettlementConflict
		}
		if len(data) > 131072 || output > 2048 {
			return model.ErrZTAPIWorkbenchPilotLimit
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.ContentLength = int64(len(data))
	req.GetBody = nil
	req.Header.Del("Idempotency-Key")
	req.Header.Del("X-Idempotency-Key")
	req.Header.Del("X-ZTAPI-Task")
	req.Header.Del("X-ZTAPI-Hold")
	req.Header.Del("X-ZTAPI-Workbench-Key")
	return nil
}
