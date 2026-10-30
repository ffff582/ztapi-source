package controller

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestZTAPICodexAdditionalToolsRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, custom := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream_%t_custom_%t", stream, custom), func(t *testing.T) {
				f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
				name, kind, outputKind, arguments := "read", "function", "function_call_output", `{}`
				if custom {
					name, kind, outputKind, arguments = "apply_patch", "custom", "custom_tool_call_output", `{"input":"*** Begin Patch\n*** End Patch"}`
				}
				sum := sha256.Sum256([]byte("functions\x00" + name))
				alias := fmt.Sprintf("ns_%x_%s", sum[:8], name)
				call := map[string]any{"id": "call_ns", "type": "function", "function": map[string]any{"name": alias, "arguments": arguments}}
				var first map[string]any
				if stream {
					call["index"] = 0
					first = map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": "tool_calls"}},
						"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28}}
				} else {
					first = map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "tool_calls": []any{call}}, "finish_reason": "tool_calls"}},
						"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28}}
				}
				encoded, err := common.Marshal(first)
				require.NoError(t, err)
				body := string(encoded)
				if stream {
					body = "data: " + body + "\n\ndata: [DONE]\n\n"
				}
				f.queue(ztapiE2EReply{status: 200, stream: stream, body: body})
				declarations := map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
					map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": kind, "name": name, "parameters": map[string]any{"type": "object"}}}},
				}}
				user := map[string]any{"role": "user", "content": "Use the tool."}
				request := map[string]any{"model": f.publicName, "store": false, "stream": stream, "max_output_tokens": 1024,
					"input": []any{declarations, user}, "tool_choice": map[string]any{"type": kind, "namespace": "functions", "name": name}}
				w := f.postJSON(t, "/v1/responses", request)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var result map[string]any
				if stream {
					for _, event := range parseZTAPISSE(t, w.Body.String()) {
						if event.name == "response.completed" {
							result = event.data["response"].(map[string]any)
						}
					}
				} else {
					require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
				}
				require.NotNil(t, result)
				output := result["output"].([]any)
				require.Len(t, output, 1)
				item := output[0].(map[string]any)
				require.Equal(t, name, item["name"])
				require.Equal(t, "functions", item["namespace"])
				require.Equal(t, "call_ns", item["call_id"])
				f.queue(ztapiE2EReply{status: 200, body: `{"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":2,"total_tokens":32}}`})
				followUp := map[string]any{"model": f.publicName, "stream": false, "max_output_tokens": 1024, "store": false,
					"input": []any{declarations, user, item, map[string]any{"type": outputKind, "call_id": "call_ns", "output": "Tool succeeded."}}}
				continued := f.postJSON(t, "/v1/responses", followUp)
				require.Equal(t, http.StatusOK, continued.Code, continued.Body.String())
				require.Contains(t, continued.Body.String(), "OK")
				f.mu.Lock()
				defer f.mu.Unlock()
				var sent map[string]any
				require.NoError(t, common.Unmarshal(f.upstreamBodies[1], &sent))
				messages := sent["messages"].([]any)
				historyCall := messages[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
				require.Equal(t, alias, historyCall["function"].(map[string]any)["name"])
				require.Equal(t, "call_ns", messages[2].(map[string]any)["tool_call_id"])
				require.EqualValues(t, 2, f.calls.Load())
			})
		}
	}
}

func TestZTAPICodexConfirmedUsageSettlesAfterTrailingStreamFailure(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	before := f.balances(t)
	client := service.GetHttpClient()
	originalTransport := client.Transport
	client.Transport = codexTrailingFailureTransport{base: originalTransport}
	t.Cleanup(func() { client.Transport = originalTransport })
	f.queue(ztapiE2EReply{status: 200, stream: true, body: `data: {"choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}` + "\n\n"})
	w := f.postJSON(t, "/v1/responses", map[string]any{"model": f.publicName, "input": "hi", "stream": true, "max_output_tokens": 1024})
	require.Contains(t, w.Body.String(), "response.failed")
	require.NotContains(t, w.Body.String(), "response.completed")
	settlement := assertCodexConfirmedUsageChargedOnce(t, f, before)
	assertCodexConfirmedUsageReplayUnchanged(t, f, before, settlement)
}

