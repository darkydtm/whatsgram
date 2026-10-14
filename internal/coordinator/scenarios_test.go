package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
	"github.com/go-telegram/bot/models"
)

func setupActiveChat(t *testing.T, coord *Coordinator, st *store.Store, waJID string, tgChatID, threadID int64) store.Chat {
	t.Helper()
	ctx := context.Background()
	cand := domain.ChatCandidate{
		WhatsAppJID: waJID,
		Title:       "Test Chat",
		Source:      "test",
	}
	if err := st.DiscoverChat(ctx, cand, tgChatID); err != nil {
		t.Fatalf("failed to discover chat: %v", err)
	}
	topicRef := domain.TopicRef{
		ChatID:     tgChatID,
		ThreadID:   threadID,
		Generation: 1,
	}
	if err := st.BindTopic(ctx, waJID, topicRef); err != nil {
		t.Fatalf("failed to bind topic: %v", err)
	}
	chat, err := st.Chat(ctx, waJID)
	if err != nil {
		t.Fatalf("failed to get active chat: %v", err)
	}
	return chat
}

// Scenario 1: Create + multiple edits Telegram -> WhatsApp.
func TestScenario1_CreateAndMultipleEditsTelegramToWhatsApp(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// 1. Initial message create
	createEvent := makeTelegramMessageEvent("msg-1", -10012345, 42, 10, "original message")
	if _, err := st.Accept(ctx, createEvent); err != nil {
		t.Fatalf("failed to accept create event: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox create failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery create failed: %v", err)
	}
	if len(waPort.sentRequests) != 1 {
		t.Fatalf("expected 1 sent request to WA, got %d", len(waPort.sentRequests))
	}
	if waPort.sentRequests[0].Content.Text != "original message" {
		t.Fatalf("unexpected WA sent text: %s", waPort.sentRequests[0].Content.Text)
	}

	// 2. First edit
	edit1 := createEvent
	edit1.Key = "msg-1-edit-1"
	edit1.Kind = domain.MessageEdit
	edit1.Version.Order = 200
	edit1.Version.Tie = "edit-1"
	edit1.Text = &domain.TextContent{Text: "edited once"}
	if _, err := st.Accept(ctx, edit1); err != nil {
		t.Fatalf("failed to accept edit 1: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox edit 1 failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery edit 1 failed: %v", err)
	}
	if len(waPort.editedMessages) != 1 {
		t.Fatalf("expected 1 WA edit call, got %d", len(waPort.editedMessages))
	}
	if waPort.editedMessages[0].Content.Text != "edited once" {
		t.Fatalf("unexpected WA edit 1 text: %s", waPort.editedMessages[0].Content.Text)
	}
	if waPort.editedMessages[0].Ref.Chat != chat.WhatsAppJID {
		t.Fatalf("unexpected WA edit target chat: %s", waPort.editedMessages[0].Ref.Chat)
	}

	// 3. Second edit
	edit2 := createEvent
	edit2.Key = "msg-1-edit-2"
	edit2.Kind = domain.MessageEdit
	edit2.Version.Order = 300
	edit2.Version.Tie = "edit-2"
	edit2.Text = &domain.TextContent{Text: "edited twice"}
	if _, err := st.Accept(ctx, edit2); err != nil {
		t.Fatalf("failed to accept edit 2: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox edit 2 failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery edit 2 failed: %v", err)
	}
	if len(waPort.editedMessages) != 2 {
		t.Fatalf("expected 2 WA edit calls, got %d", len(waPort.editedMessages))
	}
	if waPort.editedMessages[1].Content.Text != "edited twice" {
		t.Fatalf("unexpected WA edit 2 text: %s", waPort.editedMessages[1].Content.Text)
	}
}

