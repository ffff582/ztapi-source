package service

import (
	"errors"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

const hostedToolUnknownReason = `["hosted_tool_billing_unconfirmed"]`

var (
	ErrZTAPIHostedToolAlreadyDispatched = errors.New("hosted tool operation was already dispatched; provider replay forbidden")
	hostedToolOperationID               = regexp.MustCompile(`^hstool_[a-f0-9]{32}$`)
	hostedToolRootID                    = regexp.MustCompile(`^hsroot_[a-f0-9]{32}$`)
	hostedToolHash                      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	hostedToolOpaqueID                  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)
	hostedToolDecimal                   = regexp.MustCompile(`^[0-9]{1,20}(\.[0-9]{1,18})?$`)
)

// Construct only from authenticated server state and the explicit official-tool
// configuration. Query, URL, credentials, and caller-owned operation IDs have no fields.
type ZTAPIHostedToolBillingInput struct {
	UserID            int
	TokenID           int
	ParentRequestID   string
	Action            string
	OfficialChannelID int
	CredentialVersion string
}

type ZTAPIHostedToolBillingResult struct {
	Outcome           string
	HTTPStatus        int
	UpstreamRequestID string
	ResponseSHA256    string
	NonEmpty          bool
}

type ztapiHostedToolPriceSnapshot struct {
	SchemaVersion        int    `json:"schema_version"`
	BillingKind          string `json:"billing_kind"`
	Provider             string `json:"provider"`
	Tool                 string `json:"tool"`
	Unit                 string `json:"unit"`
	Currency             string `json:"currency"`
	SalePerCallCNY       string `json:"sale_per_call_cny"`
	QuotedCostPerCallCNY string `json:"quoted_cost_per_call_cny"`
	CNYPerU              string `json:"cny_per_u"`
	QuotaPerUnit         int64  `json:"quota_per_unit"`
	FXPolicyID           int64  `json:"fx_policy_id"`
	ChannelID            int    `json:"channel_id"`
	CredentialVersion    string `json:"credential_version"`
	ParentRequestID      string `json:"parent_request_id"`
}

type ztapiHostedToolUsage struct {
	SchemaVersion     int    `json:"schema_version"`
	BillingKind       string `json:"billing_kind"`
	Outcome           string `json:"outcome"`
	NonEmpty          bool   `json:"nonempty"`
	HTTPStatus        int    `json:"http_status"`
	UpstreamRequestID string `json:"upstream_request_id"`
	ResponseSHA256    string `json:"response_sha256"`
	BillableCalls     *int   `json:"billable_calls"`
	Unit              string `json:"unit"`
	SupplierCostState string `json:"supplier_cost_state"`
}

type ztapiHostedToolPrice struct {
	quota     int64
	actual    int64
	unitQuota string
	saleU     string
}

// Reserve always creates a new server-owned operation. Persist its returned IDs
// for settlement/recovery; do not call Reserve again to retry the same provider operation.
func ReserveZTAPIHostedToolBilling(input ZTAPIHostedToolBillingInput) (*model.ZTAPIRequestSettlement, error) {
	tool := "search"
	if input.Action == "open_page" {
		tool = "fetch"
	} else if input.Action != "search" {
		return nil, model.ErrZTAPISettlementInvalid
	}
	if model.DB == nil || input.UserID <= 0 || input.TokenID <= 0 || !hostedToolRootID.MatchString(input.ParentRequestID) ||
		!hostedToolHash.MatchString(input.CredentialVersion) {
		return nil, model.ErrZTAPISettlementInvalid
	}
	if err := validateHostedToolChannel(input.OfficialChannelID); err != nil {
		return nil, err
	}
	policy, err := model.CurrentZTAPIFXPolicy(model.DB)
	if err != nil || policy == nil || policy.ID <= 0 {
		return nil, model.ErrZTAPISettlementInvalid
	}
	rate, err := decimal.NewFromString(policy.PlatformCNYPerUSDT)
	if err != nil || !rate.IsPositive() {
		return nil, model.ErrZTAPISettlementInvalid
	}
	quotaRaw, err := canonicalZTAPIQuotaPerUnit(common.QuotaPerUnit)
	if err != nil {
		return nil, err
	}
	quota, err := strconv.ParseInt(quotaRaw, 10, 64)
	if err != nil || quota <= 0 {
		return nil, model.ErrZTAPISettlementInvalid
	}
	frozen := ztapiHostedToolPriceSnapshot{SchemaVersion: 1, BillingKind: "hosted_tool", Provider: "deepseek_official",
		Tool: tool, Unit: "call", Currency: "CNY", SalePerCallCNY: "0.01", QuotedCostPerCallCNY: "0.005",
		CNYPerU: rate.String(), QuotaPerUnit: quota, FXPolicyID: policy.ID, ChannelID: input.OfficialChannelID,
		CredentialVersion: input.CredentialVersion, ParentRequestID: input.ParentRequestID}
	price, err := priceHostedTool(frozen)
	if err != nil {
		return nil, err
	}
	raw, err := common.Marshal(frozen)
	if err != nil {
		return nil, err
	}
	var token model.Token
	if err := model.DB.Select("id", "user_id", "unlimited_quota").First(&token, input.TokenID).Error; err != nil || token.UserId != input.UserID {
		return nil, model.ErrZTAPISettlementInvalid
	}
	op := "hstool_" + common.GetUUID()
	return model.BeginZTAPIRequestSettlement(model.ZTAPIRequestSettlement{
		OperationID: op, RequestID: op, UserID: input.UserID, TokenID: input.TokenID, TokenUnlimited: token.UnlimitedQuota,
		PublicModel: "ztapi-tool-" + tool, PriceSnapshotJSON: string(raw), ReservedQuota: price.quota,
	})
}

