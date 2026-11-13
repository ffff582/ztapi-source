package spending

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRequiredSourceColumnsReturnsIsolatedCompleteMetadata(t *testing.T) {
	first := RequiredSourceColumns()
	require.Len(t, first, 7)
	require.Contains(t, first["ztapi_request_settlements"], "charge_dimensions_json")
	require.Contains(t, first["ztapi_request_settlements"], "updated_at")
	require.Contains(t, first["balance_ledgers"], "created_at")
	require.Contains(t, first["ztapi_settlement_log_outboxes"], "created_at")
	for _, columns := range first {
		for _, forbidden := range []string{"key", "key_hash", "content", "other", "submission_json", "credential_version"} {
			require.NotContains(t, columns, forbidden)
		}
	}
	original := RequiredSourceColumns()
	first["users"][0] = "key"
	delete(first, "logs")
	first["injected"] = []string{"secret"}
	require.Equal(t, original, RequiredSourceColumns())
	r, _ := sourceFixture(t)
	seen := map[string][]string{}
	require.NoError(t, r.DB.Callback().Query().Before("gorm:query").Register("metadata_projection", func(db *gorm.DB) { seen[db.Statement.Table] = append([]string(nil), db.Statement.Selects...) }))
	require.NoError(t, r.Preflight(context.Background()))
	require.Len(t, seen, len(original))
	for table, columns := range original {
		require.Equal(t, columns, seen[table])
	}
}
