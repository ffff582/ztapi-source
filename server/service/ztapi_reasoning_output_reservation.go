package service

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
)

// ReserveZTAPIReasoningOutputBudget runs before dispatch, outside the billing
// attempt mutex. Retries top up the same immutable-ledger reservation.
func ReserveZTAPIReasoningOutputBudget(info *relaycommon.RelayInfo) error {
	if info == nil || info.ZTAPIReasoningOutputBudget <= 0 || info.IsChannelTest {
		return nil
	}
	if info.Billing == nil || info.ZTAPIPublicationSnapshot == nil {
		return types.NewErrorWithStatusCode(errors.New("Billing is temporarily unavailable. Please retry later."),
			types.ErrorCodeQueryDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	target, err := relaycommon.ZTAPIReasoningOutputReservationQuota(info.ZTAPIPublicationSnapshot,
		info.GetEstimatePromptTokens(), info.ZTAPIReasoningOutputBudget)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeModelPriceError, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if target <= info.Billing.GetPreConsumedQuota() {
		return nil
	}
	if err := info.Billing.Reserve(target); err != nil {
		if errors.Is(err, model.ErrInsufficientUserQuota) || errors.Is(err, model.ErrInsufficientTokenQuota) ||
			errors.Is(err, model.ErrBalanceLedgerNegativeBalance) {
			return types.NewErrorWithStatusCode(errors.New("Insufficient wallet or API key quota for the model output budget."),
				types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry())
		}
		return types.NewErrorWithStatusCode(errors.New("Billing is temporarily unavailable. Please retry later."),
			types.ErrorCodeQueryDataError, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	return nil
}