// Scenario 2: Create + edits WhatsApp -> Telegram.
func TestScenario2_CreateAndEditsWhatsAppToTelegram(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// 1. Initial message from WhatsApp
	createEvent := makeWhatsAppMessageEvent("wa-msg-1", chat.WhatsAppJID, chat.WhatsAppJID, false, "hello from whatsapp")
	if _, err := st.Accept(ctx, createEvent); err != nil {
		t.Fatalf("failed to accept WA create event: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox WA create failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery WA create failed: %v", err)
	}
	if len(tgPort.sentRequests) != 1 {
		t.Fatalf("expected 1 TG sent request, got %d", len(tgPort.sentRequests))
	}
	if tgPort.sentRequests[0].Content.Text != "hello from whatsapp" {
		t.Fatalf("unexpected TG sent text: %s", tgPort.sentRequests[0].Content.Text)
	}

	// 2. First edit from WhatsApp
	edit1 := createEvent
	edit1.Key = "wa-msg-1/edit-1"
	edit1.Kind = domain.MessageEdit
	edit1.Version.Order = 200
	edit1.Version.Tie = "wa-edit-1"
	edit1.Text = &domain.TextContent{Text: "hello from whatsapp (v2)"}
	if _, err := st.Accept(ctx, edit1); err != nil {
		t.Fatalf("failed to accept WA edit 1: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox WA edit 1 failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery WA edit 1 failed: %v", err)
	}
	if len(tgPort.editedMessages) != 1 {
		t.Fatalf("expected 1 TG edit call, got %d", len(tgPort.editedMessages))
	}
	if tgPort.editedMessages[0].Content.Text != "hello from whatsapp (v2)" {
		t.Fatalf("unexpected TG edited text: %s", tgPort.editedMessages[0].Content.Text)
	}

	// 3. Second edit from WhatsApp
	edit2 := createEvent
	edit2.Key = "wa-msg-1/edit-2"
	edit2.Kind = domain.MessageEdit
	edit2.Version.Order = 300
	edit2.Version.Tie = "wa-edit-2"
	edit2.Text = &domain.TextContent{Text: "hello from whatsapp (v3 final)"}
	if _, err := st.Accept(ctx, edit2); err != nil {
		t.Fatalf("failed to accept WA edit 2: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox WA edit 2 failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery WA edit 2 failed: %v", err)
	}
	if len(tgPort.editedMessages) != 2 {
		t.Fatalf("expected 2 TG edit calls, got %d", len(tgPort.editedMessages))
	}
	if tgPort.editedMessages[1].Content.Text != "hello from whatsapp (v3 final)" {
		t.Fatalf("unexpected TG edited text 2: %s", tgPort.editedMessages[1].Content.Text)
	}
}

// Scenario 3: Edit from owner's other device with fallback notice when Telegram representation cannot be edited.
func TestScenario3_WhatsAppOwnerOtherDeviceEditFallbackNotice(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// Create initial message
	createEvent := makeWhatsAppMessageEvent("wa-msg-fallback", chat.WhatsAppJID, "15551234567@s.whatsapp.net", true, "initial text")
	if _, err := st.Accept(ctx, createEvent); err != nil {
		t.Fatalf("failed to accept: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery failed: %v", err)
	}

	// Telegram port edit fails with permission denied (e.g. message too old or rights lost)
	tgPort.editErr = domain.AdapterError{
		Class: domain.Permission,
		Code:  "permission_denied",
	}

	// WhatsApp edit arrives from owner
	editEvent := createEvent
	editEvent.Key = "wa-msg-fallback/edit-fallback"
	editEvent.Kind = domain.MessageEdit
	editEvent.Version.Order = 500
	editEvent.Version.Tie = "fallback-edit"
	editEvent.Text = &domain.TextContent{Text: "edited text from phone"}
	if _, err := st.Accept(ctx, editEvent); err != nil {
		t.Fatalf("failed to accept edit: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox edit failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery edit failed: %v", err)
	}

	// Verify fallback notification was sent to Telegram
	lastTg := tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "✏️ Message edited: You: edited text from phone") {
		t.Fatalf("expected degraded edit notification, got: %s", lastTg.Content.Text)
	}
	if lastTg.Content.ReplyTo == nil {
		t.Fatalf("expected reply reference to original message, got nil")
	}
}

