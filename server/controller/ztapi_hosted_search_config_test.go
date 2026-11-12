package controller

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostedSearchConfigurationDisabledByDefault(t *testing.T) {
	t.Setenv("ZTAPI_HOSTED_SEARCH_ENABLED", "")
	options, enabled, err := HostedResponsesOptionsFromEnvironment()
	require.NoError(t, err)
	require.False(t, enabled)
	require.Nil(t, options.Provider)
}

func TestHostedSearchConfigurationRequiresDedicatedProtectedSource(t *testing.T) {
	t.Setenv("ZTAPI_HOSTED_SEARCH_ENABLED", "1")
	t.Setenv("ZTAPI_HOSTED_SEARCH_CHANNEL_ID", "")
	t.Setenv("ZTAPI_HOSTED_SEARCH_KEY_FILE", "")
	_, enabled, err := HostedResponsesOptionsFromEnvironment()
	require.True(t, enabled)
	require.Error(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "official-key.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"api_key":"sk-offline-test-only-key-not-real","allowed_endpoints":["https://api.deepseek.com/v1/tools/search","https://api.deepseek.com/v1/tools/fetch"]}`), 0600))
	t.Setenv("ZTAPI_HOSTED_SEARCH_CHANNEL_ID", "99")
	t.Setenv("ZTAPI_HOSTED_SEARCH_KEY_FILE", path)
	options, enabled, err := HostedResponsesOptionsFromEnvironment()
	require.NoError(t, err)
	require.True(t, enabled)
	require.NotNil(t, options.Provider)
	require.NotNil(t, options.Dispatch)
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(path, 0644))
		_, _, err = HostedResponsesOptionsFromEnvironment()
		require.Error(t, err)
	}
}

func TestHostedSearchConfigurationRejectsRedirectedOrAmbiguousCredentials(t *testing.T) {
	t.Setenv("ZTAPI_HOSTED_SEARCH_ENABLED", "1")
	t.Setenv("ZTAPI_HOSTED_SEARCH_CHANNEL_ID", "99")
	for _, raw := range []string{
		`{"api_key":"sk-one","api_key":"sk-two","allowed_endpoints":["https://api.deepseek.com/v1/tools/search","https://api.deepseek.com/v1/tools/fetch"]}`,
		`{"api_key":"sk-offline-test-only","allowed_endpoints":["https://attacker.example/search"]}`,
		`{"api_key":"sk-offline-test-only"}`,
	} {
		path := filepath.Join(t.TempDir(), "key.json")
		require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
		t.Setenv("ZTAPI_HOSTED_SEARCH_KEY_FILE", path)
		_, _, err := HostedResponsesOptionsFromEnvironment()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "sk-")
	}
}
