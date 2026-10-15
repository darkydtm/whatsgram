package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
)

// Verifies BLOCKER-1: When a message is originally bridge-sent to WhatsApp (BridgeOwned: true),
// owner-initiated edit and reaction from WhatsApp are forwarded to Telegram and not dropped as echoes.
func TestWhatsAppOwnerMutationOnBridgeSentMessage(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	ownerJID := "15551234567@s.whatsapp.net"
	chat := setupActiveChat(t, coord, st, ownerJID, -10012345, 10)

	// 1. Initial message sent from Telegram to WhatsApp
	tgCreate := makeTelegramMessageEvent("tg-msg-owner-mut-1", -10012345, 42, 10, "Hello bridge")
	if _, err := st.Accept(ctx, tgCreate); err != nil {
		t.Fatalf("failed to accept tgCreate: %v", err)
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
	waSendID := waPort.sentRequests[0].WhatsAppSendID
	if waSendID == "" {
		t.Fatal("expected non-empty WhatsAppSendID")
	}

	// Verify WhatsApp part is marked BridgeOwned: true in store
	parts, err := st.FindParts(ctx, *tgCreate.Target)
	if err != nil {
		t.Fatalf("FindParts failed: %v", err)
	}
	var waPart *domain.DeliveredPart
	for i := range parts {
		if parts[i].Ref.Platform == domain.WhatsApp {
			waPart = &parts[i]
			break
		}
	}
	if waPart == nil || !waPart.BridgeOwned {
		t.Fatalf("expected waPart with BridgeOwned: true, got %+v", waPart)
	}

	// 2. Owner edits this message from their phone on WhatsApp
	ownerEdit := domain.Event{
		Key:        "phone-edit-stanza-1",
		Source:     domain.WhatsApp,
		Kind:       domain.MessageEdit,
		Chat:       chat.WhatsAppJID,
		Actor:      ownerJID,
		Target:     &domain.MessageRef{Platform: domain.WhatsApp, Chat: chat.WhatsAppJID, Author: waPart.Ref.Author, ID: waSendID},
		OccurredAt: time.Now(),
		Version: domain.SourceVersion{
			Kind:        string(waPart.Ref.Platform),
			Order:       200,
			Tie:         "phone-edit-1",
			Digest:      "digest-phone-edit-1",
			StreamEpoch: "epoch1",
		},
		Text: &domain.TextContent{Text: "edited text from phone"},
		Raw:  []byte(`{"from_me":true}`),
	}
	if _, err := st.Accept(ctx, ownerEdit); err != nil {
		t.Fatalf("failed to accept ownerEdit: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox owner edit failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery owner edit failed: %v (%#v)", err, err)
	}

	// Verify degraded fallback notice was sent to Telegram (since Telegram part was user-authored, not bridge-owned)
	lastTg := tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "✏️ Message edited: You: edited text from phone") {
		t.Fatalf("expected degraded fallback notice, got: %s", lastTg.Content.Text)
	}
	if lastTg.Content.ReplyTo == nil || lastTg.Content.ReplyTo.ID != "tg-msg-owner-mut-1" {
		t.Fatalf("expected reply to tg-msg-owner-mut-1, got: %+v", lastTg.Content.ReplyTo)
	}

	// 3. Owner reacts to this message from their phone on WhatsApp
	ownerReaction := domain.Event{
		Key:        "phone-rx-stanza-1",
		Source:     domain.WhatsApp,
		Kind:       domain.ReactionSet,
		Chat:       chat.WhatsAppJID,
		Actor:      ownerJID,
		Target:     &domain.MessageRef{Platform: domain.WhatsApp, Chat: chat.WhatsAppJID, Author: waPart.Ref.Author, ID: waSendID},
		OccurredAt: time.Now(),
		Version: domain.SourceVersion{
			Kind:        string(waPart.Ref.Platform),
			Order:       300,
			Tie:         "phone-rx-1",
			Digest:      "digest-phone-rx-1",
			StreamEpoch: "epoch1",
		},
		Text: &domain.TextContent{Text: "👍"},
		Raw:  []byte(`{"from_me":true}`),
	}
	if _, err := st.Accept(ctx, ownerReaction); err != nil {
		t.Fatalf("failed to accept ownerReaction: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox owner reaction failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery owner reaction failed: %v", err)
	}

	// Verify reaction was delivered to Telegram
	tgPort.mu.Lock()
	reactionCalls := len(tgPort.reactions)
	var lastRx telegramReactionCall
	if reactionCalls > 0 {
		lastRx = tgPort.reactions[reactionCalls-1]
	}
	tgPort.mu.Unlock()

	if reactionCalls != 1 {
		t.Fatalf("expected 1 reaction call on Telegram, got %d", reactionCalls)
	}
	if lastRx.Reaction != "👍" || lastRx.Ref.ID != "tg-msg-owner-mut-1" {
		t.Fatalf("unexpected reaction delivery: %+v", lastRx)
	}

	// 4. Bridge echo edit should still be detected and suppressed
	bridgeSendID := "wa-bridge-send-edit-100"
	coord.recordBridgeSend(bridgeSendID)
	echoEdit := domain.Event{
		Key:        chat.WhatsAppJID + "/" + ownerJID + "/" + bridgeSendID,
		Source:     domain.WhatsApp,
		Kind:       domain.MessageEdit,
		Chat:       chat.WhatsAppJID,
		Actor:      ownerJID,
		Target:     &domain.MessageRef{Platform: domain.WhatsApp, Chat: chat.WhatsAppJID, Author: waPart.Ref.Author, ID: waSendID},
		OccurredAt: time.Now(),
		Version: domain.SourceVersion{
			Kind:        string(waPart.Ref.Platform),
			Order:       400,
			Tie:         bridgeSendID,
			Digest:      "digest-echo-edit-1",
			StreamEpoch: "epoch1",
		},
		Text: &domain.TextContent{Text: "Bridge echo edit"},
		Raw:  []byte(`{"from_me":true}`),
	}
	if _, err := st.Accept(ctx, echoEdit); err != nil {
		t.Fatalf("failed to accept echoEdit: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox echoEdit failed: %v", err)
	}
	err = coord.StepDelivery(ctx)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork because echo was suppressed, got: %v", err)
	}
}

