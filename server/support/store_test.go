package support

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/support.db"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	s, err := NewStore(db)
	require.NoError(t, err)
	return s
}

func TestStoreOwnScopeAndConversationIdempotency(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "same-key-123", now)
	require.NoError(t, err)
	again, err := s.CreateConversation(ctx, 8, "same-key-123", now)
	require.NoError(t, err)
	require.Equal(t, c.ID, again.ID)
	_, err = s.Conversation(ctx, 9, c.ID)
	require.ErrorIs(t, err, ErrNotFound)
	rows, err := s.Conversations(ctx, 9)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestStoreOneDispatchAndImmutableReceipt(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "conversation-one", now)
	require.NoError(t, err)
	in := BeginInput{UserID: 8, ConversationID: c.ID, IdempotencyKey: "message-one-key", Content: "Bearer private-token-123 sk-real-secret-value", Model: "flash", ChannelID: 4, Supplier: "xcloud", Now: now}
	job, err := s.BeginCall(ctx, in)
	require.NoError(t, err)
	require.NotContains(t, job.Message.Content, "private-token-123")
	require.NotContains(t, job.Message.Content, "sk-real-secret-value")
	_, err = s.BeginCall(ctx, in)
	require.ErrorIs(t, err, ErrBusy)
	require.NoError(t, s.MarkDispatched(ctx, job.CallID))
	input, output, cache, cost := int64(100), int64(5), int64(20), "0.00001"
	result, err := s.FinishCall(ctx, job.CallID, ProviderResult{Content: "可以，先检查接入地址。", Outcome: "completed", InputTokens: &input, OutputTokens: &output, CacheReadTokens: &cache, QuoteCostU: &cost, QuoteEvidence: "quote-v1"}, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, "assistant", result.Reply.Role)
	replayed, err := s.BeginCall(ctx, in)
	require.NoError(t, err)
	require.NotNil(t, replayed.Reply)
	require.Equal(t, result.Reply.ID, replayed.Reply.ID)
	in.Content = "different payload"
	_, err = s.BeginCall(ctx, in)
	require.ErrorIs(t, err, ErrConflict)
	usages, err := ReadFinancialUsage(ctx, s.db, now.Add(-time.Minute), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, usages, 1)
	require.Equal(t, "customer_service", usages[0].Purpose)
	require.Equal(t, input, *usages[0].InputTokens)
	require.Equal(t, cost, *usages[0].QuoteCostU)
	require.Nil(t, usages[0].ActualCostU)
	beijing := time.FixedZone("Asia/Shanghai", 8*3600)
	usages, err = ReadFinancialUsage(ctx, s.db, now.Add(-time.Minute).In(beijing), now.Add(time.Minute).In(beijing))
	require.NoError(t, err)
	require.Len(t, usages, 1, "month bounds supplied in Beijing time must preserve the same UTC window")
	require.Error(t, s.db.Model(&callReceipt{}).Where("call_id = ?", job.CallID).Update("outcome", "changed").Error)
}

func TestStoreOrphanIsUnknownAndDoesNotBlockHumanHandoff(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "orphan-conversation", now)
	require.NoError(t, err)
	job, err := s.BeginCall(ctx, BeginInput{UserID: 8, ConversationID: c.ID, IdempotencyKey: "orphan-message-key", Content: "help", Model: "flash", ChannelID: 4, Supplier: "xcloud", Now: now})
	require.NoError(t, err)
	require.NoError(t, s.MarkDispatched(ctx, job.CallID))
	require.NoError(t, s.InterruptOrphans(ctx, now.Add(time.Second)))
	usage, err := ReadFinancialUsage(ctx, s.db, now.Add(-time.Minute), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, usage, 1)
	require.True(t, usage[0].Dispatched)
	require.Nil(t, usage[0].QuoteCostU)
	require.Nil(t, usage[0].InputTokens)
	ticket, err := s.CreateTicket(ctx, 8, TicketInput{ConversationID: c.ID, Subject: "请求没有答复", Content: "请人工核查", IdempotencyKey: "orphan-ticket-key"}, now)
	require.NoError(t, err)
	require.Equal(t, "open", ticket.Status)
}

func TestStoreNonDispatchedCallHasLocalZeroEvidence(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "local-zero-conversation", now)
	require.NoError(t, err)
	job, err := s.BeginCall(ctx, BeginInput{UserID: 8, ConversationID: c.ID, IdempotencyKey: "local-zero-message", Content: "help", Model: "flash", ChannelID: 8, Supplier: "xcloud", Now: now})
	require.NoError(t, err)
	require.NoError(t, s.InterruptOrphans(ctx, now.Add(time.Second)))
	rows, err := ReadFinancialUsage(ctx, s.db, now.Add(-time.Minute), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, job.CallID, rows[0].CallID)
	require.False(t, rows[0].Dispatched)
	require.Equal(t, "not_dispatched", rows[0].Outcome)
	require.NotNil(t, rows[0].QuoteCostU)
	require.Equal(t, "0", *rows[0].QuoteCostU)
	require.Equal(t, "local_dispatch_log:not_dispatched", rows[0].QuoteEvidence)
}

func TestTicketReplyAndStatusAreOwnerVisibleAndIdempotent(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "ticket-conversation", now)
	require.NoError(t, err)
	ticket, err := s.CreateTicket(ctx, 8, TicketInput{ConversationID: c.ID, Subject: "接入问题", Content: "请帮忙检查", IdempotencyKey: "ticket-creation-key"}, now)
	require.NoError(t, err)
	_, err = s.CreateTicket(ctx, 9, TicketInput{ConversationID: c.ID, Subject: "越权", Content: "private", IdempotencyKey: "foreign-ticket-key"}, now)
	require.ErrorIs(t, err, ErrNotFound)
	reply, err := s.ReplyTicket(ctx, 10, ticket.ID, "已收到，正在核对。", "human-reply-key", now)
	require.NoError(t, err)
	_, err = s.ReplyTicket(ctx, 10, ticket.ID, "已收到，正在核对。", "human-reply-key", now)
	require.NoError(t, err)
	view, err := s.Conversation(ctx, 8, c.ID)
	require.NoError(t, err)
	require.Equal(t, "human", view.Messages[len(view.Messages)-1].Role)
	require.Equal(t, reply.Message.ID, view.Messages[len(view.Messages)-1].ID)
	_, err = s.ChangeTicketStatus(ctx, 10, ticket.ID, "resolved", 1, "stale-status-key", now)
	require.ErrorIs(t, err, ErrConflict)
	closed, err := s.ChangeTicketStatus(ctx, 10, ticket.ID, "resolved", reply.Ticket.Version, "close-status-key", now)
	require.NoError(t, err)
	require.Equal(t, "resolved", closed.Status)
}