// Scenario 4: Reaction: add, replace, remove.
func TestScenario4_ReactionsLifecycle(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// 1. Create a WhatsApp message
	createEvent := makeWhatsAppMessageEvent("rx-target-msg", chat.WhatsAppJID, chat.WhatsAppJID, false, "message to react to")
	if _, err := st.Accept(ctx, createEvent); err != nil {
		t.Fatalf("failed to accept: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox create failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery create failed: %v", err)
	}

	// A. WhatsApp -> Telegram: Add reaction
	rxAdd := createEvent
	rxAdd.Key = "rx-target-msg/rx-add"
	rxAdd.Kind = domain.ReactionSet
	rxAdd.Version.Order = 200
	rxAdd.Version.Tie = "rx-1"
	rxAdd.Text = &domain.TextContent{Text: "👍"}
	if _, err := st.Accept(ctx, rxAdd); err != nil {
		t.Fatalf("failed to accept rx add: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox rx add failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery rx add failed: %v", err)
	}
	if len(tgPort.reactions) != 1 || tgPort.reactions[0].Reaction != "👍" {
		t.Fatalf("expected TG reaction '👍', got: %+v", tgPort.reactions)
	}

	// B. WhatsApp -> Telegram: Replace reaction
	rxReplace := createEvent
	rxReplace.Key = "rx-target-msg/rx-replace"
	rxReplace.Kind = domain.ReactionSet
	rxReplace.Version.Order = 300
	rxReplace.Version.Tie = "rx-2"
	rxReplace.Text = &domain.TextContent{Text: "🔥"}
	if _, err := st.Accept(ctx, rxReplace); err != nil {
		t.Fatalf("failed to accept rx replace: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox rx replace failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery rx replace failed: %v", err)
	}
	if len(tgPort.reactions) != 2 || tgPort.reactions[1].Reaction != "🔥" {
		t.Fatalf("expected TG reaction '🔥', got: %+v", tgPort.reactions)
	}

	// C. WhatsApp -> Telegram: Remove reaction
	rxRemove := createEvent
	rxRemove.Key = "rx-target-msg/rx-remove"
	rxRemove.Kind = domain.ReactionSet
	rxRemove.Version.Order = 400
	rxRemove.Version.Tie = "rx-3"
	rxRemove.Text = &domain.TextContent{Text: ""}
	if _, err := st.Accept(ctx, rxRemove); err != nil {
		t.Fatalf("failed to accept rx remove: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox rx remove failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery rx remove failed: %v", err)
	}
	if len(tgPort.reactions) != 3 || tgPort.reactions[2].Reaction != "" {
		t.Fatalf("expected TG reaction empty (removed), got: %+v", tgPort.reactions)
	}

	// D. Telegram -> WhatsApp: Reaction from driver
	parts, err := st.FindParts(ctx, *createEvent.Target)
	if err != nil {
		t.Fatalf("failed to find parts: %v", err)
	}
	var tgPart *domain.DeliveredPart
	for i := range parts {
		if parts[i].Ref.Platform == domain.Telegram {
			tgPart = &parts[i]
			break
		}
	}
	if tgPart == nil {
		t.Fatalf("telegram part not found")
	}

	tgRx := domain.Event{
		Key:              "tg-rx-1",
		Source:           domain.Telegram,
		Kind:             domain.ReactionSet,
		Chat:             tgPart.Ref.Chat,
		Actor:            "42", // allowed driver
		TelegramThreadID: 10,
		Target:           &tgPart.Ref,
		Version: domain.SourceVersion{
			Kind:        "telegram.message",
			Order:       500,
			Tie:         "tg-rx-1",
			Digest:      "digest-tg-rx",
			StreamEpoch: "epoch1",
		},
		Text:       &domain.TextContent{Text: "❤️"},
		OccurredAt: time.Now(),
	}
	if _, err := st.Accept(ctx, tgRx); err != nil {
		t.Fatalf("failed to accept tg rx: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox tg rx failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery tg rx failed: %v", err)
	}
	if len(waPort.reactions) != 1 || waPort.reactions[0].Reaction != "❤️" {
		t.Fatalf("expected WA reaction '❤️', got: %+v", waPort.reactions)
	}

	// Non-driver reaction should be ignored
	tgRxNonDriver := tgRx
	tgRxNonDriver.Key = "tg-rx-non-driver"
	tgRxNonDriver.Actor = "99" // unauthorized non-driver
	tgRxNonDriver.Version.Order = 600
	tgRxNonDriver.Version.Tie = "tg-rx-non-driver"
	if _, err := st.Accept(ctx, tgRxNonDriver); err != nil {
		t.Fatalf("failed to accept non-driver rx: %v", err)
	}
	_ = coord.StepInbox(ctx)
	if len(waPort.reactions) != 1 {
		t.Fatalf("non-driver reaction must not reach WhatsApp")
	}
}

