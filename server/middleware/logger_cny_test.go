package middleware

import "testing"

func TestCNYNotifyDoesNotLogQuery(t *testing.T) {
	for _, base := range []string{"/api/payment/cny/notify", "/api/payment/cny/notify/"} {
		if got := sanitizeLogPath(base + "?out_trade_no=test&sign=fixture&money=1.00"); got != base {
			t.Errorf("callback query leaked: %q", got)
		}
	}
}