func assertCodexConfirmedUsageChargedOnce(t *testing.T, f *ztapiE2EFixture, before ztapiE2EBalances) model.ZTAPIRequestSettlement {
	t.Helper()
	// The fixture's frozen prices bill 20 input + 2 output tokens as 20 quota:
	// round((20 * 1.6666666667 + 2 * 3.3333333333) * 500000 / 1000000).
	const charged = 20
	after := f.balances(t)
	require.Equal(t, before.UserQuota-charged, after.UserQuota, "wallet must retain exactly confirmed usage, not the reservation or a second debit")
	require.Equal(t, before.TokenRemain-charged, after.TokenRemain)
	require.Equal(t, before.TokenUsed+charged, after.TokenUsed)
	require.Equal(t, before.Settlements+1, after.Settlements)
	require.Equal(t, before.Attempts+1, after.Attempts, "failed delivery must not redispatch")
	require.Equal(t, before.Calls+1, after.Calls)
	require.Equal(t, before.Logs+1, after.Logs)
	require.Equal(t, before.LedgerRows+2, after.LedgerRows, "one reservation and one actual-usage adjustment")
	var settlements []model.ZTAPIRequestSettlement
	require.NoError(t, f.db.Order("id").Find(&settlements).Error)
	require.Len(t, settlements, 1)
	row := settlements[0]
	require.Equal(t, model.ZTAPISettlementSettled, row.Status)
	require.True(t, row.Dispatched)
	require.EqualValues(t, charged, row.ChargedQuota)
	require.EqualValues(t, charged, row.TokenChargedQuota)
	require.Equal(t, row.InitialReservedQuota, row.ReservedQuota)
	require.Equal(t, row.ReservedQuota, row.TokenReservedQuota)
	require.Greater(t, row.ReservedQuota, row.ChargedQuota, "exercise returning the unused hold")
	require.Equal(t, 1, row.FinalAttempt)
	var usage struct {
		PromptTokens     int    `json:"prompt_tokens"`
		CompletionTokens int    `json:"completion_tokens"`
		TotalTokens      int    `json:"total_tokens"`
		UsageSource      string `json:"usage_source"`
	}
	require.NoError(t, common.UnmarshalJsonStr(row.UsageJSON, &usage))
	require.Equal(t, 20, usage.PromptTokens)
	require.Equal(t, 2, usage.CompletionTokens)
	require.Equal(t, 22, usage.TotalTokens)
	require.Equal(t, "upstream_reported", usage.UsageSource)
	var attempts []model.ZTAPIRequestAttempt
	require.NoError(t, f.db.Where("settlement_id = ?", row.ID).Find(&attempts).Error)
	require.Len(t, attempts, 1)
	require.Equal(t, 1, attempts[0].Attempt)
	require.Equal(t, 1, attempts[0].ChannelID)
	require.Equal(t, http.StatusOK, attempts[0].HTTPStatus)
	require.Equal(t, "local-upstream-1", attempts[0].UpstreamRequestID)
	var ledger []model.BalanceLedger
	require.NoError(t, f.db.Order("id").Find(&ledger).Error)
	require.Len(t, ledger, 2)
	require.Equal(t, model.BalanceLedgerSourceUsageReservation, ledger[0].SourceType)
	require.Equal(t, -row.ReservedQuota, ledger[0].Delta)
	require.Equal(t, model.BalanceLedgerSourceUsageSettlement, ledger[1].SourceType)
	require.Equal(t, row.ReservedQuota-charged, ledger[1].Delta)
	require.EqualValues(t, -charged, ledger[0].Delta+ledger[1].Delta)
	require.EqualValues(t, before.UserQuota, ledger[0].BalanceBefore)
	require.Equal(t, ledger[0].BalanceAfter, ledger[1].BalanceBefore)
	require.EqualValues(t, after.UserQuota, ledger[1].BalanceAfter)
	for _, entry := range ledger {
		require.Equal(t, row.RequestID, entry.RequestID)
		require.Equal(t, row.UserID, entry.UserID)
		require.NotEmpty(t, entry.IdempotencyKey)
	}
	require.NotEqual(t, ledger[0].IdempotencyKey, ledger[1].IdempotencyKey)
	require.Equal(t, ledger[1].ID, row.LastLedgerID)
	var logs []model.Log
	require.NoError(t, f.db.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 1)
	require.Equal(t, row.RequestID, logs[0].RequestId)
	require.Equal(t, charged, logs[0].Quota)
	require.Equal(t, 20, logs[0].PromptTokens)
	require.Equal(t, 2, logs[0].CompletionTokens)
	var user model.User
	var channel model.Channel
	require.NoError(t, f.db.First(&user, f.user.Id).Error)
	require.NoError(t, f.db.First(&channel, 1).Error)
	require.Equal(t, charged, user.UsedQuota)
	require.Equal(t, 1, user.RequestCount)
	require.EqualValues(t, charged, channel.UsedQuota)
	return row
}