// Scenario 5: Revoke in WhatsApp and /del in Telegram.
func TestScenario5_RevokeInWhatsAppAndDelInTelegram(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// --- Part A: WhatsApp revoke -> Telegram delete ---
	waMsg := makeWhatsAppMessageEvent("wa-to-revoke", chat.WhatsAppJID, chat.WhatsAppJID, false, "message to revoke")
	if _, err := st.Accept(ctx, waMsg); err != nil {
		t.Fatalf("failed to accept waMsg: %v", err)
	}
	_ = coord.StepInbox(ctx)
	_ = coord.StepDelivery(ctx)

	revokeEvent := waMsg
	revokeEvent.Key = "wa-to-revoke/revoke"
	revokeEvent.Kind = domain.MessageRevoke
	revokeEvent.Version.Order = 200
	revokeEvent.Version.Tie = "revoke-1"
	if _, err := st.Accept(ctx, revokeEvent); err != nil {
		t.Fatalf("failed to accept revoke: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox revoke failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery revoke failed: %v", err)
	}
	if len(tgPort.deletedMessages) != 1 {
		t.Fatalf("expected Telegram message to be deleted, got %d deletions", len(tgPort.deletedMessages))
	}
	msgState, err := st.Message(ctx, *waMsg.Target)
	if err != nil || !msgState.Tombstone {
		t.Fatalf("expected message to be tombstoned in store, state: %+v, err: %v", msgState, err)
	}

	// --- Part B: WhatsApp revoke with Telegram delete failure -> fallback notice ---
	waMsg2 := makeWhatsAppMessageEvent("wa-to-revoke-fallback", chat.WhatsAppJID, chat.WhatsAppJID, false, "message to revoke fallback")
	if _, err := st.Accept(ctx, waMsg2); err != nil {
		t.Fatalf("failed to accept waMsg2: %v", err)
	}
	_ = coord.StepInbox(ctx)
	_ = coord.StepDelivery(ctx)

	tgPort.deleteErr = domain.AdapterError{
		Class: domain.Permission,
		Code:  "permission_denied",
	}
	revokeEvent2 := waMsg2
	revokeEvent2.Key = "wa-to-revoke-fallback/revoke"
	revokeEvent2.Kind = domain.MessageRevoke
	revokeEvent2.Version.Order = 200
	revokeEvent2.Version.Tie = "revoke-2"
	if _, err := st.Accept(ctx, revokeEvent2); err != nil {
		t.Fatalf("failed to accept revoke 2: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox revoke 2 failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery revoke 2 failed: %v", err)
	}
	lastTg := tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "🗑 Message revoked by sender") {
		t.Fatalf("expected revoke fallback notification, got: %s", lastTg.Content.Text)
	}

	// Reset delete error
	tgPort.deleteErr = nil

	// --- Part C: /del in Telegram -> WhatsApp revoke ---
	tgMsg := makeTelegramMessageEvent("1001", -10012345, 42, 10, "telegram message to delete")
	if _, err := st.Accept(ctx, tgMsg); err != nil {
		t.Fatalf("failed to accept tgMsg: %v", err)
	}
	_ = coord.StepInbox(ctx)
	_ = coord.StepDelivery(ctx)

	// Send /del in reply to message 1001
	delCmd := makeTelegramMessageEvent("1002", -10012345, 42, 10, "/del")
	delCmd.Text.ReplyTo = &domain.MessageRef{
		Platform: domain.Telegram,
		Chat:     strconv.FormatInt(-10012345, 10),
		Author:   "42",
		ID:       "1001",
	}
	rawUpdate := models.Update{
		Message: &models.Message{
			ID:              1002,
			Text:            "/del",
			MessageThreadID: 10,
			From:            &models.User{ID: 42},
			Chat:            models.Chat{ID: -10012345},
			ReplyToMessage:  &models.Message{ID: 1001, From: &models.User{ID: 42}},
		},
	}
	delCmd.Raw, _ = json.Marshal(rawUpdate)

	if _, err := st.Accept(ctx, delCmd); err != nil {
		t.Fatalf("failed to accept /del command: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox /del failed: %v", err)
	}
	if len(waPort.revokedMessages) != 1 {
		t.Fatalf("expected WA revoke call from /del, got %d", len(waPort.revokedMessages))
	}
}