// A successful return grants provider dispatch once. Every repeat, including a
// process restart with an existing attempt, is an error and must not call the provider.
func MarkZTAPIHostedToolBillingDispatched(operationID string) (*model.ZTAPIRequestAttempt, error) {
	row, frozen, _, err := loadHostedToolSettlement(operationID)
	if err != nil {
		return nil, err
	}
	if row.Dispatched {
		return nil, ErrZTAPIHostedToolAlreadyDispatched
	}
	if row.Status != model.ZTAPISettlementReserved || row.UsageJSON != "{}" {
		return nil, model.ErrZTAPISettlementPending
	}
	if err := validateHostedToolChannel(frozen.ChannelID); err != nil {
		return nil, err
	}
	return model.BeginZTAPIRequestAttempt(operationID, frozen.ChannelID, frozen.CredentialVersion, "/v1/tools/"+frozen.Tool)
}

func FinalizeZTAPIHostedToolBilling(operationID string, result ZTAPIHostedToolBillingResult) (*model.ZTAPIRequestSettlement, error) {
	if !validHostedToolResult(result) {
		return nil, model.ErrZTAPISettlementInvalid
	}
	row, frozen, price, err := loadHostedToolSettlement(operationID)
	if err != nil {
		return nil, err
	}
	if !row.Dispatched {
		if result.Outcome != "rejected" || result.HTTPStatus != 0 {
			return nil, model.ErrZTAPISettlementInvalid
		}
		return model.ReleaseZTAPIRequestSettlement(operationID)
	}
	var attempts []model.ZTAPIRequestAttempt
	if err := model.DB.Where("settlement_id = ?", row.ID).Find(&attempts).Error; err != nil {
		return nil, err
	}
	if len(attempts) != 1 || attempts[0].Attempt != 1 || attempts[0].ChannelID != frozen.ChannelID ||
		attempts[0].CredentialVersion != frozen.CredentialVersion || attempts[0].Protocol != "/v1/tools/"+frozen.Tool {
		return nil, model.ErrZTAPISettlementConflict
	}
	usage, err := marshalHostedToolUsage(result)
	if err != nil {
		return nil, err
	}
	// Claim the first sanitized receipt with an atomic metadata-only CAS. Unknown
	// enters its private pending state in the same write, before a rejection HTTP
	// status could make a generic recovery worker consider releasing the hold.
	updates := map[string]any{"usage_json": usage}
	if result.Outcome == "unknown" {
		updates["status"] = model.ZTAPISettlementPending
		updates["missing_dimensions_json"] = hostedToolUnknownReason
	}
	claim := model.DB.Model(&model.ZTAPIRequestSettlement{}).
		Where("id = ? AND status = ? AND dispatched = ? AND usage_json = ? AND missing_dimensions_json = ?",
			row.ID, model.ZTAPISettlementReserved, true, "{}", "[]").Updates(updates)
	if claim.Error != nil {
		return nil, claim.Error
	}
	row, _, _, err = loadHostedToolSettlement(operationID)
	if err != nil {
		return nil, err
	}
	if row.UsageJSON != usage {
		return nil, model.ErrZTAPISettlementConflict
	}
	if row.Status == model.ZTAPISettlementReleased || (row.Status == model.ZTAPISettlementPending &&
		row.MissingDimensionsJSON != `["settlement_retry_required"]` && result.Outcome != "unknown") {
		return nil, model.ErrZTAPISettlementPending
	}
	if row.Status != model.ZTAPISettlementSettled {
		if err := model.RecordZTAPIRequestAttemptResponse(operationID, 1, frozen.ChannelID, result.HTTPStatus, result.UpstreamRequestID); err != nil {
			return row, err
		}
	}
	if result.Outcome == "unknown" {
		return model.PendZTAPIRequestSettlement(operationID, usage, hostedToolUnknownReason)
	}
	actual := int64(0)
	dimensions := []model.ZTAPISupplierRefundDimension{}
	quantity, quoteState, unitPrice := 0, "free", "0"
	if result.Outcome == "succeeded" {
		actual, quantity, quoteState, unitPrice = price.actual, 1, "quoted", price.saleU
		dimensions = append(dimensions, model.ZTAPISupplierRefundDimension{Dimension: "tool_call", Units: "1",
			UnitQuota: price.unitQuota, ChargedQuota: actual})
	}
	rawDimensions, err := common.Marshal(dimensions)
	if err != nil {
		return nil, err
	}
	other, err := common.Marshal(map[string]any{"billing_source": "wallet", "billing_status": "settled",
		"billing_dimensions": []map[string]any{{"dimension": "tool_call", "quantity": quantity,
			"unit": "call", "quote_state": quoteState, "unit_price_usd": unitPrice}}})
	if err != nil {
		return nil, err
	}
	evidence := model.ZTAPISettlementEvidence{FinalAttempt: 1, ConsumeLog: model.Log{
		UserId: row.UserID, TokenId: row.TokenID, RequestId: row.RequestID, ModelName: row.PublicModel,
		ChannelId: frozen.ChannelID, Type: model.LogTypeConsume, Quota: int(actual), CreatedAt: row.CreatedAt.Unix(),
		UpstreamRequestId: result.UpstreamRequestID, Other: string(other),
	}}
	settled, err := model.FinalizeZTAPIRequestSettlementWithEvidence(operationID, actual, usage, string(rawDimensions), evidence)
	if err != nil {
		var intent model.ZTAPISettlementFinalizationIntent
		if lookupErr := model.DB.Where("operation_id = ? AND completed_at = 0", operationID).Take(&intent).Error; lookupErr == nil && intent.UsageJSON == usage {
			_, _ = model.PendZTAPIRequestSettlement(operationID, usage, `["settlement_retry_required"]`)
		}
	}
	return settled, err
}

