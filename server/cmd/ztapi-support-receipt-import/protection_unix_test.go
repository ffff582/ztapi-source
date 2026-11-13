//go:build !windows

package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func protectTestManifest(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.Chmod(path, 0600))
}
func publicTestManifest(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.Chmod(path, 0644))
}
