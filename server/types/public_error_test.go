package types

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestLocalErrorWrapperCannotMakeSupplierDiagnosticsPublic(t *testing.T) {
	internal := NewError(errors.New(`vendor failure: {"environment":{"UNRELATED_NAME":"opaque-secret"}}`), ErrorCodeBadResponse)
	public := internal.ToPublicOpenAIError()
	encoded, err := common.Marshal(public)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "opaque-secret")
	require.Contains(t, internal.Error(), "opaque-secret")
	require.Equal(t, string(ErrorCodeBadResponse), public.Code)
}

func TestPublicLocalValidationKeepsTrustedActionableHints(t *testing.T) {
	for _, tc := range []struct{ raw, hint string }{
		{"messages: orphan or duplicate tool result opaque-secret", "tool result"},
		{"tools: unsupported tool type opaque-secret", "tools:"},
		{"tool_choice: opaque-secret", "tool_choice:"},
		{"custom tool format: opaque-secret", "format:"},
		{"previous_response_id is not supported for this model; opaque-secret", "previous_response_id"},
	} {
		public := NewError(errors.New(tc.raw), ErrorCodeInvalidRequest).ToPublicOpenAIError()
		require.Contains(t, public.Message, tc.hint)
		require.NotContains(t, public.Message, "opaque-secret")
	}
}
