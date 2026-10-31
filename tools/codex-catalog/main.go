package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      7 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
}

func run(getenv func(string) string) error {
	cfg, err := readConfig(getenv)
	if err != nil {
		return err
	}
	entry, err := loadModelInfo(cfg.modelInfoPath)
	if err != nil {
		return err
	}
	handler := newCatalog(cfg, entry)
	defer handler.client.CloseIdleConnections()
	server := newHTTPServer(cfg.listenAddr, handler)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			_ = server.Close()
		}
		shutdownDone <- err
	}()
	err = server.ListenAndServe()
	stop()
	shutdownErr := <-shutdownDone
	if (err != nil && !errors.Is(err, http.ErrServerClosed)) || shutdownErr != nil {
		return errors.New("HTTP server failed or shutdown timed out")
	}
	return nil
}

func main() {
	if err := run(os.Getenv); err != nil {
		// All returned errors are fixed messages, never raw URL, file, or HTTP errors.
		log.Print("codex-catalog: ", err)
		os.Exit(1)
	}
}
