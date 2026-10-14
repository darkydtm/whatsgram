package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/admin"
	"github.com/darkydtm/whatsgram/internal/config"
	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
)

type telegramEditCall struct {
	Ref     domain.MessageRef
	Content domain.TextContent
}

type telegramReactionCall struct {
	Ref      domain.MessageRef
	Reaction string
}

type fakeTelegramPort struct {
	mu              sync.Mutex
	createdTopics   []domain.TopicRef
	nextThreadID    int64
	sentRequests    []domain.SendTextRequest
	sendErr         error
	editedMessages  []telegramEditCall
	editErr         error
	deletedMessages []domain.MessageRef
	deleteErr       error
	reactions       []telegramReactionCall
	reactionErr     error
}

func (f *fakeTelegramPort) Run(ctx context.Context, sink domain.EventSink) error {
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeTelegramPort) CreateTopic(ctx context.Context, chatID int64, title string) (domain.TopicRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextThreadID++
	ref := domain.TopicRef{
		ChatID:     chatID,
		ThreadID:   f.nextThreadID,
		Generation: 0,
	}
	f.createdTopics = append(f.createdTopics, ref)
	return ref, nil
}

func (f *fakeTelegramPort) RenameTopic(ctx context.Context, topic domain.TopicRef, title string) error {
	return nil
}

func (f *fakeTelegramPort) SendText(ctx context.Context, req domain.SendTextRequest) (domain.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return domain.SendResult{}, f.sendErr
	}
	f.sentRequests = append(f.sentRequests, req)
	part := domain.DeliveredPart{
		Ref: domain.MessageRef{
			Platform: domain.Telegram,
			Chat:     strconv.FormatInt(req.TelegramTopic.ChatID, 10),
			Author:   strconv.FormatInt(req.TelegramTopic.ChatID, 10),
			ID:       strconv.FormatInt(time.Now().UnixNano(), 10),
		},
		Index:           0,
		Role:            domain.RolePrimary,
		BridgeOwned:     true,
		Capabilities:    domain.CanDelete | domain.CanRevoke | domain.CanEditText | domain.CanReact,
		TopicGeneration: req.TelegramTopic.Generation,
	}
	return domain.SendResult{
		Parts:      []domain.DeliveredPart{part},
		AcceptedAt: time.Now(),
	}, nil
}

func (f *fakeTelegramPort) EditText(ctx context.Context, ref domain.MessageRef, content domain.TextContent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.editedMessages = append(f.editedMessages, telegramEditCall{Ref: ref, Content: content})
	return nil
}

func (f *fakeTelegramPort) Delete(ctx context.Context, ref domain.MessageRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedMessages = append(f.deletedMessages, ref)
	return nil
}

func (f *fakeTelegramPort) SetOwnReaction(ctx context.Context, ref domain.MessageRef, reaction string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reactionErr != nil {
		return f.reactionErr
	}
	f.reactions = append(f.reactions, telegramReactionCall{Ref: ref, Reaction: reaction})
	return nil
}

func (f *fakeTelegramPort) LastSentRequest() domain.SendTextRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sentRequests) == 0 {
		return domain.SendTextRequest{}
	}
	return f.sentRequests[len(f.sentRequests)-1]
}

type whatsAppEditCall struct {
	Ref     domain.MessageRef
	Content domain.TextContent
	Salt    string
}

type whatsAppRevokeCall struct {
	Ref  domain.MessageRef
	Salt string
}

type whatsAppReactionCall struct {
	Ref      domain.MessageRef
	Reaction string
	Salt     string
}

type fakeWhatsAppPort struct {
	mu              sync.Mutex
	nextSendID      int64
	candidates      []domain.ChatCandidate
	sentRequests    []domain.SendTextRequest
	sendErr         error
	editedMessages  []whatsAppEditCall
	editErr         error
	revokedMessages []whatsAppRevokeCall
	revokeErr       error
	reactions       []whatsAppReactionCall
	reactionErr     error
}

func (f *fakeWhatsAppPort) Run(ctx context.Context, sink domain.EventSink) error {
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeWhatsAppPort) DiscoverChats(ctx context.Context) ([]domain.ChatCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.candidates, nil
}

func (f *fakeWhatsAppPort) AllocateSendID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSendID++
	return fmt.Sprintf("wa-send-%d", f.nextSendID)
}

