package support

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func tinyImage() *ImageInput {
	return &ImageInput{MIMEType: "image/png", DataBase64: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aQxkAAAAASUVORK5CYII="}
}

func TestPrivateAttachmentIsOwnerScopedAndReplayDoesNotDuplicate(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "attachment-conversation", now)
	require.NoError(t, err)
	in := BeginInput{UserID: 8, ConversationID: c.ID, IdempotencyKey: "attachment-message", Content: "截图", HasImage: true, Attachment: tinyImage(), ImageHash: "fixture-image", Model: "flash", ChannelID: 8, Supplier: "xcloud", Now: now}
	job, err := s.BeginCall(ctx, in)
	require.NoError(t, err)
	require.NotNil(t, job.Message.AttachmentID)
	data, err := s.Attachment(ctx, 8, *job.Message.AttachmentID)
	require.NoError(t, err)
	require.Equal(t, tinyImage().DataBase64, data.DataBase64)
	_, err = s.Attachment(ctx, 9, *job.Message.AttachmentID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.Attachment(ctx, 0, *job.Message.AttachmentID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.BeginCall(ctx, in)
	require.ErrorIs(t, err, ErrBusy)
	var count int64
	require.NoError(t, s.db.Model(&attachmentRecord{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	_, err = base64.StdEncoding.DecodeString(data.DataBase64)
	require.NoError(t, err)
}

func TestHumanFollowupDoesNotCreateBillableCall(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "human-followup-conversation", now)
	require.NoError(t, err)
	_, err = s.CreateTicket(ctx, 8, TicketInput{ConversationID: c.ID, Subject: "人工", Content: "请核查", IdempotencyKey: "human-followup-ticket"}, now)
	require.NoError(t, err)
	message, err := s.AppendHumanMessage(ctx, 8, c.ID, "补充截图", tinyImage(), "human-followup-message", now)
	require.NoError(t, err)
	again, err := s.AppendHumanMessage(ctx, 8, c.ID, "补充截图", tinyImage(), "human-followup-message", now)
	require.NoError(t, err)
	require.Equal(t, message.Message.ID, again.Message.ID)
	require.Empty(t, message.CallID)
	require.NotNil(t, message.Message.AttachmentID)
	var calls int64
	require.NoError(t, s.db.Model(&callRecord{}).Count(&calls).Error)
	require.Zero(t, calls)
}

func TestMessageIdempotencySurvivesHandoffAndResolution(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "handoff-idem-conversation", now)
	require.NoError(t, err)
	input := BeginInput{UserID: 8, ConversationID: c.ID, IdempotencyKey: "before-handoff-message", Content: "same message", Model: "flash", ChannelID: 8, Supplier: "xcloud", Now: now}
	job, err := s.BeginCall(ctx, input)
	require.NoError(t, err)
	original, err := s.FinishCall(ctx, job.CallID, ProviderResult{Content: "original answer", Outcome: "completed"}, now)
	require.NoError(t, err)
	ticket, err := s.CreateTicket(ctx, 8, TicketInput{ConversationID: c.ID, Subject: "人工", Content: "请核查", IdempotencyKey: "handoff-idem-ticket"}, now)
	require.NoError(t, err)
	replay, err := s.AppendHumanMessage(ctx, 8, c.ID, "same message", nil, "before-handoff-message", now)
	require.NoError(t, err)
	require.Equal(t, original.Reply.ID, replay.Reply.ID, "handoff must not turn a retry into a second message")
	human, err := s.AppendHumanMessage(ctx, 8, c.ID, "human followup", nil, "after-handoff-message", now)
	require.NoError(t, err)
	_, err = s.ChangeTicketStatus(ctx, 10, ticket.ID, "resolved", ticket.Version, "resolve-idem-ticket", now)
	require.NoError(t, err)
	replayed, err := s.BeginCall(ctx, BeginInput{UserID: 8, ConversationID: c.ID, IdempotencyKey: "after-handoff-message", Content: "human followup", Model: "flash", ChannelID: 8, Supplier: "xcloud", Now: now})
	require.NoError(t, err)
	require.NotNil(t, replayed.Reply)
	require.Equal(t, human.Reply.ID, replayed.Reply.ID)
}

func TestHandoffFencesQueuedModelDispatch(t *testing.T) {
	s, ctx, now := testStore(t), context.Background(), time.Now().UTC()
	c, err := s.CreateConversation(ctx, 8, "handoff-fence-conversation", now)
	require.NoError(t, err)
	job, err := s.BeginCall(ctx, BeginInput{UserID: 8, ConversationID: c.ID, IdempotencyKey: "handoff-fence-message", Content: "question", Model: "flash", ChannelID: 8, Supplier: "xcloud", Now: now})
	require.NoError(t, err)
	_, err = s.CreateTicket(ctx, 8, TicketInput{ConversationID: c.ID, Subject: "人工", Content: "请核查", IdempotencyKey: "handoff-fence-ticket"}, now)
	require.NoError(t, err)
	require.ErrorIs(t, s.MarkDispatched(ctx, job.CallID), ErrHumanHandoff)
}