// Scenario 6: Duplicate events & loop prevention.
func TestScenario6_DuplicateEventsAndLoopPrevention(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// 1. Initial message from WhatsApp
	msg := makeWhatsAppMessageEvent("wa-dedupe-1", chat.WhatsAppJID, chat.WhatsAppJID, false, "dedupe test message")
	adm, err := st.Accept(ctx, msg)
	if err != nil || !adm.Inserted {
		t.Fatalf("initial acceptance failed: inserted=%v, err=%v", adm.Inserted, err)
	}
	_ = coord.StepInbox(ctx)
	_ = coord.StepDelivery(ctx)
	if len(tgPort.sentRequests) != 1 {
		t.Fatalf("expected 1 sent message to TG, got %d", len(tgPort.sentRequests))
	}

	// 2. Duplicate admission attempt with same key
	admDup, err := st.Accept(ctx, msg)
	if err != nil || admDup.Inserted {
		t.Fatalf("expected duplicate not inserted, got inserted=%v, err=%v", admDup.Inserted, err)
	}

	// 3. Loop prevention: bridge send record matches WhatsApp echo
	sendID := "wa-bridge-send-echo-123"
	coord.recordBridgeSend(sendID)

	echoMsg := makeWhatsAppMessageEvent(sendID, chat.WhatsAppJID, "15551234567@s.whatsapp.net", true, "echo message")
	if _, err := st.Accept(ctx, echoMsg); err != nil {
		t.Fatalf("failed to accept echo: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox on echo failed: %v", err)
	}

	// Verify no new deliveries planned for TG
	if len(tgPort.sentRequests) != 1 {
		t.Fatalf("echo message must not be sent to Telegram, count: %d", len(tgPort.sentRequests))
	}
}

