package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func workbenchNginxFixture(t *testing.T, backend *httptest.Server, locations string) string {
	t.Helper()
	host, port, err := net.SplitHostPort(backend.Listener.Addr().String())
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)
	locations, err = filepath.Abs(locations)
	require.NoError(t, err)
	dir := t.TempDir()
	rules, err := os.ReadFile(locations)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workbench.locations.conf"), rules, 0600))
	docker := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	const nginxImage = "nginx:1.29-alpine"
	addresses, err := docker("run", "--pull", "never", "--rm", "--entrypoint", "getent", nginxImage, "ahostsv4", "host.docker.internal")
	require.NoError(t, err, "%s", addresses)
	fields := strings.Fields(addresses)
	require.NotEmpty(t, fields)
	require.NotNil(t, net.ParseIP(fields[0]).To4())
	config := fmt.Sprintf(`
upstream ztapi_workbench_pilot_backend {
    server %s;
}
server {
    listen 8080;
    server_name localhost;
    client_max_body_size 32m;
    add_header X-Content-Type-Options nosniff always;
    include /qa/workbench.locations.conf;
    location / {
        proxy_pass http://ztapi_workbench_pilot_backend;
        proxy_next_upstream off;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $remote_addr;
    }
}
`, net.JoinHostPort(fields[0], port))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "default.conf"), []byte(config), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nginx.conf"), []byte("events {}\nhttp { access_log off; include /qa/default.conf; }\n"), 0600))
	name := "ztapi-wb-nginx-" + uuid.NewString()
	out, err := docker("run", "--pull", "never", "--rm", "-d", "--name", name,
		"-p", "127.0.0.1::8080",
		"--mount", "type=bind,src="+dir+",dst=/qa,readonly",
		nginxImage, "nginx", "-g", "daemon off;", "-c", "/qa/nginx.conf")
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := docker("logs", "--tail", "12", name)
			t.Logf("local Nginx diagnostics: %s", logs)
		}
		_, _ = docker("stop", name)
	})
	require.NoError(t, err, "%s", out)
	address, err := docker("port", name, "8080/tcp")
	require.NoError(t, err, "%s", address)
	h, _, err := net.SplitHostPort(address)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", h)
	origin := "http://" + address
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	last := ""
	ready := func() bool {
		resp, err := client.Get(origin + "/.well-known/ztapi-workbench")
		if err != nil {
			last = err.Error()
			return false
		}
		_ = resp.Body.Close()
		last = resp.Status
		return resp.StatusCode == http.StatusOK && resp.Header.Get("Cache-Control") == "no-store"
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return origin
		}
		time.Sleep(250 * time.Millisecond)
	}
	logs, _ := docker("logs", "--tail", "10", name)
	t.Fatalf("local Nginx did not reach the Workbench fixture: %s: %s", last, logs)
	return ""
}