// Verifies BLOCKER-2: Account ownership and permission verification on Telegram edits.
func TestTelegramEditPermissionsAndOwnership(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// 1. Initial message created by user 42
	tgCreate := makeTelegramMessageEvent("tg-edit-perm-1", -10012345, 42, 10, "Original text")
	if _, err := st.Accept(ctx, tgCreate); err != nil {
		t.Fatalf("failed to accept tgCreate: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox create failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery create failed: %v", err)
	}
	if len(waPort.sentRequests) != 1 {
		t.Fatalf("expected 1 sent request, got %d", len(waPort.sentRequests))
	}

	// 2. Authorized edit by original author (user 42)
	authEdit := tgCreate
	authEdit.Key = "tg-edit-perm-auth"
	authEdit.Kind = domain.MessageEdit
	authEdit.Actor = "42"
	authEdit.Version.Order = 200
	authEdit.Version.Tie = "auth-edit"
	authEdit.Version.Digest = "digest-auth-edit"
	authEdit.Text = &domain.TextContent{Text: "Authorized edit"}
	if _, err := st.Accept(ctx, authEdit); err != nil {
		t.Fatalf("failed to accept authEdit: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox authEdit failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery authEdit failed: %v", err)
	}
	waPort.mu.Lock()
	editCount := len(waPort.editedMessages)
	waPort.mu.Unlock()
	if editCount != 1 {
		t.Fatalf("expected 1 edited message on WhatsApp, got %d", editCount)
	}

	// 3. Unauthorized edit by user 43 (allowed user, but not original author and not driver)
	unauthEdit1 := authEdit
	unauthEdit1.Key = "tg-edit-perm-unauth-43"
	unauthEdit1.Actor = "43"
	unauthEdit1.Version.Order = 300
	unauthEdit1.Version.Tie = "unauth-43"
	unauthEdit1.Version.Digest = "digest-unauth-43"
	unauthEdit1.Text = &domain.TextContent{Text: "Tampered edit by 43"}
	if _, err := st.Accept(ctx, unauthEdit1); err != nil {
		t.Fatalf("failed to accept unauthEdit1: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox unauthEdit1 failed: %v", err)
	}
	err := coord.StepDelivery(ctx)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork because unauthorized edit by user 43 was dropped, got: %v", err)
	}
	waPort.mu.Lock()
	editCountAfter := len(waPort.editedMessages)
	waPort.mu.Unlock()
	if editCountAfter != 1 {
		t.Fatalf("expected edit count to remain 1, got %d", editCountAfter)
	}

	// 4. Unauthorized edit by non-allowed user (user 99)
	unauthEdit2 := authEdit
	unauthEdit2.Key = "tg-edit-perm-unauth-99"
	unauthEdit2.Actor = "99"
	unauthEdit2.Version.Order = 400
	unauthEdit2.Version.Tie = "unauth-99"
	unauthEdit2.Version.Digest = "digest-unauth-99"
	unauthEdit2.Text = &domain.TextContent{Text: "Tampered edit by 99"}
	if _, err := st.Accept(ctx, unauthEdit2); err != nil {
		t.Fatalf("failed to accept unauthEdit2: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox unauthEdit2 failed: %v", err)
	}
	err = coord.StepDelivery(ctx)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork because unauthorized edit by user 99 was dropped, got: %v", err)
	}

	// 5. Attempt to edit a non-bridge-owned WhatsApp message
	// Message originated on WhatsApp -> waPart has BridgeOwned: false
	waMsg := makeWhatsAppMessageEvent("wa-incoming-orig", chat.WhatsAppJID, "15559876543@s.whatsapp.net", false, "WA origin message")
	if _, err := st.Accept(ctx, waMsg); err != nil {
		t.Fatalf("failed to accept waMsg: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox waMsg failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery waMsg failed: %v", err)
	}

	// Find the Telegram part created for this WhatsApp message
	parts, err := st.FindParts(ctx, *waMsg.Target)
	if err != nil {
		t.Fatalf("FindParts failed: %v", err)
	}
	var tgPart *domain.DeliveredPart
	for i := range parts {
		if parts[i].Ref.Platform == domain.Telegram {
			tgPart = &parts[i]
			break
		}
	}
	if tgPart == nil {
		t.Fatal("expected tgPart for incoming WA message")
	}

	// User 42 attempts to edit this message from Telegram
	tgEditOnWA := makeTelegramMessageEvent("tg-edit-on-wa-orig", -10012345, 42, 10, "Edit on WA orig")
	tgEditOnWA.Kind = domain.MessageEdit
	tgEditOnWA.Target = &tgPart.Ref
	tgEditOnWA.Version.Digest = "digest-tg-edit-wa"
	tgEditOnWA.Version.Order = 500
	if _, err := st.Accept(ctx, tgEditOnWA); err != nil {
		t.Fatalf("failed to accept tgEditOnWA: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox tgEditOnWA failed: %v", err)
	}
	err = coord.StepDelivery(ctx)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork because target WhatsApp message is not bridge-owned, got: %v", err)
	}
}

