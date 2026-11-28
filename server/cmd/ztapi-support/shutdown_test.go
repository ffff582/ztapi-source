package main

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShutdownWaitsForDispatchedHandlerReceipt(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var receipt atomic.Bool
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		receipt.Store(true)
		w.WriteHeader(200)
	})}
	go func() { done <- serveSupport(ctx, server, listener) }()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		r, e := http.Get("http://" + listener.Addr().String())
		if e == nil {
			r.Body.Close()
		}
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		t.Fatalf("server exited before receipt: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	<-clientDone
	require.True(t, receipt.Load())
}
