package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestMetaSyncDirectContactTopicAndCard(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	groupJID := "120363000@g.us"
	contactJID := "15551234567@s.whatsapp.net"

	waPort.groups = map[string]domain.GroupMeta{
		groupJID: {
			JID:  groupJID,
			Name: "Developers",
		},
	}
	waPort.contacts = map[string]domain.ContactMeta{
		contactJID: {
			JID:   contactJID,
			Name:  "Alice Smith",
			Phone: "+15551234567",
		},
	}
	waPort.commonGroups = map[string][]domain.GroupMeta{
		contactJID: {
			{JID: groupJID, Name: "Developers"},
		},
	}

	groupTopic, err := coord.EnsureTopic(ctx, groupJID, "Developers")
	if err != nil {
		t.Fatalf("EnsureTopic group failed: %v", err)
	}

	contactTopic, err := coord.EnsureTopic(ctx, contactJID, "")
	if err != nil {
		t.Fatalf("EnsureTopic contact failed: %v", err)
	}

	created := tgPort.createdTopics
	if len(created) < 2 {
		t.Fatalf("expected at least 2 created topics, got %d", len(created))
	}

	chat, err := st.Chat(ctx, contactJID)
	if err != nil {
		t.Fatalf("failed to load chat: %v", err)
	}
	if chat.Title != "Alice Smith" {
		t.Fatalf("expected chat title 'Alice Smith', got %q", chat.Title)
	}

	var contactCard *domain.SendTextRequest
	for i := range tgPort.sentRequests {
		req := &tgPort.sentRequests[i]
		if req.TelegramTopic.ThreadID == contactTopic.ThreadID && strings.Contains(req.Content.Text, "Справка контакта") {
			contactCard = req
			break
		}
	}
	if contactCard == nil {
		t.Fatalf("contact meta-card message not sent to topic %d", contactTopic.ThreadID)
	}

	if !strings.Contains(contactCard.Content.Text, "Имя: Alice Smith") {
		t.Errorf("expected contact name in card, got: %s", contactCard.Content.Text)
	}
	if !strings.Contains(contactCard.Content.Text, "Номер: +15551234567") {
		t.Errorf("expected phone in card, got: %s", contactCard.Content.Text)
	}
	if !strings.Contains(contactCard.Content.Text, "Developers") {
		t.Errorf("expected common group name in card, got: %s", contactCard.Content.Text)
	}

	expectedURL := groupTopic.URL()
	var foundLink bool
	for _, ent := range contactCard.Content.Entities {
		if ent.Kind == "text_link" && ent.URL == expectedURL {
			foundLink = true
			break
		}
	}
	if !foundLink {
		t.Fatalf("expected text_link pointing to %s, got entities: %+v", expectedURL, contactCard.Content.Entities)
	}
}

func TestMetaSyncGroupTopicAndCard(t *testing.T) {
	coord, _, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	groupJID := "120363999@g.us"
	aliceJID := "15551111111@s.whatsapp.net"
	bobJID := "15552222222@s.whatsapp.net"

	waPort.contacts = map[string]domain.ContactMeta{
		aliceJID: {JID: aliceJID, Name: "Alice", Phone: "+15551111111"},
		bobJID:   {JID: bobJID, Name: "Bob", Phone: "+15552222222"},
	}
	waPort.groups = map[string]domain.GroupMeta{
		groupJID: {
			JID:  groupJID,
			Name: "Family",
			Participants: []domain.ContactMeta{
				{JID: aliceJID, Name: "Alice", Phone: "+15551111111"},
				{JID: bobJID, Name: "Bob", Phone: "+15552222222"},
			},
		},
	}

	groupTopic, err := coord.EnsureTopic(ctx, groupJID, "Family")
	if err != nil {
		t.Fatalf("EnsureTopic group failed: %v", err)
	}

	var groupCard *domain.SendTextRequest
	for i := range tgPort.sentRequests {
		req := &tgPort.sentRequests[i]
		if req.TelegramTopic.ThreadID == groupTopic.ThreadID && strings.Contains(req.Content.Text, "Справка группы") {
			groupCard = req
			break
		}
	}
	if groupCard == nil {
		t.Fatalf("group meta-card not sent")
	}

	if !strings.Contains(groupCard.Content.Text, "Название: Family") {
		t.Errorf("expected group name in card, got: %s", groupCard.Content.Text)
	}
	if !strings.Contains(groupCard.Content.Text, "Alice") || !strings.Contains(groupCard.Content.Text, "Bob") {
		t.Errorf("expected participants in card, got: %s", groupCard.Content.Text)
	}

	if len(groupCard.Content.Entities) < 2 {
		t.Fatalf("expected at least 2 text_link entities in group card, got: %+v", groupCard.Content.Entities)
	}
}