func (f *fakeWhatsAppPort) SendText(ctx context.Context, req domain.SendTextRequest) (domain.SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return domain.SendResult{}, f.sendErr
	}
	f.sentRequests = append(f.sentRequests, req)
	part := domain.DeliveredPart{
		Ref: domain.MessageRef{
			Platform: domain.WhatsApp,
			Chat:     req.WhatsAppChat,
			Author:   req.WhatsAppChat,
			ID:       req.WhatsAppSendID,
		},
		Index:           0,
		Role:            domain.RolePrimary,
		BridgeOwned:     true,
		Capabilities:    domain.CanDelete | domain.CanRevoke | domain.CanEditText | domain.CanReact,
		TopicGeneration: 1,
	}
	return domain.SendResult{
		Parts:      []domain.DeliveredPart{part},
		AcceptedAt: time.Now(),
	}, nil
}

func (f *fakeWhatsAppPort) EditOwnText(ctx context.Context, ref domain.MessageRef, content domain.TextContent, salt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.editedMessages = append(f.editedMessages, whatsAppEditCall{Ref: ref, Content: content, Salt: salt})
	return nil
}

func (f *fakeWhatsAppPort) Revoke(ctx context.Context, ref domain.MessageRef, salt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revokedMessages = append(f.revokedMessages, whatsAppRevokeCall{Ref: ref, Salt: salt})
	return nil
}

func (f *fakeWhatsAppPort) SetOwnReaction(ctx context.Context, ref domain.MessageRef, reaction, salt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reactionErr != nil {
		return f.reactionErr
	}
	f.reactions = append(f.reactions, whatsAppReactionCall{Ref: ref, Reaction: reaction, Salt: salt})
	return nil
}

func (f *fakeWhatsAppPort) LastSentRequest() domain.SendTextRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sentRequests) == 0 {
		return domain.SendTextRequest{}
	}
	return f.sentRequests[len(f.sentRequests)-1]
}

type fakePairingPort struct {
	mu       sync.Mutex
	qrChan   chan domain.PairingNotice
	codeResp domain.PairingNotice
	codeErr  error
	qrErr    error
}

func (f *fakePairingPort) BeginQR(ctx context.Context) (<-chan domain.PairingNotice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.qrErr != nil {
		return nil, f.qrErr
	}
	ch := make(chan domain.PairingNotice, 1)
	if f.qrChan != nil {
		go func() {
			for n := range f.qrChan {
				ch <- n
			}
			close(ch)
		}()
	} else {
		ch <- domain.PairingNotice{
			Kind:      "qr",
			Secret:    domain.NewPairingSecret("test-qr-secret-xyz"),
			ExpiresAt: time.Now().Add(time.Minute),
		}
	}
	return ch, nil
}

func (f *fakePairingPort) BeginPhoneCode(ctx context.Context, phone string) (domain.PairingNotice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.codeErr != nil {
		return domain.PairingNotice{}, f.codeErr
	}
	if f.codeResp.Kind != "" {
		return f.codeResp, nil
	}
	return domain.PairingNotice{
		Kind:      "code",
		Secret:    domain.NewPairingSecret("1234-5678"),
		ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

func setupTestEnvironment(t *testing.T) (*Coordinator, *store.Store, *fakeTelegramPort, *fakeWhatsAppPort, *fakePairingPort) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), dbPath, 2*time.Second)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.Default()
	cfg.Telegram.ChatID = -10012345
	cfg.Telegram.AllowedUserIDs = []int64{42, 43}
	cfg.WhatsApp.Phone = "15551234567"

	policy, err := admin.NewPolicy(cfg.Telegram.ChatID, cfg.Telegram.AllowedUserIDs, "TestBot")
	if err != nil {
		t.Fatalf("failed to create admin policy: %v", err)
	}

	tgPort := &fakeTelegramPort{}
	waPort := &fakeWhatsAppPort{}
	pairingPort := &fakePairingPort{}

	coord, err := New(cfg, st, tgPort, waPort, pairingPort, policy,
		WithPollInterval(10*time.Millisecond),
		WithLeaseDuration(10*time.Second),
	)
	if err != nil {
		t.Fatalf("failed to create coordinator: %v", err)
	}

	return coord, st, tgPort, waPort, pairingPort
}

