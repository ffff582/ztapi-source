// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
// Local synthetic acceptance fixture. Never deploy this command.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/finance"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/support"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const fixturePassword = "fixture-only-password"
const cookieName = "ztapi_support_fixture_session"

type fixtureOptions struct{ StateRoot, PublicOrigin, AdminOrigin string }
type fakeProvider struct{ calls, images, failures atomic.Int64 }
type fixtureUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Role     int    `json:"role"`
	Group    string `json:"group"`
}
type fakeSource struct {
	now   time.Time
	users map[int]fixtureUser
}
type fixture struct {
	now                   time.Time
	stateDir              string
	db, readDB, financeDB *gorm.DB
	provider              *fakeProvider
	source                *fakeSource
	support, finance      http.Handler
	origins               []string
	mu                    sync.Mutex
	sessions              map[string]int
	closeOnce             sync.Once
	closeErr              error
}

func localHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func validateListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !localHost(host) {
		return errors.New("fixture must bind an explicit loopback address")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("invalid fixture port")
	}
	return nil
}
func localOrigin(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && localHost(u.Hostname()) && u.User == nil && u.Port() != "" && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func randomString() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func openDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	raw, err := db.DB()
	if err != nil {
		return nil, err
	}
	raw.SetMaxOpenConns(1)
	return db, nil
}
func newFixture(options fixtureOptions) (_ *fixture, err error) {
	if !localOrigin(options.PublicOrigin) || !localOrigin(options.AdminOrigin) {
		return nil, errors.New("fixture origins must be explicit HTTP loopback origins")
	}
	if options.StateRoot == "" {
		options.StateRoot = os.TempDir()
	}
	if err := os.MkdirAll(options.StateRoot, 0700); err != nil {
		return nil, err
	}
	stateDir, err := os.MkdirTemp(options.StateRoot, "support-fixture-")
	if err != nil {
		return nil, err
	}
	f := &fixture{now: time.Now().UTC().Add(-2 * time.Minute), stateDir: stateDir, provider: &fakeProvider{}, origins: []string{options.PublicOrigin, options.AdminOrigin}, sessions: map[string]int{}}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	key, err := randomString()
	if err != nil {
		return nil, err
	}
	if err = service.ConfigureZTAPISessionSigningKey(key); err != nil {
		return nil, err
	}
	f.source = &fakeSource{now: f.now, users: map[int]fixtureUser{
		101: {101, "fixture-alice-1440", common.RoleCommonUser, "default"},
		111: {111, "fixture-alice-390", common.RoleCommonUser, "default"},
		121: {121, "fixture-alice-320", common.RoleCommonUser, "default"},
		131: {131, "fixture-recovery", common.RoleCommonUser, "default"},
		102: {102, "fixture-bob", common.RoleCommonUser, "default"},
		202: {202, "fixture-support", common.RoleSupportUser, "default"},
		203: {203, "fixture-finance", common.RoleFinanceUser, "default"},
		210: {210, "fixture-admin", common.RoleAdminUser, "default"},
		300: {300, "fixture-root", common.RoleRootUser, "default"},
	}}
	path := filepath.Join(stateDir, "private-support.sqlite")
	f.db, err = openDB(path)
	if err != nil {
		return nil, err
	}
	if err = f.db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		return nil, err
	}
	store, err := support.NewStore(f.db)
	if err != nil {
		return nil, err
	}
	filename := (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()
	f.readDB, err = openDB("file:" + filename + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)")
	if err != nil {
		return nil, err
	}
	f.financeDB, err = openDB(filepath.Join(stateDir, "private-finance.sqlite"))
	if err != nil {
		return nil, err
	}
	financeStore, err := finance.NewStore(f.financeDB)
	if err != nil {
		return nil, err
	}
	f.support = support.NewHandler(f.source, store, f.provider, support.HandlerConfig{Enabled: true, VisionEnabled: true, Model: "fixture-synthetic-flash", ChannelID: 1, Supplier: "fixture-synthetic-supplier", Environment: "fixture", Origins: f.origins, Now: func() time.Time { return f.now }})
	f.finance = finance.NewHandlerWithSupportReader(fakeFinanceSource{f.source}, financeStore, options.AdminOrigin, func() time.Time { return f.now.Add(time.Minute) }, f.readUsages)
	return f, nil
}
func (f *fixture) Close() error {
	f.closeOnce.Do(func() {
		for _, db := range []*gorm.DB{f.readDB, f.financeDB, f.db} {
			if db != nil {
				if raw, err := db.DB(); err == nil {
					f.closeErr = errors.Join(f.closeErr, raw.Close())
				}
			}
		}
	})
	return f.closeErr
}
func (f *fixture) readUsages(ctx context.Context, start, end time.Time) ([]support.FinancialUsage, error) {
	return support.ReadFinancialUsage(ctx, f.readDB, start, end)
}
func (s *fakeSource) Identity(_ context.Context, id int) (support.Identity, error) {
	user, ok := s.users[id]
	if !ok {
		return support.Identity{}, support.ErrNotFound
	}
	return support.Identity{ID: user.ID, Role: user.Role, Status: common.UserStatusEnabled}, nil
}
func (s *fakeSource) RecentRequests(ctx context.Context, id int) ([]support.Diagnosis, error) {
	d, err := s.Diagnose(ctx, id, fmt.Sprintf("fixture-request-%d", id))
	return []support.Diagnosis{d}, err
}
func (s *fakeSource) Diagnose(_ context.Context, id int, request string) (support.Diagnosis, error) {
	if _, exists := s.users[id]; !exists || request != fmt.Sprintf("fixture-request-%d", id) {
		return support.Diagnosis{}, support.ErrNotFound
	}
	return support.Diagnosis{RequestID: request, Model: "fixture-public-model", CreatedAt: s.now.Add(-time.Hour), Outcome: "upstream_unavailable", Code: "UPSTREAM_UNAVAILABLE", Summary: "本地合成请求证据：HTTP 503，未收到用量凭证；不代表已退款或零消耗。", HTTPStatus: 503, Stream: true, Dispatched: true, UsageReceived: false, ChargedU: "0.125", ReservedU: "0.200", NeedsHuman: true}, nil
}
func (s *fakeSource) Knowledge(_ context.Context, id int, _, _ string) (string, error) {
	if _, exists := s.users[id]; !exists {
		return "", support.ErrNotFound
	}
	return "本地零费用合成知识：涵盖公开业务文档、接入、模型目录、价格计量、账单与故障；不承诺固定充值优惠，不操作客户资金。仅用于隔离浏览器验收。", nil
}