// Readiness is checked before model dispatch as well as before each tool charge.
func CheckZTAPIHostedToolSource(id int) error { return validateHostedToolChannel(id) }

func validateHostedToolChannel(id int) error {
	if model.DB == nil || id <= 0 {
		return model.ErrZTAPISettlementInvalid
	}
	var channel model.Channel
	if err := model.DB.Select("id", "status", "base_url").First(&channel, id).Error; err != nil ||
		channel.Status != common.ChannelStatusEnabled || channel.BaseURL == nil {
		return model.ErrZTAPISettlementInvalid
	}
	u, err := url.Parse(*channel.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host != "api.deepseek.com" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || strings.Contains(*channel.BaseURL, "#") {
		return model.ErrZTAPISettlementInvalid
	}
	var abilities int64
	if err := model.DB.Model(&model.Ability{}).Where("channel_id = ?", id).Count(&abilities).Error; err != nil {
		return err
	}
	if abilities != 0 {
		return model.ErrZTAPISettlementInvalid
	}
	return nil
}

func loadHostedToolSettlement(op string) (*model.ZTAPIRequestSettlement, ztapiHostedToolPriceSnapshot, ztapiHostedToolPrice, error) {
	var row model.ZTAPIRequestSettlement
	var frozen ztapiHostedToolPriceSnapshot
	var price ztapiHostedToolPrice
	if model.DB == nil || !hostedToolOperationID.MatchString(op) {
		return nil, frozen, price, model.ErrZTAPISettlementInvalid
	}
	if err := model.DB.Where("operation_id = ?", op).Take(&row).Error; err != nil {
		return nil, frozen, price, err
	}
	if len(row.PriceSnapshotJSON) > 8192 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(row.PriceSnapshotJSON)) != nil ||
		common.DecodeJsonStrict(strings.NewReader(row.PriceSnapshotJSON), &frozen) != nil ||
		row.RequestID != op || row.UserID <= 0 || row.TokenID <= 0 || frozen.SchemaVersion != 1 || frozen.BillingKind != "hosted_tool" ||
		frozen.Provider != "deepseek_official" || (frozen.Tool != "search" && frozen.Tool != "fetch") || frozen.Unit != "call" ||
		frozen.Currency != "CNY" || frozen.SalePerCallCNY != "0.01" || frozen.QuotedCostPerCallCNY != "0.005" ||
		frozen.ChannelID <= 0 || frozen.FXPolicyID <= 0 || !hostedToolHash.MatchString(frozen.CredentialVersion) ||
		!hostedToolRootID.MatchString(frozen.ParentRequestID) || row.PublicModel != "ztapi-tool-"+frozen.Tool {
		return nil, frozen, price, model.ErrZTAPISettlementInvalid
	}
	var policy model.ZTAPIFXPolicy
	if err := model.DB.First(&policy, frozen.FXPolicyID).Error; err != nil {
		return nil, frozen, price, err
	}
	rate, err := decimal.NewFromString(policy.PlatformCNYPerUSDT)
	if err != nil || rate.String() != frozen.CNYPerU {
		return nil, frozen, price, model.ErrZTAPISettlementConflict
	}
	price, err = priceHostedTool(frozen)
	if err != nil || row.InitialReservedQuota != price.quota || row.ReservedQuota != price.quota {
		return nil, frozen, price, model.ErrZTAPISettlementConflict
	}
	return &row, frozen, price, nil
}