func makeTelegramMessageEvent(key string, chatID, actorID, threadID int64, text string) domain.Event {
	return domain.Event{
		Key:              key,
		Source:           domain.Telegram,
		Kind:             domain.MessageCreate,
		Chat:             strconv.FormatInt(chatID, 10),
		Actor:            strconv.FormatInt(actorID, 10),
		TelegramThreadID: threadID,
		Target: &domain.MessageRef{
			Platform: domain.Telegram,
			Chat:     strconv.FormatInt(chatID, 10),
			Author:   strconv.FormatInt(actorID, 10),
			ID:       key,
		},
		Version: domain.SourceVersion{
			Kind:        "telegram.message",
			Order:       100,
			Tie:         key,
			Digest:      "digest-" + key,
			StreamEpoch: "epoch1",
		},
		Text:       &domain.TextContent{Text: text},
		OccurredAt: time.Now(),
	}
}

func makeWhatsAppMessageEvent(key, chatJID, authorJID string, fromMe bool, text string) domain.Event {
	envelope := struct {
		ID        string `json:"id"`
		Chat      string `json:"chat"`
		Sender    string `json:"sender"`
		FromMe    bool   `json:"from_me"`
		Timestamp int64  `json:"timestamp"`
	}{
		ID:        key,
		Chat:      chatJID,
		Sender:    authorJID,
		FromMe:    fromMe,
		Timestamp: time.Now().UnixMilli(),
	}
	raw, _ := json.Marshal(envelope)

	return domain.Event{
		Key:    chatJID + "/" + authorJID + "/" + key,
		Source: domain.WhatsApp,
		Kind:   domain.MessageCreate,
		Chat:   chatJID,
		Actor:  authorJID,
		Target: &domain.MessageRef{
			Platform: domain.WhatsApp,
			Chat:     chatJID,
			Author:   authorJID,
			ID:       key,
		},
		Version: domain.SourceVersion{
			Kind:        "wa.message",
			Order:       100,
			Tie:         key,
			Digest:      "digest-" + key,
			StreamEpoch: "epoch1",
		},
		Text:       &domain.TextContent{Text: text},
		Raw:        raw,
		OccurredAt: time.Now(),
	}
}

