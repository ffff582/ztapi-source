package model

// ZTAPIModelProbeReceipt20261128 is transient metadata for one dispatched text
// probe, not a supplier debit or a database row. Missing usage fields stay unknown.
type ZTAPIModelProbeReceipt20261128 struct {
	Mode       string `json:"mode"`
	StatusCode int    `json:"status_code"`
	RequestID  string `json:"request_id,omitempty"`
	UsageJSON  string `json:"usage_json,omitempty"`
	// Complete requires accepted terminal/usage evidence, a safe request ID, and
	// recognized authority metadata when present. It does not establish cache
	// TTLs, quoted costs, or actual supplier charges.
	Complete bool `json:"complete"`
}
