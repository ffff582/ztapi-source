package support

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/support/bridgeapi"
	"github.com/stretchr/testify/require"
)

func bindingHarness(t *testing.T) (*TelegramBridge, *Store, *httpSource, *time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	s := testStore(t)
	source := &httpSource{role: 1, status: 1}
	b, err := NewTelegramBridge(s, source, TelegramBridgeConfig{Secret: []byte(strings.Repeat("s", 32)), BotUsername: "ztapi_bot", Now: func() time.Time { return now }})
	require.NoError(t, err)
	return b, s, source, &now
}

func challengeToken(t *testing.T, view BindingView) string {
	t.Helper()
	u, err := url.Parse(view.BindingURL)
	require.NoError(t, err)
	return strings.TrimPrefix(u.Query().Get("start"), "bind_")
}

func bindTestUser(t *testing.T, b *TelegramBridge, user int, tg int64, key string) BindingView {
	t.Helper()
	ctx := context.Background()
	v, e := b.UpdateBinding(ctx, user, BindingInput{Action: "create", IdempotencyKey: key})
	require.NoError(t, e)
	_, e = b.Execute(ctx, bridgeapi.Request{Action: "claim_binding", TelegramID: tg, Token: challengeToken(t, v), Username: "owner"})
	require.NoError(t, e)
	v, e = b.UpdateBinding(ctx, user, BindingInput{Action: "confirm", IdempotencyKey: key + "-confirm", ChallengeID: v.Pending.ID})
	require.NoError(t, e)
	return v
}

func TestTelegramBindingClaimDoesNotAuthorizeBeforeWebsiteConfirm(t *testing.T) {
	b, _, _, _ := bindingHarness(t)
	ctx := context.Background()
	v, e := b.UpdateBinding(ctx, 8, BindingInput{Action: "create", IdempotencyKey: "create-link-1"})
	require.NoError(t, e)
	require.False(t, v.Bound)
	require.NotEmpty(t, v.BindingURL)
	token := challengeToken(t, v)
	require.Len(t, token, 43)
	r, e := b.Execute(ctx, bridgeapi.Request{Action: "claim_binding", TelegramID: 42, Token: token, Username: "real_owner"})
	require.NoError(t, e)
	require.True(t, r.Pending)
	require.False(t, r.Bound)
	_, e = b.Execute(ctx, bridgeapi.Request{Action: "diagnose", TelegramID: 42, RequestID: "own-request"})
	require.ErrorIs(t, e, ErrNotFound)
	v, e = b.Binding(ctx, 8)
	require.NoError(t, e)
	require.True(t, v.Pending.Claimed)
	require.Empty(t, v.BindingURL)
	_, e = b.UpdateBinding(ctx, 9, BindingInput{Action: "confirm", IdempotencyKey: "foreign-confirm", ChallengeID: v.Pending.ID})
	require.ErrorIs(t, e, ErrNotFound)
	v, e = b.UpdateBinding(ctx, 8, BindingInput{Action: "confirm", IdempotencyKey: "owner-confirm", ChallengeID: v.Pending.ID})
	require.NoError(t, e)
	require.True(t, v.Bound)
	require.Equal(t, int64(42), v.TelegramID)
	r, e = b.Execute(ctx, bridgeapi.Request{Action: "diagnose", TelegramID: 42, RequestID: "own-request"})
	require.NoError(t, e)
	require.Equal(t, "own-request", r.Diagnosis.RequestID)
	_, e = b.Execute(ctx, bridgeapi.Request{Action: "diagnose", TelegramID: 42, RequestID: "someone-else"})
	require.ErrorIs(t, e, ErrNotFound)
}

func TestTelegramBindingExpiryReplayAndDisabledIdentity(t *testing.T) {
	b, _, source, now := bindingHarness(t)
	ctx := context.Background()
	v, e := b.UpdateBinding(ctx, 8, BindingInput{Action: "create", IdempotencyKey: "expires-create"})
	require.NoError(t, e)
	token := challengeToken(t, v)
	_, e = b.Execute(ctx, bridgeapi.Request{Action: "claim_binding", TelegramID: 42, Token: token})
	require.NoError(t, e)
	_, e = b.Execute(ctx, bridgeapi.Request{Action: "claim_binding", TelegramID: 43, Token: token})
	require.ErrorIs(t, e, ErrConflict)
	*now = now.Add(5 * time.Minute)
	_, e = b.UpdateBinding(ctx, 8, BindingInput{Action: "confirm", IdempotencyKey: "expired-confirm", ChallengeID: v.Pending.ID})
	require.ErrorIs(t, e, ErrConflict)
	bindTestUser(t, b, 8, 42, "second-create")
	source.status = 2
	_, e = b.Execute(ctx, bridgeapi.Request{Action: "recent_requests", TelegramID: 42})
	require.ErrorIs(t, e, ErrNotFound)
	source.status = 1
	_, e = b.UpdateBinding(ctx, 8, BindingInput{Action: "unbind", IdempotencyKey: "unbind-second"})
	require.NoError(t, e)
	_, e = b.Execute(ctx, bridgeapi.Request{Action: "diagnose", TelegramID: 42, RequestID: "own-request"})
	require.ErrorIs(t, e, ErrNotFound)
}

