package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type drainingWorker struct {
	started chan struct{}
	release chan struct{}
	stored  atomic.Bool
	err     error
}

func (w *drainingWorker) Run(ctx context.Context) error {
	close(w.started)
	<-ctx.Done()
	<-w.release
	w.stored.Store(true)
	return w.err
}

func TestTelegramLifecycleWaitsForWorkerReceiptOnShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := &drainingWorker{started: make(chan struct{}), release: make(chan struct{})}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })}
	done := make(chan error, 1)
	go func() { done <- runServices(ctx, server, listener, worker) }()
	<-worker.started
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Telegram runtime exited before worker receipt: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(worker.release)
	require.NoError(t, <-done)
	require.True(t, worker.stored.Load())
}

func TestDisabledTelegramRuntimeDoesNotRunWorker(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &http.Server{Handler: http.NotFoundHandler()}
	require.NoError(t, runServices(ctx, server, listener, nil))
}

func TestTelegramShutdownDoesNotHideReceiptPersistenceFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	worker := &drainingWorker{started: make(chan struct{}), release: make(chan struct{}), err: errors.New("fixture receipt persistence failed")}
	server := &http.Server{Handler: http.NotFoundHandler()}
	done := make(chan error, 1)
	go func() { done <- runServices(ctx, server, listener, worker) }()
	<-worker.started
	cancel()
	close(worker.release)
	require.Error(t, <-done)
}
