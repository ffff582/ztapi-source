package support

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

type httpSource struct{ role, status int }

func (s *httpSource) Identity(_ context.Context, id int) (Identity, error) {
	return Identity{ID: id, Role: s.role, Status: s.status}, nil
}
func (*httpSource) RecentRequests(context.Context, int) ([]Diagnosis, error) {
	return []Diagnosis{}, nil
}
func (*httpSource) Diagnose(_ context.Context, id int, key string) (Diagnosis, error) {
	if id != 8 || key != "own-request" {
		return Diagnosis{}, ErrNotFound
	}
	return Diagnosis{RequestID: key, Code: "client_cancelled", Summary: "调用已取消，预留不等于实扣。", NeedsHuman: true}, nil
}
func (*httpSource) Knowledge(context.Context, int, string, string) (string, error) {
	return "只根据平台已确认规则回答，不修改余额。", nil
}

type mockProvider struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	seen    []PromptMessage
}

func (p *mockProvider) Complete(ctx context.Context, in ProviderInput) (ProviderResult, error) {
	p.calls.Add(1)
	p.seen = append([]PromptMessage(nil), in.Messages...)
	if p.started != nil {
		close(p.started)
		<-p.release
		if ctx.Err() != nil {
			return ProviderResult{}, ctx.Err()
		}
	}
	input, output, cache, cost := int64(100), int64(4), int64(0), "0.0000048"
	return ProviderResult{Content: "先检查接入地址和模型名称；账单疑问可提交人工核查。", Outcome: "completed", InputTokens: &input, OutputTokens: &output, CacheReadTokens: &cache, QuoteCostU: &cost, QuoteEvidence: "fixture-quote"}, nil
}
func supportHTTP(t *testing.T) (http.Handler, *Store, *httpSource, *mockProvider, string) {
	t.Helper()
	require.NoError(t, service.ConfigureZTAPISessionSigningKey(strings.Repeat("support-test-signing-key", 3)))
	now := time.Now().UTC()
	token, err := service.IssueZTAPIAccessToken(8, now)
	require.NoError(t, err)
	source := &httpSource{role: 1, status: 1}
	store := testStore(t)
	provider := &mockProvider{}
	h := NewHandler(source, store, provider, HandlerConfig{Enabled: true, Model: "deepseek-v4.1-flash", ChannelID: 8, Supplier: "xcloud", Origins: []string{"https://ztapi.example", "https://admin.example"}, Now: func() time.Time { return now }})
	return h, store, source, provider, token
}
func requestSupport(h http.Handler, token string, user int, method, path, body, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("New-Api-User", fmt.Sprint(user))
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func responseData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var j struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &j))
	return j.Data
}

func TestSupportHTTPAuthenticationAndNoFinancialWritePaths(t *testing.T) {
	h, _, source, _, token := supportHTTP(t)
	require.Equal(t, 401, requestSupport(h, "", 8, "GET", "/api/support/config", "", "").Code)
	require.Equal(t, 401, requestSupport(h, token, 9, "GET", "/api/support/config", "", "").Code)
	source.status = 2
	require.Equal(t, 403, requestSupport(h, token, 8, "GET", "/api/support/config", "", "").Code)
	source.status = 1
	require.Equal(t, 404, requestSupport(h, token, 8, "POST", "/api/support/refund", `{}`, "https://ztapi.example").Code)
	require.Equal(t, 403, requestSupport(h, token, 8, "POST", "/api/support/conversations", `{"idempotency_key":"safe-key-123"}`, "https://evil.example").Code)
	require.Equal(t, 400, requestSupport(h, token, 8, "POST", "/api/support/conversations", `{"idempotency_key":"safe-key-123","user_id":9}`, "https://ztapi.example").Code)
}

func TestSupportStorageGuardKeepsReadsAndBlocksNewWrites(t *testing.T) {
	h, store, _, provider, token := supportHTTP(t)
	con, err := store.CreateConversation(context.Background(), 8, "guard-conversation", time.Now())
	require.NoError(t, err)
	h.(*handler).cfg.AdmitWrite = func() error { return fmt.Errorf("disk headroom low") }
	w := requestSupport(h, token, 8, "POST", "/api/support/conversations/"+con.ID+"/messages", `{"content":"help","idempotency_key":"guard-message"}`, "https://ztapi.example")
	require.Equal(t, 503, w.Code)
	require.Contains(t, w.Body.String(), "support_storage_unavailable")
	require.Equal(t, int32(0), provider.calls.Load())
	require.Equal(t, 200, requestSupport(h, token, 8, "GET", "/api/support/conversations/"+con.ID, "", "").Code)
}

func TestSupportHTTPChatPersistsAndNeverReplaysTheProvider(t *testing.T) {
	h, store, _, provider, token := supportHTTP(t)
	w := requestSupport(h, token, 8, "POST", "/api/support/conversations", `{"idempotency_key":"chat-conversation"}`, "https://ztapi.example")
	require.Equal(t, 201, w.Code)
	id := responseData(t, w)["id"].(string)
	body := `{"content":"我用Claude Code连接失败，Bearer private-client-key","idempotency_key":"chat-message-key"}`
	w = requestSupport(h, token, 8, "POST", "/api/support/conversations/"+id+"/messages", body, "https://ztapi.example")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "private-client-key")
	require.Equal(t, 200, requestSupport(h, token, 8, "POST", "/api/support/conversations/"+id+"/messages", body, "https://ztapi.example").Code)
	require.Equal(t, int32(1), provider.calls.Load())
	view, err := store.Conversation(context.Background(), 8, id)
	require.NoError(t, err)
	require.Len(t, view.Messages, 2)
	foreign, err := service.IssueZTAPIAccessToken(9, h.(*handler).cfg.Now())
	require.NoError(t, err)
	require.Equal(t, 404, requestSupport(h, foreign, 9, "GET", "/api/support/conversations/"+id, "", "").Code)
	require.Equal(t, 404, requestSupport(h, foreign, 9, "GET", "/api/support/diagnostics/own-request", "", "").Code)
}