func TestGroupMessageAppendsAuthorHyperlinkWithoutPrefix(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	groupJID := "120363888@g.us"
	authorJID := "15553333333@s.whatsapp.net"

	waPort.contacts = map[string]domain.ContactMeta{
		authorJID: {JID: authorJID, Name: "Charlie", Phone: "+15553333333"},
	}
	waPort.groups = map[string]domain.GroupMeta{
		groupJID: {JID: groupJID, Name: "Work Chat"},
	}

	groupTopic, err := coord.EnsureTopic(ctx, groupJID, "Work Chat")
	if err != nil {
		t.Fatal(err)
	}

	ev := makeWhatsAppMessageEvent("msg-100", groupJID, authorJID, false, "Hello team!")
	if _, err := st.Accept(ctx, ev); err != nil {
		t.Fatal(err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery failed: %v", err)
	}

	lastTg := tgPort.LastSentRequest()
	if lastTg.TelegramTopic.ThreadID != groupTopic.ThreadID {
		t.Fatalf("expected thread %d, got %d", groupTopic.ThreadID, lastTg.TelegramTopic.ThreadID)
	}

	if strings.Contains(lastTg.Content.Text, "[Charlie]:") || strings.Contains(lastTg.Content.Text, "[15553333333]:") {
		t.Errorf("message should not contain prefix [author]:, got: %s", lastTg.Content.Text)
	}

	expectedSuffix := "\nCharlie"
	if !strings.HasSuffix(lastTg.Content.Text, expectedSuffix) {
		t.Errorf("expected message to end with %q, got: %s", expectedSuffix, lastTg.Content.Text)
	}

	if len(lastTg.Content.Entities) != 1 || lastTg.Content.Entities[0].Kind != "text_link" {
		t.Fatalf("expected 1 text_link entity, got: %+v", lastTg.Content.Entities)
	}
}

func TestTopicAutoCreatedOnIncomingMessageWithoutPriorChat(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	senderJID := "15554444444@s.whatsapp.net"
	waPort.contacts = map[string]domain.ContactMeta{
		senderJID: {JID: senderJID, Name: "David", Phone: "+15554444444"},
	}

	ev := makeWhatsAppMessageEvent("msg-200", senderJID, senderJID, false, "Hello from new contact!")
	if _, err := st.Accept(ctx, ev); err != nil {
		t.Fatal(err)
	}

	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}
	if err := coord.StepDelivery(ctx); err != nil {
		t.Fatalf("StepDelivery failed: %v", err)
	}

	chat, err := st.Chat(ctx, senderJID)
	if err != nil || chat.Topic == nil {
		t.Fatalf("expected auto-created topic for sender, got err: %v, chat: %+v", err, chat)
	}

	if chat.Title != "David" {
		t.Errorf("expected title 'David', got %q", chat.Title)
	}

	var metaCardFound bool
	for _, req := range tgPort.sentRequests {
		if req.TelegramTopic.ThreadID == chat.Topic.ThreadID && strings.Contains(req.Content.Text, "Справка контакта") {
			metaCardFound = true
			break
		}
	}
	if !metaCardFound {
		t.Fatalf("expected meta-card sent for auto-created topic")
	}

	lastTg := tgPort.LastSentRequest()
	if lastTg.Content.Text != "Hello from new contact!" {
		t.Errorf("expected message delivered, got: %s", lastTg.Content.Text)
	}
}

func TestSyncCommandWithMetadata(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	groupJID := "120363777@g.us"
	contactJID := "15555555555@s.whatsapp.net"

	waPort.candidates = []domain.ChatCandidate{
		{WhatsAppJID: contactJID, Title: "Emma", Source: "contact"},
		{WhatsAppJID: groupJID, Title: "Project Alpha", Source: "group"},
	}
	waPort.contacts = map[string]domain.ContactMeta{
		contactJID: {JID: contactJID, Name: "Emma", Phone: "+15555555555"},
	}
	waPort.groups = map[string]domain.GroupMeta{
		groupJID: {
			JID:  groupJID,
			Name: "Project Alpha",
			Participants: []domain.ContactMeta{
				{JID: contactJID, Name: "Emma", Phone: "+15555555555"},
			},
		},
	}
	waPort.commonGroups = map[string][]domain.GroupMeta{
		contactJID: {
			{JID: groupJID, Name: "Project Alpha"},
		},
	}

	syncEvent := makeTelegramMessageEvent("sync-1", -10012345, 42, 0, "/sync")
	if _, err := st.Accept(ctx, syncEvent); err != nil {
		t.Fatalf("failed to accept sync event: %v", err)
	}
	if err := coord.StepInbox(ctx); err != nil {
		t.Fatalf("StepInbox failed: %v", err)
	}

	lastTg := tgPort.LastSentRequest()
	if !strings.Contains(lastTg.Content.Text, "Sync completed:") {
		t.Fatalf("unexpected sync reply: %s", lastTg.Content.Text)
	}

	chatGroup, err := st.Chat(ctx, groupJID)
	if err != nil || chatGroup.Topic == nil {
		t.Fatalf("group topic not synced: %v", err)
	}
	chatContact, err := st.Chat(ctx, contactJID)
	if err != nil || chatContact.Topic == nil {
		t.Fatalf("contact topic not synced: %v", err)
	}

	if chatGroup.Title != "Project Alpha" {
		t.Errorf("expected group title 'Project Alpha', got %q", chatGroup.Title)
	}
	if chatContact.Title != "Emma" {
		t.Errorf("expected contact title 'Emma', got %q", chatContact.Title)
	}
}
