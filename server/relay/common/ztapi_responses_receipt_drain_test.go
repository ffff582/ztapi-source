package common

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestZTAPIResponsesReceiptTransportCancellation(t *testing.T) {
	for _, arm := range []bool{false, true} {
		client, disconnect := context.WithCancel(context.Background())
		drain := &ZTAPIResponsesReceiptDrain{}
		drain.SetArmed(arm)
		upstream, stop := drain.TransportContext(client)
		disconnect()
		remaining := drain.Remaining(client)
		drain.SetArmed(!arm)
		require.Equal(t, arm, drain.Armed())
		if arm {
			require.Positive(t, remaining)
			require.NoError(t, upstream.Err())
		} else {
			require.Zero(t, remaining)
		}
		select {
		case <-upstream.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("upstream transport not cancelled")
		}
		stop()
	}
}

func TestZTAPIResponsesReceiptDrainBoundsAndPreservesDeadline(t *testing.T) {
	client, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	drain := &ZTAPIResponsesReceiptDrain{}
	drain.SetArmed(true)
	input := drain.LimitReader(client, strings.NewReader(strings.Repeat("x", 2*ZTAPIResponsesReceiptBytes)))
	disconnect()
	buf, err := io.ReadAll(input)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, buf, ZTAPIResponsesReceiptBytes)

	client, stopClient := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopClient()
	upstream, stop := drain.TransportContext(client)
	defer stop()
	clientDeadline, _ := client.Deadline()
	upstreamDeadline, ok := upstream.Deadline()
	require.True(t, ok)
	require.Equal(t, clientDeadline, upstreamDeadline)
	<-upstream.Done()
	require.ErrorIs(t, upstream.Err(), context.DeadlineExceeded)
}