// Verifies NON-BLOCKER-1: Transient and rate-limited errors in Telegram edit/delete trigger retry backoff,
// while permission and permanent errors fall back to notice messages.
func TestTelegramEditDeleteTransientRetry(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()
	chat := setupActiveChat(t, coord, st, "15559876543@s.whatsapp.net", -10012345, 10)

	// Message 1 originated on WhatsApp and forwarded to Telegram (Telegram part is BridgeOwned: true)
	createEvent1 := makeWhatsAppMessageEvent("wa-msg-retry-test-1", chat.WhatsAppJID, "15559876543@s.whatsapp.net", false, "initial text 1")
	if _, err := st.Accept(ctx, createEvent1); err != nil {
		t.Fatalf("failed to accept: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox create 1 failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery create 1 failed: %v", err)
	}

	// 1. Transient error on Telegram edit should return error for backoff retry (no immediate fallback notice)
	tgPort.mu.Lock()
	tgPort.editErr = domain.AdapterError{
		Class: domain.Transient,
		Code:  "saturated",
	}
	tgSentBefore := len(tgPort.sentRequests)
	tgPort.mu.Unlock()

	editEvent := createEvent1
	editEvent.Key = "wa-edit-transient-1"
	editEvent.Kind = domain.MessageEdit
	editEvent.Version.Order = 200
	editEvent.Version.Tie = "edit-transient"
	editEvent.Version.Digest = "digest-edit-transient"
	editEvent.Text = &domain.TextContent{Text: "transient edit text"}
	if _, err := st.Accept(ctx, editEvent); err != nil {
		t.Fatalf("failed to accept edit: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox edit failed: %v", err)
	}
	err := coord.StepDelivery(ctx)
	if err == nil {
		t.Fatal("expected StepDelivery to fail with transient error, got nil")
	}
	var adapterErr domain.AdapterError
	if !errors.As(err, &adapterErr) || adapterErr.Class != domain.Transient {
		t.Fatalf("expected transient AdapterError, got: %v", err)
	}
	tgPort.mu.Lock()
	tgSentAfter := len(tgPort.sentRequests)
	tgPort.mu.Unlock()
	if tgSentAfter != tgSentBefore {
		t.Fatalf("expected no fallback notice sent on transient error, got %d sent requests", tgSentAfter)
	}

	// Message 2 for testing delete rate limiting independently
	createEvent2 := makeWhatsAppMessageEvent("wa-msg-retry-test-2", chat.WhatsAppJID, "15559876543@s.whatsapp.net", false, "initial text 2")
	if _, err := st.Accept(ctx, createEvent2); err != nil {
		t.Fatalf("failed to accept: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox create 2 failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery create 2 failed: %v", err)
	}

	// 2. RateLimited error on Telegram delete should return error for backoff retry (no immediate fallback notice)
	tgPort.mu.Lock()
	tgPort.deleteErr = domain.AdapterError{
		Class: domain.RateLimited,
		Code:  "rate_limited",
	}
	tgSentBeforeDel := len(tgPort.sentRequests)
	tgPort.mu.Unlock()

	revokeEvent := createEvent2
	revokeEvent.Key = "wa-revoke-ratelimit-1"
	revokeEvent.Kind = domain.MessageRevoke
	revokeEvent.Version.Order = 300
	revokeEvent.Version.Tie = "revoke-ratelimit"
	revokeEvent.Version.Digest = "digest-revoke-ratelimit"
	if _, err := st.Accept(ctx, revokeEvent); err != nil {
		t.Fatalf("failed to accept revoke: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox revoke failed: %v", err)
	}
	err = coord.StepDelivery(ctx)
	if err == nil {
		t.Fatal("expected StepDelivery to fail with rate limited error, got nil")
	}
	if !errors.As(err, &adapterErr) || adapterErr.Class != domain.RateLimited {
		t.Fatalf("expected rate limited AdapterError, got: %v", err)
	}
	tgPort.mu.Lock()
	tgSentAfterDel := len(tgPort.sentRequests)
	tgPort.mu.Unlock()
	if tgSentAfterDel != tgSentBeforeDel {
		t.Fatalf("expected no fallback notice sent on rate limited error, got %d sent requests", tgSentAfterDel)
	}
}
