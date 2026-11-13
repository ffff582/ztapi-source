package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtectedConfigAndStoreLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.json")
	raw := `{"endpoint":"https://www.xcloudtoken.com/v1/chat/completions","api_key":"test-only-secret-not-real","model":"deepseek-v4.1-flash","channel_id":8,"supplier":"xcloud","group":"国产分组","group_multiplier":"0.6","cash_per_credit":"0.5","input_credit_per_million":"0.14","output_credit_per_million":"0.5","cache_read_credit_per_million":"0.003","quote_evidence":"fixture-v1","vision_enabled":false}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
	cfg, err := readProviderConfig(path)
	require.NoError(t, err)
	require.Equal(t, "deepseek-v4.1-flash", cfg.Model)
	lockPath := filepath.Join(t.TempDir(), "runtime.lock")
	unlock, err := lockStore(lockPath)
	require.NoError(t, err)
	_, err = lockStore(lockPath)
	require.Error(t, err, "only one writer may recover calls in this private store")
	unlock()
	unlock, err = lockStore(lockPath)
	require.NoError(t, err)
	unlock()
}

func TestProviderConfigRejectsOversizeAndExtraFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"api_key":"secret-value","arbitrary_url":"http://evil.invalid"}`), 0600))
	_, err := readProviderConfig(path)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-value")
}

func TestStorageHeadroomHasNoDailyCounter(t *testing.T) {
	require.NoError(t, storageHeadroom(0, 2<<30))
	require.NoError(t, storageHeadroom(959<<20, 2<<30))
	require.Error(t, storageHeadroom(960<<20, 2<<30))
	require.Error(t, storageHeadroom(0, (1<<30)-1))
	path := filepath.Join(t.TempDir(), "support.db")
	require.NoError(t, os.WriteFile(path, []byte("fixture"), 0600))
	require.NoError(t, storageAdmission(path)())
	require.Error(t, storageAdmission(filepath.Join(t.TempDir(), "missing-parent", "db"))())
}
