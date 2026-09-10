package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
)

func TestAdminOverviewDayExpressionUsesLogDialect(t *testing.T) {
	cases := []struct {
		logDialect string
		want       string
	}{
		{common.DatabaseTypeSQLite, "strftime('%Y-%m-%d', created_at, 'unixepoch')"},
		{common.DatabaseTypeMySQL, "DATE_FORMAT(FROM_UNIXTIME(created_at), '%Y-%m-%d')"},
		{common.DatabaseTypePostgreSQL, "TO_CHAR(TO_TIMESTAMP(created_at), 'YYYY-MM-DD')"},
	}
	for _, tc := range cases {
		t.Run(tc.logDialect, func(t *testing.T) {
			got, err := overviewDayExpression(tc.logDialect)
			if err != nil || got != tc.want {
				t.Fatalf("overviewDayExpression(%q) = %q, %v; want %q, nil", tc.logDialect, got, err, tc.want)
			}
		})
	}
	if _, err := overviewDayExpression("unknown"); err == nil {
		t.Fatal("unknown log dialect must fail safely")
	}
}