type fakeFinanceSource struct{ source *fakeSource }

func (s fakeFinanceSource) Identity(ctx context.Context, id int) (finance.User, error) {
	user, err := s.source.Identity(ctx, id)
	return finance.User{ID: user.ID, Username: s.source.users[id].Username, Role: user.Role, Status: user.Status}, err
}
func (s fakeFinanceSource) Load(context.Context, time.Time) (finance.Snapshot, error) {
	users := []finance.User{}
	for _, user := range s.source.users {
		users = append(users, finance.User{ID: user.ID, Username: user.Username, Role: user.Role, Status: common.UserStatusEnabled})
	}
	return finance.Snapshot{SourceCommit: "synthetic-local-support-fixture", QuotaPerUnit: 500000, Users: users, Channels: []finance.Channel{{ID: 1, Name: "fixture-synthetic-supplier", BaseURL: "http://127.0.0.1"}}}, nil
}
func (p *fakeProvider) Complete(ctx context.Context, input support.ProviderInput) (support.ProviderResult, error) {
	if err := ctx.Err(); err != nil {
		return support.ProviderResult{}, err
	}
	n := p.calls.Add(1)
	if input.Image != nil {
		p.images.Add(1)
	}
	for _, message := range input.Messages {
		if text, ok := message.Content.(string); ok && strings.Contains(text, "FIXTURE_SYSTEM_TERMINAL") {
			p.failures.Add(1)
			return support.ProviderResult{Outcome: "upstream_error"}, errors.New("synthetic fixture provider failure")
		}
	}
	inputTokens, outputTokens, cache := int64(123), int64(45), int64(23)
	quote := "0.000123"
	return support.ProviderResult{Content: fmt.Sprintf("本地合成答复 #%d：已读取本人请求证据；如需进一步核实，请申请人工工单。\n<img src=x onerror=\"window.__supportXss=true\">", n), Outcome: "completed", InputTokens: &inputTokens, OutputTokens: &outputTokens, CacheReadTokens: &cache, QuoteCostU: &quote, QuoteEvidence: "SYNTHETIC_LOCAL_FIXTURE_NOT_AN_UPSTREAM_DEBIT", UpstreamRequestID: fmt.Sprintf("fixture-provider-%d", n)}, nil
}
func sendJSON(w http.ResponseWriter, status int, data any) {
	raw, err := common.Marshal(map[string]any{"success": status < 400, "data": data})
	if err != nil {
		http.Error(w, "fixture serialization failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func (f *fixture) session(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	originOK := false
	for _, origin := range f.origins {
		if r.Header.Get("Origin") == origin {
			originOK = true
		}
	}
	if !originOK {
		sendJSON(w, 403, nil)
		return
	}
	id := 0
	switch r.URL.Path {
	case "/api/auth/login":
		var credentials struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if common.DecodeJsonStrict(http.MaxBytesReader(w, r.Body, 4096), &credentials) != nil || credentials.Password != fixturePassword {
			sendJSON(w, 401, nil)
			return
		}
		for _, user := range f.source.users {
			if user.Username == credentials.Username {
				id = user.ID
			}
		}
		if id == 0 {
			sendJSON(w, 401, nil)
			return
		}
		secret, err := randomString()
		if err != nil {
			sendJSON(w, 500, nil)
			return
		}
		f.mu.Lock()
		f.sessions[secret] = id
		f.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: secret, Path: "/api/auth", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	case "/api/auth/refresh":
		cookie, err := r.Cookie(cookieName)
		if err == nil {
			f.mu.Lock()
			id = f.sessions[cookie.Value]
			f.mu.Unlock()
		}
		if id == 0 {
			sendJSON(w, 401, nil)
			return
		}
	case "/api/auth/logout":
		if cookie, err := r.Cookie(cookieName); err == nil {
			f.mu.Lock()
			delete(f.sessions, cookie.Value)
			f.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/api/auth", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		sendJSON(w, 200, map[string]bool{"logged_out": true})
		return
	default:
		http.NotFound(w, r)
		return
	}
	token, err := service.IssueZTAPIAccessToken(id, f.now)
	if err != nil {
		sendJSON(w, 500, nil)
		return
	}
	sendJSON(w, 200, map[string]any{"access_token": token, "expires_in": 900, "user": f.source.users[id]})
}
func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-ZTAPI-Fixture", "synthetic-zero-cost")
	w.Header().Set("X-ZTAPI-Paid-Calls", "0")
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if !localHost(host) || err != nil || !localHost(remote) {
		sendJSON(w, 403, nil)
		return
	}
	switch {
	case r.Method == "GET" && (r.URL.Path == "/healthz" || r.URL.Path == "/__fixture/status"):
		sendJSON(w, 200, map[string]any{"fixture": "synthetic-local-support", "paid_calls": 0, "provider_calls": f.provider.calls.Load(), "image_calls": f.provider.images.Load(), "provider_failures": f.provider.failures.Load(), "state_dir": f.stateDir, "fixture_now": f.now, "finance_month": f.now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01"), "users": f.source.users})
		return
	case r.URL.Path == "/api/status" && r.Method == "GET":
		sendJSON(w, 200, map[string]any{"system_name": "ZTAPI local synthetic fixture", "quota_per_unit": 500000, "password_reset_enabled": false})
		return
	case strings.HasPrefix(r.URL.Path, "/api/auth/"):
		f.session(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/api/support/") || strings.HasPrefix(r.URL.Path, "/api/admin/support/"):
		f.support.ServeHTTP(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/api/admin/finance-reporting/") && r.Method == "GET":
		f.finance.ServeHTTP(w, r)
		return
	default:
		http.NotFound(w, r)
	}
}
func run() error {
	address := flag.String("listen", "127.0.0.1:18087", "loopback address only")
	public := flag.String("public-origin", "http://127.0.0.1:4187", "local public UI origin")
	admin := flag.String("admin-origin", "http://127.0.0.1:4188", "local admin UI origin")
	state := flag.String("state-root", "", "parent of a newly-created private fixture state directory")
	flag.Parse()
	if err := validateListen(*address); err != nil {
		return err
	}
	f, err := newFixture(fixtureOptions{*state, *public, *admin})
	if err != nil {
		return err
	}
	defer f.Close()
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: f, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 15 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	ready, _ := common.Marshal(map[string]any{"ready": true, "listen": listener.Addr().String(), "state_dir": f.stateDir, "synthetic": true, "paid_calls": 0})
	fmt.Println(string(ready))
	if err = server.Serve(listener); errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "local synthetic support fixture:", err)
		os.Exit(1)
	}
}