func TestAdminCommandsHelpAndStatus(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()

	helpEvent := makeTelegramMessageEvent("1", -10012345, 42, 0, "/help")
	if _, err := st.Accept(ctx, helpEvent); err != nil {
		t.Fatalf("failed to accept help event: %v", err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	lastTg := tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "Available commands") {
		t.Fatalf("expected help reply, got: %s", lastTg.Content.Text)
	}

	statusEvent := makeTelegramMessageEvent("2", -10012345, 42, 0, "/status")
	if _, err := st.Accept(ctx, statusEvent); err != nil {
		t.Fatalf("failed to accept status event: %v", err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	lastTg = tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "WhatsGram Bridge Status") {
		t.Fatalf("expected status reply, got: %s", lastTg.Content.Text)
	}
}

func TestAdminCommandsPairing(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()

	qrEvent := makeTelegramMessageEvent("10", -10012345, 42, 0, "/pair")
	if _, err := st.Accept(ctx, qrEvent); err != nil {
		t.Fatalf("failed to accept pair qr event: %v", err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	lastTg := tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "WhatsApp QR Pairing") {
		t.Fatalf("expected QR pairing reply, got: %s", lastTg.Content.Text)
	}

	codeEvent := makeTelegramMessageEvent("11", -10012345, 42, 0, "/pair 15551234567")
	if _, err := st.Accept(ctx, codeEvent); err != nil {
		t.Fatalf("failed to accept pair code event: %v", err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	lastTg = tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "WhatsApp Pairing Code") {
		t.Fatalf("expected code pairing reply, got: %s", lastTg.Content.Text)
	}
}

func TestAdminCommandsSyncAndBind(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	waPort.candidates = []domain.ChatCandidate{
		{
			WhatsAppJID: "15550001111@s.whatsapp.net",
			Title:       "Alice Chat",
			Source:      "discovery",
		},
		{
			WhatsAppJID: "1234567890-9876543210@g.us",
			Title:       "Developers Group",
			Source:      "discovery",
		},
	}

	syncEvent := makeTelegramMessageEvent("20", -10012345, 42, 0, "/sync")
	if _, err := st.Accept(ctx, syncEvent); err != nil {
		t.Fatalf("failed to accept sync event: %v", err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	lastTg := tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "Sync completed: 2 synced") {
		t.Fatalf("expected sync summary reply, got: %s", lastTg.Content.Text)
	}

	chat1, err := st.Chat(ctx, "15550001111@s.whatsapp.net")
	if err != nil || chat1.Topic == nil || chat1.State != store.ChatActive {
		t.Fatalf("expected active bound chat, got: %+v, err: %v", chat1, err)
	}
	if chat1.Generation < 1 || chat1.Topic.Generation < 1 {
		t.Fatalf("expected generation >= 1, got chat gen %d, topic gen %d", chat1.Generation, chat1.Topic.Generation)
	}

	bindEvent := makeTelegramMessageEvent("21", -10012345, 42, 0, "/bind 55 15559998888@s.whatsapp.net")
	if _, err := st.Accept(ctx, bindEvent); err != nil {
		t.Fatalf("failed to accept bind event: %v", err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	lastTg = tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "Successfully bound topic 55") {
		t.Fatalf("expected bind reply, got: %s", lastTg.Content.Text)
	}

	chat2, err := st.Chat(ctx, "15559998888@s.whatsapp.net")
	if err != nil || chat2.Topic == nil || chat2.Topic.ThreadID != 55 {
		t.Fatalf("expected bound topic 55, got: %+v, err: %v", chat2, err)
	}
}

func TestForwardTelegramToWhatsApp(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	waJID := "15550002222@s.whatsapp.net"
	cand := domain.ChatCandidate{WhatsAppJID: waJID, Title: "Bob", Source: "test"}
	if err := st.DiscoverChat(ctx, cand, -10012345); err != nil {
		t.Fatal(err)
	}
	topicRef := domain.TopicRef{ChatID: -10012345, ThreadID: 15, Generation: 1}
	if err := st.BindTopic(ctx, waJID, topicRef); err != nil {
		t.Fatal(err)
	}

	msgEvent := makeTelegramMessageEvent("30", -10012345, 42, 15, "Hello from Telegram!")
	if _, err := st.Accept(ctx, msgEvent); err != nil {
		t.Fatal(err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery failed: %v", err)
	}

	lastWa := waPort.LastSentRequest()
	if lastWa.WhatsAppChat != waJID {
		t.Fatalf("expected WhatsApp chat %s, got: %s", waJID, lastWa.WhatsAppChat)
	}
	if lastWa.Content.Text != "Hello from Telegram!" {
		t.Fatalf("expected text 'Hello from Telegram!', got: %s", lastWa.Content.Text)
	}
	if lastWa.WhatsAppSendID == "" {
		t.Fatalf("expected allocated send ID, got empty")
	}
}

func TestForwardWhatsAppToTelegramDirectAndGroup(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()

	directJID := "15550003333@s.whatsapp.net"
	groupJID := "1234567890-1111111111@g.us"

	if err := st.DiscoverChat(ctx, domain.ChatCandidate{WhatsAppJID: directJID, Title: "Direct", Source: "test"}, -10012345); err != nil {
		t.Fatal(err)
	}
	if err := st.BindTopic(ctx, directJID, domain.TopicRef{ChatID: -10012345, ThreadID: 21, Generation: 1}); err != nil {
		t.Fatal(err)
	}

	if err := st.DiscoverChat(ctx, domain.ChatCandidate{WhatsAppJID: groupJID, Title: "Group", Source: "test"}, -10012345); err != nil {
		t.Fatal(err)
	}
	if err := st.BindTopic(ctx, groupJID, domain.TopicRef{ChatID: -10012345, ThreadID: 22, Generation: 1}); err != nil {
		t.Fatal(err)
	}

	directMsg := makeWhatsAppMessageEvent("40", directJID, directJID, false, "Direct msg")
	if _, err := st.Accept(ctx, directMsg); err != nil {
		t.Fatal(err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox direct failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery direct failed: %v", err)
	}

	lastTg := tgPort.LastSentRequest()
	if lastTg.TelegramTopic.ThreadID != 21 {
		t.Fatalf("expected thread 21, got: %d", lastTg.TelegramTopic.ThreadID)
	}
	if lastTg.Content.Text != "Direct msg" {
		t.Fatalf("expected text 'Direct msg', got: %s", lastTg.Content.Text)
	}

	groupMsg := makeWhatsAppMessageEvent("41", groupJID, "15559876543@s.whatsapp.net", false, "Group msg")
	if _, err := st.Accept(ctx, groupMsg); err != nil {
		t.Fatal(err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox group failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery group failed: %v", err)
	}

	lastTg = tgPort.LastSentRequest()
	if lastTg.TelegramTopic.ThreadID != 22 {
		t.Fatalf("expected thread 22, got: %d", lastTg.TelegramTopic.ThreadID)
	}
	if lastTg.Content.Text != "[15559876543]: Group msg" {
		t.Fatalf("expected formatted text '[15559876543]: Group msg', got: %s", lastTg.Content.Text)
	}
}

func TestEchoProtectionBridgeSuppressionAndOwnerDevice(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()

	waJID := "15550004444@s.whatsapp.net"
	if err := st.DiscoverChat(ctx, domain.ChatCandidate{WhatsAppJID: waJID, Title: "EchoTest", Source: "test"}, -10012345); err != nil {
		t.Fatal(err)
	}
	if err := st.BindTopic(ctx, waJID, domain.TopicRef{ChatID: -10012345, ThreadID: 33, Generation: 1}); err != nil {
		t.Fatal(err)
	}

	coord.recordBridgeSend("wa-bridge-send-999")

	echoEvent := makeWhatsAppMessageEvent("wa-bridge-send-999", waJID, "15551234567@s.whatsapp.net", true, "Echoed bridge text")
	if _, err := st.Accept(ctx, echoEvent); err != nil {
		t.Fatal(err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox echo failed: %v", err)
	}

	err := coord.StepDelivery(ctx)
	if !errors.Is(err, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork for delivery because echo was suppressed, got: %v", err)
	}

	ownerEvent := makeWhatsAppMessageEvent("owner-device-send-100", waJID, "15551234567@s.whatsapp.net", true, "Sent from phone")
	if _, err := st.Accept(ctx, ownerEvent); err != nil {
		t.Fatal(err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox owner failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery owner failed: %v", err)
	}

	lastTg := tgPort.LastSentRequest()
	if lastTg.Content.Text != "You: Sent from phone" {
		t.Fatalf("expected 'You: Sent from phone', got: %s", lastTg.Content.Text)
	}
}

func TestDeliveryErrorHandling(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()

	waJID := "15550005555@s.whatsapp.net"
	if err := st.DiscoverChat(ctx, domain.ChatCandidate{WhatsAppJID: waJID, Title: "ErrTest", Source: "test"}, -10012345); err != nil {
		t.Fatal(err)
	}
	if err := st.BindTopic(ctx, waJID, domain.TopicRef{ChatID: -10012345, ThreadID: 44, Generation: 1}); err != nil {
		t.Fatal(err)
	}

	msgEvent := makeWhatsAppMessageEvent("50", waJID, waJID, false, "Transient failure text")
	if _, err := st.Accept(ctx, msgEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}

	tgPort.sendErr = domain.AdapterError{
		Class:      domain.Transient,
		Code:       "rate_limited",
		RetryAfter: 2 * time.Second,
	}

	err := coord.StepDelivery(ctx)
	if err == nil {
		t.Fatal("expected delivery error")
	}

	claimErr := coord.StepDelivery(ctx)
	if !errors.Is(claimErr, store.ErrNoWork) {
		t.Fatalf("expected ErrNoWork because failed delivery is uncertain or pending with backoff, got: %v", claimErr)
	}
}

func TestCoordinatorLifecycleShutdown(t *testing.T) {
	coord, _, _, _, _ := setupTestEnvironment(t)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- coord.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("expected nil or context.Canceled on shutdown, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator failed to stop within timeout")
	}
}

func TestCoordinatorRunChildLoopError(t *testing.T) {
	coord, _, _, _, _ := setupTestEnvironment(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := coord.Run(ctx)
	if err == nil {
		t.Fatal("expected deadline exceeded error from Run, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}
}

func TestCrossPlatformReplyMapping(t *testing.T) {
	coord, st, _, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	waJID := "15550007777@s.whatsapp.net"
	cand := domain.ChatCandidate{WhatsAppJID: waJID, Title: "Alice", Source: "test"}
	if err := st.DiscoverChat(ctx, cand, -10012345); err != nil {
		t.Fatal(err)
	}
	topicRef := domain.TopicRef{ChatID: -10012345, ThreadID: 25, Generation: 1}
	if err := st.BindTopic(ctx, waJID, topicRef); err != nil {
		t.Fatal(err)
	}

	waMsgEvent := makeWhatsAppMessageEvent("wa-msg-1", waJID, waJID, false, "Original WA message")
	if _, err := st.Accept(ctx, waMsgEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatal(err)
	}

	parts, err := st.FindParts(ctx, *waMsgEvent.Target)
	if err != nil || len(parts) < 2 {
		t.Fatalf("expected at least 2 parts, got %d, err: %v", len(parts), err)
	}
	var tgPart domain.DeliveredPart
	for _, p := range parts {
		if p.Ref.Platform == domain.Telegram {
			tgPart = p
			break
		}
	}
	if tgPart.Ref.Platform != domain.Telegram {
		t.Fatal("failed to find delivered Telegram part")
	}

	tgReplyEvent := makeTelegramMessageEvent("tg-reply-1", -10012345, 42, 25, "Reply from Telegram")
	tgReplyEvent.Text.ReplyTo = &tgPart.Ref
	if _, err := st.Accept(ctx, tgReplyEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	lastWa := waPort.LastSentRequest()
	if lastWa.Content.ReplyTo == nil {
		t.Fatal("expected mapped ReplyTo on WhatsApp delivery, got nil")
	}
	if lastWa.Content.ReplyTo.Platform != domain.WhatsApp || lastWa.Content.ReplyTo.ID != "wa-msg-1" {
		t.Fatalf("expected WhatsApp ReplyTo with ID wa-msg-1, got: %+v", lastWa.Content.ReplyTo)
	}

	tgUnmappedEvent := makeTelegramMessageEvent("tg-reply-2", -10012345, 42, 25, "Unmapped reply")
	tgUnmappedEvent.Text.ReplyTo = &domain.MessageRef{
		Platform: domain.Telegram,
		Chat:     "-10012345",
		Author:   "42",
		ID:       "9999999",
	}
	if _, err := st.Accept(ctx, tgUnmappedEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	lastWa = waPort.LastSentRequest()
	if lastWa.Content.ReplyTo != nil {
		t.Fatalf("expected nil ReplyTo for unmapped target, got: %+v", lastWa.Content.ReplyTo)
	}
}

func TestCrossPlatformReplyMappingReverse(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()

	waJID := "15550008888@s.whatsapp.net"
	cand := domain.ChatCandidate{WhatsAppJID: waJID, Title: "Bob", Source: "test"}
	if err := st.DiscoverChat(ctx, cand, -10012345); err != nil {
		t.Fatal(err)
	}
	topicRef := domain.TopicRef{ChatID: -10012345, ThreadID: 35, Generation: 1}
	if err := st.BindTopic(ctx, waJID, topicRef); err != nil {
		t.Fatal(err)
	}

	tgMsgEvent := makeTelegramMessageEvent("tg-msg-origin", -10012345, 42, 35, "Origin message from Telegram")
	if _, err := st.Accept(ctx, tgMsgEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatal(err)
	}

	parts, err := st.FindParts(ctx, *tgMsgEvent.Target)
	if err != nil || len(parts) < 2 {
		t.Fatalf("expected at least 2 parts, got %d, err: %v", len(parts), err)
	}
	var waPart domain.DeliveredPart
	for _, p := range parts {
		if p.Ref.Platform == domain.WhatsApp {
			waPart = p
			break
		}
	}
	if waPart.Ref.Platform != domain.WhatsApp {
		t.Fatal("failed to find delivered WhatsApp part")
	}

	waReplyEvent := makeWhatsAppMessageEvent("wa-reply-1", waJID, waJID, false, "Reply from WhatsApp")
	waReplyEvent.Text.ReplyTo = &waPart.Ref
	if _, err := st.Accept(ctx, waReplyEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	lastTg := tgPort.LastSentRequest()
	if lastTg.Content.ReplyTo == nil {
		t.Fatal("expected mapped ReplyTo on Telegram delivery, got nil")
	}
	if lastTg.Content.ReplyTo.Platform != domain.Telegram || lastTg.Content.ReplyTo.ID != "tg-msg-origin" {
		t.Fatalf("expected Telegram ReplyTo with ID tg-msg-origin, got: %+v", lastTg.Content.ReplyTo)
	}

	waUnmappedEvent := makeWhatsAppMessageEvent("wa-reply-2", waJID, waJID, false, "Unmapped WA reply")
	waUnmappedEvent.Text.ReplyTo = &domain.MessageRef{
		Platform: domain.WhatsApp,
		Chat:     waJID,
		Author:   waJID,
		ID:       "unknown-wa-id",
	}
	if _, err := st.Accept(ctx, waUnmappedEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	lastTg = tgPort.LastSentRequest()
	if lastTg.Content.ReplyTo != nil {
		t.Fatalf("expected nil ReplyTo for unmapped target, got: %+v", lastTg.Content.ReplyTo)
	}
}

func TestRetryBackoffClampedToRuntimeMax(t *testing.T) {
	coord, st, _, _, _ := setupTestEnvironment(t)
	coord.cfg.Runtime.RetryMax = 3 * time.Second
	ctx := context.Background()

	waJID := "15550009999@s.whatsapp.net"
	cand := domain.ChatCandidate{WhatsAppJID: waJID, Title: "Clamped", Source: "test"}
	if err := st.DiscoverChat(ctx, cand, -10012345); err != nil {
		t.Fatal(err)
	}
	topicRef := domain.TopicRef{ChatID: -10012345, ThreadID: 55, Generation: 1}
	if err := st.BindTopic(ctx, waJID, topicRef); err != nil {
		t.Fatal(err)
	}

	msgEvent := makeWhatsAppMessageEvent("clamp-1", waJID, waJID, false, "Hello clamp")
	if _, err := st.Accept(ctx, msgEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	delivery, err := st.ClaimDelivery(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	adapterErr := domain.AdapterError{
		Class:      domain.Transient,
		RetryAfter: 30 * time.Second,
	}
	if err := coord.handleDeliveryError(ctx, delivery, adapterErr, false, now); err != nil {
		t.Fatal(err)
	}

	claimTime := now.Add(4 * time.Second)
	redelivery, err := st.ClaimDelivery(ctx, claimTime, time.Minute)
	if err != nil {
		t.Fatalf("expected delivery to be claimable after clamped RetryMax (3s), got error: %v", err)
	}
	if redelivery.ID != delivery.ID {
		t.Fatalf("expected delivery ID %d, got %d", delivery.ID, redelivery.ID)
	}
}

func TestBotAddressedCommands(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()

	qrEvent := makeTelegramMessageEvent("100", -10012345, 42, 0, "/pair@TestBot")
	if _, err := st.Accept(ctx, qrEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	lastTg := tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "WhatsApp QR Pairing") {
		t.Fatalf("expected QR pairing reply for /pair@TestBot, got: %s", lastTg.Content.Text)
	}

	codeEvent := makeTelegramMessageEvent("101", -10012345, 42, 0, "/pair@TestBot 15551234567")
	if _, err := st.Accept(ctx, codeEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	lastTg = tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "WhatsApp Pairing Code") {
		t.Fatalf("expected code pairing reply for /pair@TestBot <phone>, got: %s", lastTg.Content.Text)
	}

	bindEvent := makeTelegramMessageEvent("102", -10012345, 42, 0, "/bind@TestBot 60 15550003333@s.whatsapp.net")
	if _, err := st.Accept(ctx, bindEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	lastTg = tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "Successfully bound topic 60") {
		t.Fatalf("expected bind reply for /bind@TestBot, got: %s", lastTg.Content.Text)
	}

	sentCountBefore := len(tgPort.sentRequests)
	otherBotEvent := makeTelegramMessageEvent("103", -10012345, 42, 0, "/pair@OtherBot")
	if _, err := st.Accept(ctx, otherBotEvent); err != nil {
		t.Fatal(err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tgPort.sentRequests) != sentCountBefore {
		t.Fatalf("expected command addressed to other bot to be ignored, but got response: %s", tgPort.LastSentRequest().Content.Text)
	}
}
