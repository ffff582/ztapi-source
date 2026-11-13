package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

type workerRunner interface{ Run(context.Context) error }

func runServices(ctx context.Context, server *http.Server, listener net.Listener, worker workerRunner) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverDone, workerDone := make(chan error, 1), make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	if worker != nil {
		go func() { workerDone <- worker.Run(ctx) }()
	}
	serverStopped, workerStopped := false, worker == nil
	var result error
	select {
	case <-ctx.Done():
	case err := <-serverDone:
		serverStopped = true
		if !errors.Is(err, http.ErrServerClosed) {
			result = errors.New("Telegram support health listener stopped")
		}
	case err := <-workerDone:
		workerStopped = true
		if ctx.Err() == nil || (err != nil && !errors.Is(err, context.Canceled)) {
			result = errors.New("Telegram support worker stopped")
		}
	}
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), 70*time.Second)
	defer done()
	if err := server.Shutdown(shutdown); err != nil {
		result = errors.New("Telegram support shutdown incomplete")
	}
	if !serverStopped {
		<-serverDone
	}
	if !workerStopped {
		select {
		case err := <-workerDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				result = errors.New("Telegram support worker stopped with incomplete persistence")
			}
		case <-shutdown.Done():
			return errors.New("Telegram support worker did not drain")
		}
	}
	return result
}
