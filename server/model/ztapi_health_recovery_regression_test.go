package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIHealthRecoveredSuccessDoesNotRetripHistoricalFailures(t *testing.T) {
	s, c, _ := healthFixture(t)
	for i, result := range []string{"failure", "success", "failure", "success", "failure"} {
		healthRecord(t, s, healthAdmit(t, s, c, fmt.Sprint(i)), result)
	}
	state, err := s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.True(t, state.Open)
	require.NoError(t, s.ManualRecover(context.Background(), c.ID, state.Generation, 1, "verified fix with new functional evidence"))
	// Publication itself is tested by the publication gate suite; this fixture
	// isolates completion behavior after an authorized republish.
	require.NoError(t, s.DB.Model(&ZTAPIModelConfig{}).Where("id = ?", c.ID).Update("published", true).Error)
	healthRecord(t, s, healthAdmit(t, s, c, "post-recovery-success"), "success")
	state, err = s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.False(t, state.Open, "historical failures must not turn a new success into a new incident")
	window, err := s.Window(context.Background(), c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, window.Failures)
	require.EqualValues(t, 6, window.ValidSamples)
	healthRecord(t, s, healthAdmit(t, s, c, "post-recovery-failure"), "failure")
	state, err = s.GetState(context.Background(), c.ID)
	require.NoError(t, err)
	require.True(t, state.Open, "a genuinely new failure must still evaluate the preserved rolling window")
}