func TestTelegramTicketNotificationsAreGenericDurableAndRevocable(t *testing.T) {
	b, store, _, now := bindingHarness(t)
	ctx := context.Background()
	bindTestUser(t, b, 8, 42, "ticket-binding")
	r, e := b.Execute(ctx, bridgeapi.Request{Action: "create_ticket", TelegramID: 42, Content: "本人调用异常，密码 password: privateValueHere", RequestID: "own-request", IdempotencyKey: "tg-update-1024"})
	require.NoError(t, e)
	require.NotNil(t, r.Ticket)
	r2, e := b.Execute(ctx, bridgeapi.Request{Action: "create_ticket", TelegramID: 42, Content: "本人调用异常，密码 password: privateValueHere", RequestID: "own-request", IdempotencyKey: "tg-update-1024"})
	require.NoError(t, e)
	require.Equal(t, r.Ticket.ID, r2.Ticket.ID)
	*now = now.Add(time.Second)
	_, e = store.ReplyTicket(ctx, 1, r.Ticket.ID, "STAFF_PRIVATE_MESSAGE balance 200U", "staff-reply-notify", *now)
	require.NoError(t, e)
	events, e := b.Execute(ctx, bridgeapi.Request{Action: "poll_notifications"})
	require.NoError(t, e)
	require.Len(t, events.Notifications, 1)
	encoded, _ := json.Marshal(events)
	require.NotContains(t, string(encoded), "STAFF_PRIVATE_MESSAGE")
	require.NotContains(t, string(encoded), "200U")
	event := events.Notifications[0]
	require.Equal(t, int64(42), event.TelegramID)
	auth, e := b.Execute(ctx, bridgeapi.Request{Action: "authorize_notification", TelegramID: 42, EventID: event.ID})
	require.NoError(t, e)
	require.True(t, auth.Allowed)
	_, e = b.Execute(ctx, bridgeapi.Request{Action: "ack_notification", TelegramID: 42, EventID: event.ID})
	require.NoError(t, e)
	events, e = b.Execute(ctx, bridgeapi.Request{Action: "poll_notifications"})
	require.NoError(t, e)
	require.Empty(t, events.Notifications)
	*now = now.Add(time.Second)
	ticket, e := store.Ticket(ctx, r.Ticket.ID)
	require.NoError(t, e)
	_, e = store.ChangeTicketStatus(ctx, 1, ticket.ID, "resolved", ticket.Version, "staff-resolve-notify", *now)
	require.NoError(t, e)
	events, e = b.Execute(ctx, bridgeapi.Request{Action: "poll_notifications"})
	require.NoError(t, e)
	require.Len(t, events.Notifications, 1)
	_, e = b.UpdateBinding(ctx, 8, BindingInput{Action: "unbind", IdempotencyKey: "unlink-revoke-notify"})
	require.NoError(t, e)
	auth, e = b.Execute(ctx, bridgeapi.Request{Action: "authorize_notification", TelegramID: 42, EventID: events.Notifications[0].ID})
	require.NoError(t, e)
	require.False(t, auth.Allowed)
}

