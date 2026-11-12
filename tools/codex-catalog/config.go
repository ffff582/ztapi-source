package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const modelSlug = "zt-claude-opus-5"
const maxJSONBytes = 1 << 20

type config struct {
	upstream       *url.URL
	modelInfoPath  string
	listenAddr     string
	trustedProxies []netip.Prefix
}

func readConfig(getenv func(string) string) (config, error) {
	var cfg config
	raw := getenv("UPSTREAM_MODELS_URL")
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		u.EscapedPath() != "/v1/models" || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") ||
		strings.HasSuffix(u.Host, ":") {
		return cfg, errors.New("UPSTREAM_MODELS_URL must be a credential-free http(s) URL with exact path /v1/models and no query or fragment")
	}
	if strings.Contains(u.Hostname(), ":") {
		if _, err := netip.ParseAddr(u.Hostname()); err != nil {
			return cfg, errors.New("invalid upstream host")
		}
	}
	if port := u.Port(); port != "" && !validPort(port) {
		return cfg, errors.New("invalid upstream port")
	}
	cfg.upstream = u
	cfg.modelInfoPath = getenv("MODEL_INFO_PATH")
	if strings.TrimSpace(cfg.modelInfoPath) == "" {
		return cfg, errors.New("MODEL_INFO_PATH is required")
	}
	cfg.listenAddr = getenv("LISTEN_ADDR")
	if cfg.listenAddr == "" {
		cfg.listenAddr = ":8080"
	}
	_, port, err := net.SplitHostPort(cfg.listenAddr)
	if err != nil || !validPort(port) {
		return cfg, errors.New("LISTEN_ADDR must contain a host and a port in 1..65535")
	}
	if raw := getenv("TRUSTED_PROXY_CIDRS"); raw != "" {
		for _, item := range strings.Split(raw, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(item))
			if err != nil || prefix.Bits() != prefix.Addr().BitLen() || prefix.Addr().Is4In6() {
				return cfg, errors.New("TRUSTED_PROXY_CIDRS requires exact unmapped IPv4 /32 or IPv6 /128 peers")
			}
			cfg.trustedProxies = append(cfg.trustedProxies, prefix.Masked())
		}
	}
	return cfg, nil
}

func validPort(raw string) bool {
	port, err := strconv.Atoi(raw)
	return err == nil && port >= 1 && port <= 65535
}

func loadModelInfo(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open MODEL_INFO_PATH")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxJSONBytes+1))
	if err != nil || len(data) > maxJSONBytes || !utf8.Valid(data) {
		return nil, errors.New("cannot read bounded UTF-8 model metadata")
	}
	data = bytes.TrimSpace(data)
	var fields map[string]json.RawMessage
	var slug, instructions string
	if len(data) == 0 || data[0] != '{' || !uniqueJSON(data) || json.Unmarshal(data, &fields) != nil ||
		json.Unmarshal(fields["slug"], &slug) != nil || json.Unmarshal(fields["base_instructions"], &instructions) != nil ||
		slug != modelSlug || strings.TrimSpace(instructions) == "" {
		return nil, errors.New("MODEL_INFO_PATH must contain one ModelInfo object for zt-claude-opus-5 with supplied base_instructions")
	}
	return data, nil
}