// Scenario 7: Out-of-order events: edit arrives before create mapping exists (verify retry/quarantine or deferral).
func TestScenario7_OutOfOrderEditBeforeCreate(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	coord.cfg.Runtime.RetryBase = 50 * time.Millisecond
	ctx := context.Background()
	_ = setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// 1. Edit event arrives before create
	createEvent := makeTelegramMessageEvent("msg-out-of-order", -10012345, 42, 10, "original text now")
	editEvent := createEvent
	editEvent.Key = "msg-out-of-order-edit"
	editEvent.Kind = domain.MessageEdit
	editEvent.Version.Order = 200
	editEvent.Version.Tie = "edit-early"
	editEvent.Text = &domain.TextContent{Text: "edited text first"}

	if _, err := st.Accept(ctx, editEvent); err != nil {
		t.Fatalf("failed to accept early edit: %v", err)
	}

	// 2. Process early edit: should be deferred because target is not mapped
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox early edit failed: %v", err)
	}

	// Next claim immediately should find no work because edit was deferred to future
	now := time.Now()
	_, _, err := st.ClaimInbox(ctx, now, 10*time.Second)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork while deferred, got %v", err)
	}

	// 3. Now create event arrives
	if _, err := st.Accept(ctx, createEvent); err != nil {
		t.Fatalf("failed to accept create: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox create failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery create failed: %v", err)
	}

	// 4. Wait for deferral backoff to pass
	time.Sleep(150 * time.Millisecond)
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox on deferred edit failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery on deferred edit failed: %v", err)
	}

	// Verify both create and edit reached WhatsApp
	if len(waPort.sentRequests) != 1 {
		t.Fatalf("expected 1 WA sent request, got %d", len(waPort.sentRequests))
	}
	if len(waPort.editedMessages) != 1 {
		t.Fatalf("expected 1 WA edit message, got %d", len(waPort.editedMessages))
	}
	if waPort.editedMessages[0].Content.Text != "edited text first" {
		t.Fatalf("unexpected WA edit text: %s", waPort.editedMessages[0].Content.Text)
	}
}

// Scenario 8: Late edit arriving after deletion (verify rejection/conflict).
func TestScenario8_LateEditArrivingAfterDeletion(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// 1. Create message
	createEvent := makeWhatsAppMessageEvent("wa-late-edit-target", chat.WhatsAppJID, chat.WhatsAppJID, false, "initial")
	if _, err := st.Accept(ctx, createEvent); err != nil {
		t.Fatalf("failed to accept create: %v", err)
	}
	_ = coord.StepInbox(ctx)
	_ = coord.StepDelivery(ctx)

	// 2. Revoke message
	revokeEvent := createEvent
	revokeEvent.Key = "wa-late-edit-target/revoke"
	revokeEvent.Kind = domain.MessageRevoke
	revokeEvent.Version.Order = 200
	revokeEvent.Version.Tie = "revoke"
	if _, err := st.Accept(ctx, revokeEvent); err != nil {
		t.Fatalf("failed to accept revoke: %v", err)
	}
	_ = coord.StepInbox(ctx)
	_ = coord.StepDelivery(ctx)

	// Verify tombstone in store
	msgState, err := st.Message(ctx, *createEvent.Target)
	if err != nil || !msgState.Tombstone {
		t.Fatalf("message should be tombstoned, got state=%+v, err=%v", msgState, err)
	}

	// 3. Late edit arrives after tombstone
	lateEdit := createEvent
	lateEdit.Key = "wa-late-edit-target/late-edit"
	lateEdit.Kind = domain.MessageEdit
	lateEdit.Version.Order = 300
	lateEdit.Version.Tie = "late-edit"
	lateEdit.Text = &domain.TextContent{Text: "late edit text"}
	if _, err := st.Accept(ctx, lateEdit); err != nil {
		t.Fatalf("failed to accept late edit: %v", err)
	}

	initialWaEdits := len(waPort.editedMessages)
	_ = coord.StepInbox(ctx)

	// Ensure no new delivery is planned or sent
	err = coord.StepDelivery(ctx)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork after late edit on tombstone, got %v", err)
	}
	if len(waPort.editedMessages) != initialWaEdits {
		t.Fatalf("late edit on tombstone must not produce new edit delivery")
	}

	// Verify message remains tombstoned in store
	msgStateAfter, err := st.Message(ctx, *createEvent.Target)
	if err != nil || !msgStateAfter.Tombstone {
		t.Fatalf("message must remain tombstoned, got state=%+v", msgStateAfter)
	}
}

