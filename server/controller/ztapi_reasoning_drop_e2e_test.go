package controller

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"net/http"
	"testing"
)

func TestZTAPIReasoningResponsesDispatchesAndSettlesEveryStandardEffort(t *testing.T) {
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "hihg"} {
		t.Run(effort, func(t *testing.T) {
			f := newZTAPIResponsesEndpointFixture(t)
			f.queue(ztapiE2ESuccess("/v1/responses"))
			response := f.postJSON(t, "/v1/responses", map[string]any{"model": f.publicName, "input": "Say hello.", "max_output_tokens": 64, "reasoning": map[string]string{"effort": effort}})
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.EqualValues(t, 1, f.calls.Load())
			if effort == "hihg" {
				require.False(t, gjson.GetBytes(f.upstreamBodies[0], "reasoning").Exists())
			} else {
				require.Equal(t, effort, gjson.GetBytes(f.upstreamBodies[0], "reasoning.effort").String())
			}
			events := f.events(t)
			require.NotEmpty(t, events)
			for _, event := range events {
				require.NotEqual(t, "not_dispatched", event.Reason)
			}
			f.assertSettlement(t, 0, model.ZTAPISettlementSettled, []int{1})
			var logs []model.Log
			require.NoError(t, f.db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
			require.Len(t, logs, 1)
			var other map[string]any
			require.NoError(t, json.Unmarshal([]byte(logs[0].Other), &other))
			if effort == "hihg" {
				require.Equal(t, true, other["reasoning_effort_dropped"])
				require.Equal(t, "unrecognized_value", other["reasoning_effort_drop_reason"])
			} else {
				require.Equal(t, effort, other["reasoning_effort_received"])
				require.Equal(t, effort, other["reasoning_effort_forwarded"])
			}
		})
	}
}
