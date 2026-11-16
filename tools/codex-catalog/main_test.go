package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPServerBoundsHeadersAndTimeoutsWithoutRequestLogging(t *testing.T) {
	srv := newHTTPServer(":8080", http.NotFoundHandler())
	if srv.Addr != ":8080" || srv.Handler == nil || srv.ReadHeaderTimeout <= 0 || srv.ReadHeaderTimeout > 3*time.Second ||
		srv.ReadTimeout <= 0 || srv.ReadTimeout > 5*time.Second ||
		srv.WriteTimeout < 5*time.Second || srv.WriteTimeout > 7*time.Second ||
		srv.IdleTimeout <= 0 || srv.IdleTimeout > 30*time.Second || srv.MaxHeaderBytes > 16<<10 || srv.MaxHeaderBytes <= 0 {
		t.Fatal("HTTP server does not bound inbound headers and slow connections")
	}
	if srv.ErrorLog == nil || srv.ErrorLog.Writer() != io.Discard {
		t.Fatal("server error logging could retain sensitive request information")
	}
}

func TestInvalidStartupFailsBeforeOpeningListener(t *testing.T) {
	if err := run(testEnv(map[string]string{"UPSTREAM_MODELS_URL": "http://user:SECRET@main-api/v1/models"})); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("invalid startup did not fail before binding or leaked credentials")
	}
	if err := run(testEnv(map[string]string{"MODEL_INFO_PATH": t.TempDir()})); err == nil {
		t.Fatal("unreadable metadata did not prevent startup")
	}
}

func TestProductionClientIgnoresEnvironmentProxiesAndBoundsTransport(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://user:SECRET@attacker.invalid:8080")
	t.Setenv("HTTPS_PROXY", "http://user:SECRET@attacker.invalid:8080")
	cfg, err := readConfig(testEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	h := newCatalog(cfg, []byte(modelFixture))
	transport, ok := h.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || !transport.DisableCompression || transport.MaxConnsPerHost != 32 ||
		transport.MaxResponseHeaderBytes > 16<<10 || transport.MaxResponseHeaderBytes <= 0 ||
		transport.ResponseHeaderTimeout <= 0 || transport.ResponseHeaderTimeout > 5*time.Second ||
		transport.TLSHandshakeTimeout <= 0 || transport.TLSHandshakeTimeout > 5*time.Second ||
		transport.DialContext == nil || h.client.Timeout <= 0 || h.client.Timeout > 5*time.Second {
		t.Fatal("production transport could leak credentials or exceed resource bounds")
	}
}