// Scenario 9: 429 rate limit & backoff recovery.
func TestScenario9_RateLimit429AndBackoffRecovery(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	coord.cfg.Runtime.RetryBase = 100 * time.Millisecond
	coord.cfg.Runtime.RetryMax = time.Second
	ctx := context.Background()
	_ = setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	msg := makeTelegramMessageEvent("msg-rate-limit", -10012345, 42, 10, "rate limited message")
	if _, err := st.Accept(ctx, msg); err != nil {
		t.Fatalf("failed to accept: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	now := time.Now()
	delivery, err := st.ClaimDelivery(ctx, now, 10*time.Second)
	if err != nil {
		t.Fatalf("ClaimDelivery failed: %v", err)
	}

	// Adapter encounters 429 rate limit with RetryAfter
	rateLimitErr := domain.AdapterError{
		Class:      domain.RateLimited,
		Code:       "rate_limited",
		RetryAfter: 300 * time.Millisecond,
	}

	// Coordinator handles rate limit before send is marked started
	if err := coord.handleDeliveryError(ctx, delivery, rateLimitErr, false, now); err != nil {
		t.Fatalf("handleDeliveryError failed: %v", err)
	}

	// Immediate claim before backoff duration must return ErrNoWork
	_, err = st.ClaimDelivery(ctx, now.Add(100*time.Millisecond), 10*time.Second)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork during backoff, got %v", err)
	}

	// After backoff duration (300ms), delivery is claimable again
	future := now.Add(400 * time.Millisecond)
	redelivery, err := st.ClaimDelivery(ctx, future, 10*time.Second)
	if err != nil {
		t.Fatalf("expected delivery to be claimable after backoff: %v", err)
	}
	if redelivery.ID != delivery.ID {
		t.Fatalf("expected redelivery ID %d, got %d", delivery.ID, redelivery.ID)
	}

	if err := st.BeginSend(ctx, redelivery.ID, redelivery.LeaseToken, future); err != nil {
		t.Fatalf("BeginSend failed: %v", err)
	}
	res, err := coord.sendWhatsApp(ctx, redelivery)
	if err != nil {
		t.Fatalf("sendWhatsApp failed: %v", err)
	}
	comp := domain.Completion{
		Result:         res,
		AppliedVersion: redelivery.ExpectedVersion,
		EchoKeys:       []string{redelivery.WhatsAppSendID},
	}
	if err := st.CompleteDelivery(ctx, redelivery.ID, redelivery.LeaseToken, comp, future); err != nil {
		t.Fatalf("CompleteDelivery failed: %v", err)
	}
	if len(waPort.sentRequests) != 1 {
		t.Fatalf("expected 1 sent request after recovery, got %d", len(waPort.sentRequests))
	}
}

// Scenario 10: Restart with pending deliveries and active leases.
func TestScenario10_RestartWithPendingDeliveriesAndActiveLeases(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	_ = setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	msg := makeTelegramMessageEvent("msg-restart", -10012345, 42, 10, "survive restart message")
	if _, err := st.Accept(ctx, msg); err != nil {
		t.Fatalf("failed to accept: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	// Claim delivery with short lease
	now := time.Now()
	d, err := st.ClaimDelivery(ctx, now, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("ClaimDelivery failed: %v", err)
	}

	// Simulate restart after lease expires: lease expired in store
	future := now.Add(500 * time.Millisecond)
	if err := st.RecoverExpiredLeases(ctx, future); err != nil {
		t.Fatalf("RecoverExpiredLeases failed: %v", err)
	}

	// New coordinator instance starts up
	coord2, err := New(coord.cfg, st, coord.tgPort, waPort, coord.pairingPort, coord.policy)
	if err != nil {
		t.Fatalf("New coordinator 2 failed: %v", err)
	}

	// Delivery can now be claimed and executed by new coordinator
	d2, err := st.ClaimDelivery(ctx, future, 10*time.Second)
	if err != nil {
		t.Fatalf("new coordinator could not claim delivery: %v", err)
	}
	if d2.ID != d.ID {
		t.Fatalf("expected same delivery ID %d, got %d", d.ID, d2.ID)
	}

	if err := st.BeginSend(ctx, d2.ID, d2.LeaseToken, future); err != nil {
		t.Fatalf("BeginSend failed: %v", err)
	}
	res, err := coord2.sendWhatsApp(ctx, d2)
	if err != nil {
		t.Fatalf("sendWhatsApp failed: %v", err)
	}
	comp := domain.Completion{
		Result:         res,
		AppliedVersion: d2.ExpectedVersion,
		EchoKeys:       []string{d2.WhatsAppSendID},
	}
	if err := st.CompleteDelivery(ctx, d2.ID, d2.LeaseToken, comp, future); err != nil {
		t.Fatalf("CompleteDelivery failed: %v", err)
	}
	if len(waPort.sentRequests) != 1 {
		t.Fatalf("expected 1 sent request to WA, got %d", len(waPort.sentRequests))
	}
}

// Scenario 11: Fail after send, before result commit (at-least-once / deduplication).
func TestScenario11_FailAfterSendBeforeCommit(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	msg := makeTelegramMessageEvent("msg-fail-after-send", -10012345, 42, 10, "at least once test")
	if _, err := st.Accept(ctx, msg); err != nil {
		t.Fatalf("failed to accept: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	now := time.Now()
	d, err := st.ClaimDelivery(ctx, now, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("ClaimDelivery failed: %v", err)
	}
	if err := st.BeginSend(ctx, d.ID, d.LeaseToken, now); err != nil {
		t.Fatalf("BeginSend failed: %v", err)
	}

	// Message was sent over wire to WhatsApp
	_, err = coord.sendWhatsApp(ctx, d)
	if err != nil {
		t.Fatalf("sendWhatsApp failed: %v", err)
	}
	if len(waPort.sentRequests) != 1 {
		t.Fatalf("expected 1 sent request on wire, got %d", len(waPort.sentRequests))
	}
	firstSendID := waPort.sentRequests[0].WhatsAppSendID

	// CRASH before CompleteDelivery is committed:
	// Lease expires, recovery detects send_started_at_ms != null
	future := now.Add(500 * time.Millisecond)
	if err := st.RecoverExpiredLeases(ctx, future); err != nil {
		t.Fatalf("RecoverExpiredLeases failed: %v", err)
	}

	// Delivery transitions to "uncertain" to avoid blind resend
	stored, err := st.Delivery(ctx, d.ID)
	if err != nil || stored.State != "uncertain" {
		t.Fatalf("expected state 'uncertain' after crash, got state=%s, err=%v", stored.State, err)
	}

	// Delivery is NOT claimed again blindly (prevent duplicate send over wire)
	_, err = st.ClaimDelivery(ctx, future, 10*time.Second)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork to prevent duplicate send, got %v", err)
	}

	// Deduplication: Echo reservation matches the send ID
	echo := store.Echo{
		Platform: domain.WhatsApp,
		Chat:     chat.WhatsAppJID,
		Actor:    chat.WhatsAppJID,
		EventID:  firstSendID,
		Kind:     d.Kind,
		Digest:   d.ExpectedVersion.Digest,
	}
	matched, err := st.MatchEcho(ctx, echo)
	if err != nil || !matched {
		t.Fatalf("expected echo reservation match for sendID %s, got %v, err=%v", firstSendID, matched, err)
	}

	// Verify loop prevention in coordinator: echo event is recognized as bridge-dispatched
	echoEvent := makeWhatsAppMessageEvent(firstSendID, chat.WhatsAppJID, chat.WhatsAppJID, true, "at least once test")
	if !coord.isBridgeDispatched(ctx, echoEvent, firstSendID) {
		t.Fatalf("expected isBridgeDispatched to recognize bridge sendID %s", firstSendID)
	}
}
