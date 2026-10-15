package coordinator

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestPhase5AdvancedEvents(t *testing.T) {
	coord, st, tgPort, _, _ := setupTestEnvironment(t)
	ctx := context.Background()

	// Setup a bound chat
	waJID := "120363000000000000@g.us"
	err := st.DiscoverChat(ctx, domain.ChatCandidate{
		WhatsAppJID: waJID,
		Title:       "Test Advanced Group",
		Source:      "group",
	}, -10012345)
	if err != nil {
		t.Fatalf("DiscoverChat failed: %v", err)
	}

	err = st.BindTopic(ctx, waJID, domain.TopicRef{
		ChatID:     -10012345,
		ThreadID:   100,
		Generation: 1,
	})
	if err != nil {
		t.Fatalf("BindTopic failed: %v", err)
	}

	t.Run("LocationEventForwarding", func(t *testing.T) {
		meta, _ := json.Marshal(map[string]any{
			"type":      "location",
			"latitude":  37.774929,
			"longitude": -122.419416,
			"title":     "San Francisco",
		})
		ev := makeWhatsAppMessageEvent("loc-1", waJID, "15559876543@s.whatsapp.net", false, "")
		ev.Metadata = meta

		if _, err := st.Accept(ctx, ev); err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox failed: %v", err)
		}

		if err := coord.StepDelivery(ctx); err != nil {
			t.Fatalf("StepDelivery failed: %v", err)
		}

		tgPort.mu.Lock()
		defer tgPort.mu.Unlock()
		if len(tgPort.sentRequests) == 0 {
			t.Fatal("expected location message to be delivered to Telegram")
		}
		lastReq := tgPort.sentRequests[len(tgPort.sentRequests)-1]
		if !strings.Contains(lastReq.Content.Text, "Location: 37.774929, -122.419416 (San Francisco)") {
			t.Fatalf("unexpected text for location: %q", lastReq.Content.Text)
		}
	})

	t.Run("ContactEventForwarding", func(t *testing.T) {
		meta, _ := json.Marshal(map[string]any{
			"type":  "contact",
			"name":  "Alice Smith",
			"phone": "+15551234567",
		})
		ev := makeWhatsAppMessageEvent("contact-1", waJID, "15559876543@s.whatsapp.net", false, "")
		ev.Metadata = meta

		if _, err := st.Accept(ctx, ev); err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox failed: %v", err)
		}

		if err := coord.StepDelivery(ctx); err != nil {
			t.Fatalf("StepDelivery failed: %v", err)
		}

		tgPort.mu.Lock()
		defer tgPort.mu.Unlock()
		lastReq := tgPort.sentRequests[len(tgPort.sentRequests)-1]
		if !strings.Contains(lastReq.Content.Text, "Contact: Alice Smith (+15551234567)") {
			t.Fatalf("unexpected text for contact: %q", lastReq.Content.Text)
		}
	})

	t.Run("PollEventForwarding", func(t *testing.T) {
		meta, _ := json.Marshal(map[string]any{
			"type":     "poll",
			"question": "Lunch venue?",
			"options":  []string{"Pizza", "Sushi"},
		})
		ev := makeWhatsAppMessageEvent("poll-1", waJID, "15559876543@s.whatsapp.net", false, "")
		ev.Metadata = meta

		if _, err := st.Accept(ctx, ev); err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox failed: %v", err)
		}

		if err := coord.StepDelivery(ctx); err != nil {
			t.Fatalf("StepDelivery failed: %v", err)
		}

		tgPort.mu.Lock()
		defer tgPort.mu.Unlock()
		lastReq := tgPort.sentRequests[len(tgPort.sentRequests)-1]
		if !strings.Contains(lastReq.Content.Text, "Poll: Lunch venue?") || !strings.Contains(lastReq.Content.Text, "Pizza") {
			t.Fatalf("unexpected text for poll: %q", lastReq.Content.Text)
		}
	})

	t.Run("ChatTitleRenameSync", func(t *testing.T) {
		ev := domain.Event{
			Key:        "title-1",
			Source:     domain.WhatsApp,
			Kind:       domain.ChatTitle,
			Chat:       waJID,
			Actor:      "15559876543@s.whatsapp.net",
			OccurredAt: time.Now(),
			Version: domain.SourceVersion{
				Kind:        "wa.chat",
				Order:       10,
				Tie:         "title-1",
				Digest:      "digest-title",
				StreamEpoch: "epoch1",
			},
			Text: &domain.TextContent{Text: "Renamed Group Title"},
		}

		if _, err := st.Accept(ctx, ev); err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox failed: %v", err)
		}
	})

	t.Run("TimerAndExpirationHandling", func(t *testing.T) {
		// Set chat timer to 500 milliseconds
		coord.setChatTimer(waJID, 500*time.Millisecond)

		ev := makeWhatsAppMessageEvent("ephemeral-1", waJID, "15559876543@s.whatsapp.net", false, "This message will expire")

		if _, err := st.Accept(ctx, ev); err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox failed: %v", err)
		}

		if err := coord.StepDelivery(ctx); err != nil {
			t.Fatalf("StepDelivery failed: %v", err)
		}

		// Verify timer was scheduled in store
		var count int
		err := st.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_timer WHERE state='pending'").Scan(&count)
		if err != nil || count != 1 {
			t.Fatalf("expected 1 pending timer, got %d (err: %v)", count, err)
		}

		// Wait for timer to expire
		time.Sleep(600 * time.Millisecond)

		if err := coord.StepTimer(ctx); err != nil {
			t.Fatalf("StepTimer failed: %v", err)
		}

		tgPort.mu.Lock()
		defer tgPort.mu.Unlock()
		if len(tgPort.deletedMessages) == 0 {
			t.Fatal("expected expired ephemeral message to be deleted in Telegram")
		}
	})

	t.Run("AdminCommandsTimerAndRename", func(t *testing.T) {
		// Test /timer command
		timerEv := makeTelegramMessageEvent("cmd-timer", -10012345, 42, 100, "/timer 24h")
		if _, err := st.Accept(ctx, timerEv); err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox failed: %v", err)
		}

		dur, ok := coord.getChatTimer(waJID)
		if !ok || dur != 24*time.Hour {
			t.Fatalf("expected timer 24h, got %v (ok=%v)", dur, ok)
		}

		// Test /rename command
		renameEv := makeTelegramMessageEvent("cmd-rename", -10012345, 42, 100, "/rename New Topic Name")
		if _, err := st.Accept(ctx, renameEv); err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox failed: %v", err)
		}
	})

	t.Run("AdminCommandsPinAndUnpin", func(t *testing.T) {
		pinEv := makeTelegramMessageEvent("cmd-pin", -10012345, 42, 100, "/pin")
		pinEv.Target.Chat = strconv.FormatInt(-10012345, 10)
		pinEv.Target.ID = "100"
		pinEv.Metadata, _ = json.Marshal(map[string]any{"reply_to_message_id": 100})
		if _, err := st.Accept(ctx, pinEv); err != nil {
			t.Fatalf("Accept failed: %v", err)
		}

		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox failed: %v", err)
		}
	})
}
