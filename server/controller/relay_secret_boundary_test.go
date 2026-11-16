package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
)

func TestPublicRelayErrorDoesNotExposeVendorSecretsOrMutateEvidence(t *testing.T) {
	const marker = "AUDIT_SENTINEL_NOT_A_REAL_KEY"
	for _, status := range []int{400, 429, 500} {
		original := types.WithOpenAIError(types.OpenAIError{
			Message: "api_key=" + marker,
			Type:    "upstream_error", Code: "vendor_code_" + marker, Param: marker,
			Metadata: []byte(`{"environment":{"UNRELATED_NAME":"` + marker + `"}}`),
		}, status)
		before, err := common.Marshal(original.RelayError)
		if err != nil {
			t.Fatal(err)
		}
		public := relayClientError(original, types.RelayFormatOpenAI)
		public.SetMessage("customer-facing message")
		if original.Error() == "customer-facing message" {
			t.Fatal("public conversion mutated original evidence")
		}
		wire, err := common.Marshal(public.ToOpenAIError())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), marker) {
			t.Fatal("vendor secret survived the public boundary")
		}
		after, err := common.Marshal(original.RelayError)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("provider evidence changed")
		}
		if original.StatusCode != status {
			t.Fatal("provider status changed")
		}
	}
}

func TestPublicRelayContextFailureRemainsActionable(t *testing.T) {
	original := types.WithOpenAIError(types.OpenAIError{Message: "private upstream environment details", Type: "invalid_request_error", Code: "context_length_exceeded"}, http.StatusBadRequest)
	public := relayClientError(original, types.RelayFormatOpenAI).ToOpenAIError()
	if public.Code != "context_length_exceeded" {
		t.Fatal("context category lost")
	}
	if strings.Contains(public.Message, "private upstream") {
		t.Fatal("raw provider details escaped")
	}
}
