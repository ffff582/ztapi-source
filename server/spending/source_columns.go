// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package spending

// RequiredSourceColumns is the canonical release/preflight SELECT allowlist.
// Every call returns a new map and new slices; there is no I/O or secret state.
func RequiredSourceColumns() map[string][]string {
	return map[string][]string{
		"users":                         {"id", "status", "deleted_at", "quota"},
		"ztapi_request_settlements":     {"id", "user_id", "operation_id", "request_id", "status", "public_model", "price_snapshot_json", "usage_json", "charge_dimensions_json", "missing_dimensions_json", "reserved_quota", "charged_quota", "refunded_quota", "updated_at"},
		"ztapi_settlement_log_outboxes": {"operation_id", "created_at", "log_id"},
		"balance_ledgers":               {"id", "user_id", "request_id", "source_type", "idempotency_key", "delta", "created_at"},
		"ztapi_supplier_refund_charges": {"id", "settlement_id", "user_id", "request_id", "billing_proof_id", "billing_operation_id", "original_ledger_id", "charged_quota", "refunded_quota", "price_snapshot_json"},
		"ztapi_supplier_refunds":        {"id", "charge_id", "request_id", "status", "ledger_id", "refunded_quota"},
		"logs":                          {"id", "user_id", "type", "created_at", "request_id", "quota"},
	}
}
