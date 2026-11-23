package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFXReleaseCanSkipOnlyImmediateStartupRefresh(t *testing.T) {
	for _, tc := range []struct {
		value string
		calls int
	}{{"", 1}, {"false", 1}, {"true", 0}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("ZTAPI_SKIP_STARTUP_FX_REFRESH", tc.value)
			calls := 0
			sentinel := errors.New("synthetic refresh failure")
			err := runZTAPIFXRefreshOnStart(context.Background(), func(context.Context) error { calls++; return sentinel })
			require.Equal(t, tc.calls, calls)
			if tc.calls > 0 {
				require.ErrorIs(t, err, sentinel)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