func assertCodexConfirmedUsageReplayUnchanged(t *testing.T, f *ztapiE2EFixture, before ztapiE2EBalances, row model.ZTAPIRequestSettlement) {
	t.Helper()
	balances := f.balances(t)
	var ledger []model.BalanceLedger
	var attempts []model.ZTAPIRequestAttempt
	var logs []model.Log
	var outbox model.ZTAPISettlementLogOutbox
	var receipt model.ZTAPISettlementLogReceipt
	var intent model.ZTAPISettlementFinalizationIntent
	require.NoError(t, f.db.Order("id").Find(&ledger).Error)
	require.NoError(t, f.db.Order("id").Find(&attempts).Error)
	require.NoError(t, f.db.Where("type = ?", model.LogTypeConsume).Order("id").Find(&logs).Error)
	require.NoError(t, f.db.Where("operation_id = ?", row.OperationID).Take(&outbox).Error)
	require.NoError(t, f.db.Where("operation_id = ?", row.OperationID).Take(&receipt).Error)
	require.NoError(t, f.db.Where("operation_id = ?", row.OperationID).Take(&intent).Error)
	require.Positive(t, intent.CompletedAt)
	require.Positive(t, outbox.DeliveredAt)
	var evidence model.ZTAPISettlementEvidence
	require.NoError(t, common.UnmarshalJsonStr(intent.EvidenceJSON, &evidence))
	for replay := 0; replay < 2; replay++ {
		_, err := model.FinalizeZTAPIRequestSettlementWithEvidence(row.OperationID, row.ChargedQuota, row.UsageJSON, row.ChargeDimensionsJSON, evidence)
		require.NoError(t, err)
		require.NoError(t, model.RetryZTAPISettlementFinalizations(10))
		// Simulate a crash after the durable log receipt but before its outbox ACK.
		require.NoError(t, f.db.Model(&model.ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID).Update("delivered_at", 0).Error)
		delivered, err := model.ProcessPendingZTAPISettlementLogs(10)
		require.NoError(t, err)
		require.Equal(t, 1, delivered, "exercise real redelivery, not an already-ACKed no-op")
	}
	require.Equal(t, balances, f.balances(t), "replayed settlement and log delivery must not debit, reserve, dispatch or count again")
	var ledgerAfter []model.BalanceLedger
	var attemptsAfter []model.ZTAPIRequestAttempt
	var logsAfter []model.Log
	var outboxAfter model.ZTAPISettlementLogOutbox
	var receiptAfter model.ZTAPISettlementLogReceipt
	require.NoError(t, f.db.Order("id").Find(&ledgerAfter).Error)
	require.NoError(t, f.db.Order("id").Find(&attemptsAfter).Error)
	require.NoError(t, f.db.Where("type = ?", model.LogTypeConsume).Order("id").Find(&logsAfter).Error)
	require.NoError(t, f.db.Where("operation_id = ?", row.OperationID).Take(&outboxAfter).Error)
	require.NoError(t, f.db.Where("operation_id = ?", row.OperationID).Take(&receiptAfter).Error)
	require.Equal(t, ledger, ledgerAfter)
	require.Equal(t, attempts, attemptsAfter)
	require.Equal(t, logs, logsAfter)
	require.Equal(t, receipt, receiptAfter)
	require.Equal(t, outbox.PayloadJSON, outboxAfter.PayloadJSON)
	require.Equal(t, outbox.PayloadSHA256, outboxAfter.PayloadSHA256)
	require.Equal(t, outbox.LogID, outboxAfter.LogID)
	settled := assertCodexConfirmedUsageChargedOnce(t, f, before)
	require.Equal(t, row.OperationID, settled.OperationID)
	require.Equal(t, row.PriceSnapshotJSON, settled.PriceSnapshotJSON)
	require.Equal(t, row.UsageJSON, settled.UsageJSON)
	require.Equal(t, row.ChargeDimensionsJSON, settled.ChargeDimensionsJSON)
	for _, table := range []any{&model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPISupplierRefundCharge{}} {
		var count int64
		require.NoError(t, f.db.Model(table).Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
}

type codexTrailingFailureTransport struct{ base http.RoundTripper }

func (transport codexTrailingFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil {
		response.Body = codexTrailingFailureBody{ReadCloser: response.Body}
	}
	return response, err
}

type codexTrailingFailureBody struct{ io.ReadCloser }

func (body codexTrailingFailureBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
