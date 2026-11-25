package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkbenchServiceModeIsExplicitAndScopeCannotMix(t *testing.T) {
	t.Setenv("NODE_TYPE", "slave")
	t.Setenv("ZTAPI_WORKBENCH_PILOT_LISTEN", "127.0.0.1:3000")
	t.Setenv("ZTAPI_WORKBENCH_PILOT_CONFIG", filepath.Join(t.TempDir(), "policy.json"))
	t.Setenv("ZTAPI_TRUSTED_PROXY_CIDRS", "127.0.0.1/32")
	t.Setenv("ZTAPI_WORKBENCH_SERVICE_MODE", "")
	t.Setenv("ZTAPI_WORKBENCH_PILOT_USER_ID", "199")
	t.Setenv("ZTAPI_WORKBENCH_PILOT_MODEL", "")

	config, err := settingsFromEnv()
	require.NoError(t, err)
	require.False(t, config.opusBeta)
	require.Equal(t, 199, config.userID)
	t.Setenv("ZTAPI_WORKBENCH_SERVICE_MODE", "opus-beta")
	_, err = settingsFromEnv()
	require.Error(t, err, "never inherit the paid QA account into the beta process")
	t.Setenv("ZTAPI_WORKBENCH_PILOT_USER_ID", "")
	t.Setenv("ZTAPI_WORKBENCH_PILOT_MODEL", "zt-claude-opus-5.5")
	_, err = settingsFromEnv()
	require.Error(t, err)
	t.Setenv("ZTAPI_WORKBENCH_PILOT_MODEL", "")
	config, err = settingsFromEnv()
	require.NoError(t, err)
	require.True(t, config.opusBeta)
	require.Zero(t, config.userID)
	t.Setenv("ZTAPI_WORKBENCH_SERVICE_MODE", "unknown")
	_, err = settingsFromEnv()
	require.Error(t, err)
}
