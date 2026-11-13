package spending

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testSource struct {
	user     Identity
	snapshot Snapshot
}

func (s *testSource) Identity(context.Context, int) (Identity, error)        { return s.user, nil }
func (s *testSource) Load(context.Context, int, time.Time) (Snapshot, error) { return s.snapshot, nil }

type testBindings struct {
	mu       sync.Mutex
	binding  Binding
	err      error
	calls    int
	changeAt int
}

func (b *testBindings) Lookup(context.Context, int) (Binding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	value := b.binding
	if b.changeAt > 0 && b.calls >= b.changeAt {
		value.Generation = "changed"
	}
	return value, b.err
}

type testSender struct {
	calls  int
	result SendResult
}

func (s *testSender) Send(context.Context, int64) SendResult { s.calls++; return s.result }
func queuedEngine(t *testing.T) (*Engine, *testBindings, *testSender, time.Time) {
	s, err := NewStore(fixture(t))
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = s.SavePreferences(context.Background(), 1, Preferences{Enabled: true, Telegram: true, DailySpendU: "1"}, now)
	require.NoError(t, err)
	binding := Binding{TelegramID: 123, Generation: "generation"}
	require.NoError(t, s.Evaluate(context.Background(), 1, Snapshot{Overview: Overview{Complete: true, ConfirmedDailyU: str("2")}, Binding: &binding}, now))
	b := &testBindings{binding: binding}
	sender := &testSender{result: SendResult{Status: "unknown"}}
	return &Engine{Store: s, Source: &testSource{user: Identity{ID: 1, Status: 1}}, Bindings: b, Sender: sender}, b, sender, now
}
func TestTelegramGenerationDisabledUnknownAndBlocked(t *testing.T) {
	for _, kind := range []string{"generation", "disable", "account", "unknown", "blocked", "rate_limited"} {
		t.Run(kind, func(t *testing.T) {
			e, b, sender, now := queuedEngine(t)
			ctx := context.Background()
			switch kind {
			case "generation":
				b.changeAt = 2
			case "disable":
				p, err := e.Store.Preferences(ctx, 1)
				require.NoError(t, err)
				p.Enabled = false
				_, err = e.Store.SavePreferences(ctx, 1, p, now)
				require.NoError(t, err)
			case "account":
				e.Source.(*testSource).user.Status = 2
			case "blocked":
				sender.result.Status = "blocked"
			case "rate_limited":
				sender.result = SendResult{Status: "rate_limited", RetryAfter: time.Minute}
			}
			require.NoError(t, e.Deliver(ctx, now))
			require.NoError(t, e.Deliver(ctx, now.Add(time.Second)))
			expected := 1
			if kind == "generation" || kind == "disable" || kind == "account" {
				expected = 0
			}
			require.Equal(t, expected, sender.calls)
			if kind == "rate_limited" {
				b.binding.Generation = "new"
				require.NoError(t, e.Deliver(ctx, now.Add(2*time.Minute)))
				require.Equal(t, 1, sender.calls)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTelegramOnlyGenericSendMessageAndNoRedirect(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "https", r.URL.Scheme)
		require.Equal(t, "api.telegram.org", r.URL.Host)
		require.True(t, strings.HasSuffix(r.URL.Path, "/sendMessage"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), "daily_spend")
		require.NotContains(t, string(body), "request_id")
		require.Contains(t, string(body), "https://ztapi.vip/console/spending")
		require.Contains(t, string(body), "消费预警")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Header: make(http.Header)}, nil
	})}
	sender, err := NewTelegramSender("123456:"+strings.Repeat("a", 32), client)
	require.NoError(t, err)
	require.Equal(t, "sent", sender.Send(context.Background(), 123).Status)
	require.Equal(t, 1, calls)
	require.Equal(t, "failed", sender.Send(context.Background(), -123).Status)
	require.Equal(t, 1, calls)
}
func TestBindingStoreReadonlyMissingDoesNotCreate(t *testing.T) {
	_, err := OpenBindingStore(t.TempDir() + "/absent.db")
	require.Error(t, err)
	db := fixture(t)
	require.NoError(t, db.Exec(`CREATE TABLE support_telegram_bindings(user_id INTEGER PRIMARY KEY,telegram_id BIGINT,generation TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO support_telegram_bindings VALUES(1,123,'gen')`).Error)
	b := &BindingReader{DB: db}
	got, err := b.Lookup(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "gen", got.Generation)
	_, err = b.Lookup(context.Background(), 2)
	require.Error(t, err)
}

func TestBindingReadonlyLiveGenerationAndNoMigration(t *testing.T) {
	db := fixture(t)
	require.NoError(t, db.Exec(`CREATE TABLE support_telegram_bindings(user_id INTEGER PRIMARY KEY,telegram_id BIGINT,generation TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO support_telegram_bindings VALUES(1,123,'first')`).Error)
	var files []struct{ File string }
	require.NoError(t, db.Raw(`PRAGMA database_list`).Scan(&files).Error)
	require.NotEmpty(t, files)
	path, err := filepath.Abs(files[0].File)
	require.NoError(t, err)
	reader, err := OpenBindingStore(path)
	require.NoError(t, err)
	defer reader.Close()
	binding, err := reader.Lookup(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "first", binding.Generation)
	require.Error(t, reader.DB.Exec(`INSERT INTO support_telegram_bindings VALUES(2,456,'forbidden')`).Error)
	require.NoError(t, db.Exec(`UPDATE support_telegram_bindings SET generation='second' WHERE user_id=1`).Error)
	binding, err = reader.Lookup(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "second", binding.Generation)
	require.False(t, reader.DB.Migrator().HasTable("spending_preferences"))
}