func TestTelegramBridgeSignedHTTPRejectsTamperReplayAndExtraIdentity(t *testing.T) {
	b, _, _, now := bindingHarness(t)
	body := `{"action":"binding_status","telegram_id":42}`
	call := func(content, nonce, signature string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", bridgeapi.Path, strings.NewReader(content))
		r.Header.Set("Content-Type", "application/json")
		ts := fmt.Sprint(now.Unix())
		r.Header.Set(bridgeapi.HeaderTimestamp, ts)
		r.Header.Set(bridgeapi.HeaderNonce, nonce)
		if signature == "" {
			signature = bridgeapi.Signature([]byte(strings.Repeat("s", 32)), ts, nonce, "POST", bridgeapi.Path, []byte(content))
		}
		r.Header.Set(bridgeapi.HeaderSignature, signature)
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	nonce := strings.Repeat("a", 32)
	require.Equal(t, http.StatusOK, call(body, nonce, "").Code)
	require.Equal(t, http.StatusUnauthorized, call(body, nonce, "").Code)
	require.Equal(t, http.StatusUnauthorized, call(body, strings.Repeat("b", 32), strings.Repeat("0", 64)).Code)
	require.Equal(t, http.StatusBadRequest, call(`{"action":"binding_status","telegram_id":42,"user_id":8}`, strings.Repeat("c", 32), "").Code)
	_, e := b.Execute(context.Background(), bridgeapi.Request{Action: "refund", TelegramID: 42})
	require.ErrorIs(t, e, ErrConflict)
}

func TestTelegramBindingWebsiteRequiresSessionOriginAndExplicitConfirmation(t *testing.T) {
	h, store, source, _, token := supportHTTP(t)
	b, err := NewTelegramBridge(store, source, TelegramBridgeConfig{Secret: []byte(strings.Repeat("s", 32)), BotUsername: "ztapi_bot", Now: h.(*handler).cfg.Now})
	require.NoError(t, err)
	h.(*handler).cfg.TelegramBridge = b
	path := "/api/support/telegram-binding"
	require.Equal(t, 401, requestSupport(h, "", 8, "GET", path, "", "").Code)
	require.Equal(t, 403, requestSupport(h, token, 8, "POST", path, `{"action":"create","idempotency_key":"web-bind-create"}`, "https://evil.example").Code)
	created := requestSupport(h, token, 8, "POST", path, `{"action":"create","idempotency_key":"web-bind-create"}`, "https://ztapi.example")
	require.Equal(t, 200, created.Code, created.Body.String())
	data := responseData(t, created)
	require.False(t, data["bound"].(bool))
	require.NotEmpty(t, data["binding_url"])
	require.Equal(t, 400, requestSupport(h, token, 8, "POST", path, `{"action":"confirm","idempotency_key":"web-bind-confirm","user_id":9}`, "https://ztapi.example").Code)
	read := requestSupport(h, token, 8, "GET", path, "", "")
	require.Equal(t, 200, read.Code)
	require.NotContains(t, read.Body.String(), "bind_")
	internal := requestSupport(h, token, 8, "POST", bridgeapi.Path, `{"action":"binding_status","telegram_id":42}`, "https://ztapi.example")
	require.Equal(t, 401, internal.Code, "website JWT must never substitute for bridge signature")
}

type selectiveTelegramSource struct {
	httpSource
	disabled map[int]bool
}

func (s *selectiveTelegramSource) Identity(ctx context.Context, id int) (Identity, error) {
	if s.disabled[id] {
		return Identity{ID: id, Status: 2}, nil
	}
	return s.httpSource.Identity(ctx, id)
}
func TestTelegramNotificationDisabledAccountsCannotStarveOtherCustomers(t *testing.T) {
	b, store, _, now := bindingHarness(t)
	ctx := context.Background()
	source := &selectiveTelegramSource{httpSource: httpSource{role: 1, status: 1}, disabled: map[int]bool{}}
	b.source = source
	for i := 1; i <= 21; i++ {
		if i <= 20 {
			source.disabled[i] = true
		}
		link := telegramBinding{UserID: i, TelegramID: int64(i + 100), Generation: fmt.Sprintf("generation-%d", i), BoundAt: now.Add(-time.Hour)}
		require.NoError(t, store.db.Create(&link).Error)
		ticket := Ticket{UserID: i, ConversationID: fmt.Sprintf("conversation-%d", i), Subject: "fixture", Content: "private", Status: "resolved", Version: 2, IdempotencyKey: fmt.Sprintf("fixture-ticket-%d", i), CreatedAt: now.Add(-time.Minute), UpdatedAt: *now}
		require.NoError(t, store.db.Create(&ticket).Error)
	}
	_, err := b.Execute(ctx, bridgeapi.Request{Action: "poll_notifications"})
	require.NoError(t, err)
	r, err := b.Execute(ctx, bridgeapi.Request{Action: "poll_notifications"})
	require.NoError(t, err)
	require.Len(t, r.Notifications, 1, "a disabled account batch cannot permanently occupy discovery")
	require.Equal(t, int64(121), r.Notifications[0].TelegramID)
}
