package common

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningLocalOverrideOriginControlsPublicMessages(t *testing.T) {
	const secret = "PRIVATE_REASONING_OVERRIDE_DETAIL"
	for _, tc := range []struct {
		name      string
		code      string
		status    int
		skipRetry bool
	}{
		{"reasoning", "invalid_reasoning_effort", http.StatusBadRequest, true},
		{"shared_invalid_request", "invalid_request", http.StatusBadRequest, false},
		{"shared_rate_limit", "rate_limit_error", http.StatusTooManyRequests, true},
		{"custom_client_error", "custom_reasoning_override", http.StatusUnprocessableEntity, false},
		{"custom_server_error", "custom_reasoning_override", http.StatusInternalServerError, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := types.OpenAIError{Message: secret, Type: "custom_reasoning_error", Code: tc.code}
			t.Run("local_constructor_chain", func(t *testing.T) {
				_, err := ApplyParamOverride([]byte(`{"reasoning_effort":"high"}`), map[string]interface{}{
					"operations": []interface{}{
						map[string]interface{}{
							"mode": "return_error",
							"value": map[string]interface{}{
								"message": secret, "type": "custom_reasoning_error", "code": tc.code,
								"status_code": tc.status, "skip_retry": tc.skipRetry,
							},
						},
					},
				}, nil)
				require.Error(t, err)
				overrideErr, ok := AsParamOverrideReturnError(err)
				require.True(t, ok)
				local := NewAPIErrorFromParamOverride(overrideErr)
				require.Equal(t, envelope, local.ToOpenAIError())
				require.Equal(t, types.ErrorTypeOpenAIError, local.GetErrorType())
				require.Equal(t, types.ErrorCode(tc.code), local.GetErrorCode())
				require.Equal(t, tc.status, local.StatusCode)
				require.Equal(t, tc.skipRetry, types.IsSkipRetryError(local))
				for _, message := range []string{local.ToPublicOpenAIError().Message, local.ToPublicClaudeError().Message} {
					require.NotContains(t, strings.ToLower(message), "upstream")
					require.Contains(t, message, "locally")
					require.NotContains(t, message, secret)
				}
				require.Equal(t, envelope, local.ToOpenAIError())
			})

			t.Run("same_code_upstream", func(t *testing.T) {
				var opts []types.NewAPIErrorOptions
				if tc.skipRetry {
					opts = append(opts, types.ErrOptionWithSkipRetry())
				}
				upstream := types.WithOpenAIError(envelope, tc.status, opts...)
				for _, message := range []string{upstream.ToPublicOpenAIError().Message, upstream.ToPublicClaudeError().Message} {
					require.Contains(t, strings.ToLower(message), "upstream")
					require.NotContains(t, message, secret)
				}
				require.Equal(t, envelope, upstream.ToOpenAIError())
				require.Equal(t, types.ErrorTypeOpenAIError, upstream.GetErrorType())
				require.Equal(t, types.ErrorCode(tc.code), upstream.GetErrorCode())
				require.Equal(t, tc.status, upstream.StatusCode)
				require.Equal(t, tc.skipRetry, types.IsSkipRetryError(upstream))
			})
		})
	}
}

func TestZTAPIReasoningLocalOverridePreservesCustomEnvelopeWithoutExposingSecrets(t *testing.T) {
	local := NewAPIErrorFromParamOverride(&ParamOverrideReturnError{
		Message:    "PRIVATE_REASONING_OVERRIDE_MESSAGE",
		Type:       "PRIVATE_REASONING_OVERRIDE_TYPE",
		Code:       "PRIVATE_REASONING_OVERRIDE_CODE",
		StatusCode: http.StatusUnprocessableEntity,
		SkipRetry:  true,
	})
	encoded, err := common.Marshal(local.ToOpenAIError())
	require.NoError(t, err)
	require.JSONEq(t, `{"message":"PRIVATE_REASONING_OVERRIDE_MESSAGE","type":"PRIVATE_REASONING_OVERRIDE_TYPE","param":"","code":"PRIVATE_REASONING_OVERRIDE_CODE"}`, string(encoded))
	require.Contains(t, local.Error(), "PRIVATE_REASONING_OVERRIDE_MESSAGE")
	for _, public := range []interface{}{local.ToPublicOpenAIError(), local.ToPublicClaudeError()} {
		encoded, err := common.Marshal(public)
		require.NoError(t, err)
		for _, secret := range []string{"PRIVATE_REASONING_OVERRIDE_MESSAGE", "PRIVATE_REASONING_OVERRIDE_TYPE", "PRIVATE_REASONING_OVERRIDE_CODE"} {
			require.NotContains(t, string(encoded), secret)
		}
	}
}

func TestZTAPIReasoningLocalOverrideDefaultsRemainStable(t *testing.T) {
	local := NewAPIErrorFromParamOverride(&ParamOverrideReturnError{})
	require.Equal(t, http.StatusBadRequest, local.StatusCode)
	require.Equal(t, types.ErrorCodeInvalidRequest, local.GetErrorCode())
	require.Equal(t, types.ErrorTypeOpenAIError, local.GetErrorType())
	require.False(t, types.IsSkipRetryError(local))
	require.Equal(t, types.OpenAIError{Message: "request blocked by param override", Type: "invalid_request_error", Code: "invalid_request"}, local.ToOpenAIError())
	for _, message := range []string{local.ToPublicOpenAIError().Message, local.ToPublicClaudeError().Message} {
		require.NotContains(t, strings.ToLower(message), "upstream")
	}

	nilOverride := NewAPIErrorFromParamOverride(nil)
	require.Equal(t, http.StatusInternalServerError, nilOverride.StatusCode)
	require.Equal(t, types.ErrorCodeChannelParamOverrideInvalid, nilOverride.GetErrorCode())
	require.Equal(t, types.ErrorTypeNewAPIError, nilOverride.GetErrorType())
	require.True(t, types.IsSkipRetryError(nilOverride))
	for _, message := range []string{nilOverride.ToPublicOpenAIError().Message, nilOverride.ToPublicClaudeError().Message} {
		require.NotContains(t, strings.ToLower(message), "upstream")
	}
}