func priceHostedTool(frozen ztapiHostedToolPriceSnapshot) (ztapiHostedToolPrice, error) {
	if !hostedToolDecimal.MatchString(frozen.CNYPerU) || frozen.QuotaPerUnit <= 0 {
		return ztapiHostedToolPrice{}, model.ErrZTAPISettlementInvalid
	}
	rate, ok := new(big.Rat).SetString(frozen.CNYPerU)
	if !ok || rate.Sign() <= 0 {
		return ztapiHostedToolPrice{}, model.ErrZTAPISettlementInvalid
	}
	sale := big.NewRat(1, 100)
	sale.Quo(sale, rate)
	exact := new(big.Rat).Mul(sale, new(big.Rat).SetInt64(frozen.QuotaPerUnit))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(exact.Num(), exact.Denom(), remainder)
	// Reserve upward, then settle with the existing positive-charge half-up
	// rounding convention. Both calculations use exact rational configured FX.
	actual := new(big.Int).Set(quotient)
	if new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(exact.Denom()) >= 0 {
		actual.Add(actual, big.NewInt(1))
	}
	if actual.Sign() == 0 {
		actual.SetInt64(1)
	}
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Sign() <= 0 || quotient.Int64() > math.MaxInt32 {
		return ztapiHostedToolPrice{}, model.ErrZTAPISettlementInvalid
	}
	unitQuota, _ := decimal.NewFromString(exact.FloatString(18))
	saleU, _ := decimal.NewFromString(sale.FloatString(18))
	return ztapiHostedToolPrice{quota: quotient.Int64(), actual: actual.Int64(), unitQuota: unitQuota.String(), saleU: saleU.String()}, nil
}

func validHostedToolResult(result ZTAPIHostedToolBillingResult) bool {
	if result.HTTPStatus < 0 || result.HTTPStatus > 599 ||
		(result.UpstreamRequestID != "" && !hostedToolOpaqueID.MatchString(result.UpstreamRequestID)) ||
		(result.ResponseSHA256 != "" && !hostedToolHash.MatchString(result.ResponseSHA256)) {
		return false
	}
	switch result.Outcome {
	case "succeeded":
		return result.NonEmpty && result.HTTPStatus >= 200 && result.HTTPStatus < 300 && hostedToolHash.MatchString(result.ResponseSHA256)
	case "empty":
		return !result.NonEmpty && result.HTTPStatus >= 200 && result.HTTPStatus < 300 && hostedToolHash.MatchString(result.ResponseSHA256)
	case "rejected":
		if result.NonEmpty {
			return false
		}
		if result.HTTPStatus == 0 {
			return result.UpstreamRequestID == "" && result.ResponseSHA256 == ""
		}
		return result.HTTPStatus >= 400 && result.HTTPStatus <= 499 && result.HTTPStatus != 408 &&
			hostedToolHash.MatchString(result.ResponseSHA256)
	case "unknown":
		return true
	}
	return false
}

func marshalHostedToolUsage(result ZTAPIHostedToolBillingResult) (string, error) {
	quantity, costState := 0, "confirmed_zero"
	var billable *int
	if result.Outcome == "succeeded" {
		quantity, costState = 1, "quoted_not_actual"
	}
	if result.Outcome == "unknown" {
		costState = "unknown"
	} else {
		billable = &quantity
	}
	raw, err := common.Marshal(ztapiHostedToolUsage{SchemaVersion: 1, BillingKind: "hosted_tool", Outcome: result.Outcome,
		NonEmpty: result.NonEmpty, HTTPStatus: result.HTTPStatus, UpstreamRequestID: result.UpstreamRequestID,
		ResponseSHA256: result.ResponseSHA256, BillableCalls: billable, Unit: "call", SupplierCostState: costState})
	return string(raw), err
}