func TestSupportJSONSecretsNeverReachStorageOrProvider(t *testing.T) {
	h, store, _, provider, token := supportHTTP(t)
	created := requestSupport(h, token, 8, "POST", "/api/support/conversations", `{"idempotency_key":"json-secret-conversation"}`, "https://ztapi.example")
	id := responseData(t, created)["id"].(string)
	body := `{"content":"日志 {\"password\":\"plainPassword123456\",\"api_key\":\"plainVendorCredential123456\"}","idempotency_key":"json-secret-message"}`
	w := requestSupport(h, token, 8, "POST", "/api/support/conversations/"+id+"/messages", body, "https://ztapi.example")
	require.Equal(t, 200, w.Code, w.Body.String())
	view, err := store.Conversation(context.Background(), 8, id)
	require.NoError(t, err)
	for _, message := range view.Messages {
		require.NotContains(t, message.Content, "plainPassword123456")
		require.NotContains(t, message.Content, "plainVendorCredential123456")
	}
	for _, message := range provider.seen {
		raw, err := common.Marshal(message.Content)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "plainPassword123456")
		require.NotContains(t, string(raw), "plainVendorCredential123456")
	}
}

func TestSupportClientDisconnectStillPersistsUsage(t *testing.T) {
	h, store, _, provider, token := supportHTTP(t)
	c, err := store.CreateConversation(context.Background(), 8, "disconnect-conversation", time.Now().UTC())
	require.NoError(t, err)
	provider.started = make(chan struct{})
	provider.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/api/support/conversations/"+c.ID+"/messages", strings.NewReader(`{"content":"help","idempotency_key":"disconnect-message"}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("New-Api-User", "8")
	r.Header.Set("Origin", "https://ztapi.example")
	r.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() { h.ServeHTTP(w, r); close(done) }()
	<-provider.started
	cancel()
	close(provider.release)
	<-done
	require.Equal(t, 200, w.Code, w.Body.String())
	rows, err := ReadFinancialUsage(context.Background(), store.db, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].QuoteCostU)
	require.Equal(t, "completed", rows[0].Outcome)
}

func TestSupportTicketStaffPermissionAndStrictWrites(t *testing.T) {
	h, store, source, _, token := supportHTTP(t)
	c, err := store.CreateConversation(context.Background(), 8, "staff-conversation", time.Now().UTC())
	require.NoError(t, err)
	body := fmt.Sprintf(`{"conversation_id":%q,"subject":"接入失败","content":"请核查","request_id":"own-request","idempotency_key":"staff-ticket-key"}`, c.ID)
	w := requestSupport(h, token, 8, "POST", "/api/support/tickets", body, "https://ztapi.example")
	require.Equal(t, 201, w.Code, w.Body.String())
	id := uint(responseData(t, w)["id"].(float64))
	source.role = common.RoleFinanceUser
	require.Equal(t, 403, requestSupport(h, token, 8, "GET", "/api/admin/support/tickets", "", "").Code)
	source.role = common.RoleSupportUser
	require.Equal(t, 200, requestSupport(h, token, 8, "GET", "/api/admin/support/tickets", "", "").Code)
	path := fmt.Sprintf("/api/admin/support/tickets/%d/replies", id)
	require.Equal(t, 400, requestSupport(h, token, 8, "POST", path, `{"content":"已收到","idempotency_key":"staff-reply-key","balance":100}`, "https://admin.example").Code)
	w = requestSupport(h, token, 8, "POST", path, `{"content":"已收到，正在核查。","idempotency_key":"staff-reply-key"}`, "https://admin.example")
	require.Equal(t, 201, w.Code, w.Body.String())
}

func TestSupportHumanFollowupAndPrivateAttachmentAuthorization(t *testing.T) {
	h, store, source, provider, token := supportHTTP(t)
	c, err := store.CreateConversation(context.Background(), 8, "human-http-conversation", time.Now().UTC())
	require.NoError(t, err)
	_, err = store.CreateTicket(context.Background(), 8, TicketInput{ConversationID: c.ID, Subject: "人工协助", Content: "请查看截图", IdempotencyKey: "human-http-ticket"}, time.Now().UTC())
	require.NoError(t, err)
	raw, err := common.Marshal(map[string]any{"content": "补充截图", "image": tinyImage(), "idempotency_key": "human-http-message"})
	require.NoError(t, err)
	w := requestSupport(h, token, 8, "POST", "/api/support/conversations/"+c.ID+"/messages", string(raw), "https://ztapi.example")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Zero(t, provider.calls.Load())
	data := responseData(t, w)
	message := data["message"].(map[string]any)
	id := uint(message["attachment_id"].(float64))
	path := fmt.Sprintf("/api/support/attachments/%d", id)
	require.Equal(t, 200, requestSupport(h, token, 8, "GET", path, "", "").Code)
	foreign, err := service.IssueZTAPIAccessToken(9, h.(*handler).cfg.Now())
	require.NoError(t, err)
	require.Equal(t, 404, requestSupport(h, foreign, 9, "GET", path, "", "").Code)
	source.role = common.RoleFinanceUser
	adminPath := fmt.Sprintf("/api/admin/support/attachments/%d", id)
	require.Equal(t, 403, requestSupport(h, token, 8, "GET", adminPath, "", "").Code)
	source.role = common.RoleSupportUser
	require.Equal(t, 200, requestSupport(h, token, 8, "GET", adminPath, "", "").Code)
}
