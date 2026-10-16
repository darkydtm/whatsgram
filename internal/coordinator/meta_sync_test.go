package coordinator

import (
	"context"
	"strings"
	"testing"
	"time"

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

	if !strings.Contains(contactCard.Content.Text, "ID: 15551234567@s.whatsapp.net") {
		t.Errorf("expected ID in card, got: %s", contactCard.Content.Text)
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

	aliceTopic, err := coord.EnsureTopic(ctx, aliceJID, "Alice")
	if err != nil {
		t.Fatal(err)
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

	if !strings.Contains(groupCard.Content.Text, "ID: "+groupJID) {
		t.Errorf("expected group ID in card, got: %s", groupCard.Content.Text)
	}
	if !strings.Contains(groupCard.Content.Text, "Название: Family") {
		t.Errorf("expected group name in card, got: %s", groupCard.Content.Text)
	}
	if !strings.Contains(groupCard.Content.Text, "Alice") || !strings.Contains(groupCard.Content.Text, "Bob") {
		t.Errorf("expected participants in card, got: %s", groupCard.Content.Text)
	}

	// Alice had an existing topic, so she should have a link. Bob had none, so no recursive topic was created.
	if len(groupCard.Content.Entities) != 1 || groupCard.Content.Entities[0].URL != aliceTopic.URL() {
		t.Fatalf("expected 1 text_link entity pointing to Alice topic, got: %+v", groupCard.Content.Entities)
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

func TestContactTopicNamingPriorityAndFieldIsolation(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	waPort.contacts = make(map[string]domain.ContactMeta)

	// 1. Contact with Name and Phone
	namedJID := "15551111111@s.whatsapp.net"
	waPort.contacts[namedJID] = domain.ContactMeta{
		JID:   namedJID,
		Name:  "Dmitry",
		Phone: "+15551111111",
	}
	namedTopic, err := coord.EnsureTopic(ctx, namedJID, "")
	if err != nil {
		t.Fatal(err)
	}
	ch1, _ := st.Chat(ctx, namedJID)
	if ch1.Title != "Dmitry" {
		t.Errorf("expected title 'Dmitry', got %q", ch1.Title)
	}

	// 2. Contact without Name, with Phone
	phoneOnlyJID := "15552222222@s.whatsapp.net"
	waPort.contacts[phoneOnlyJID] = domain.ContactMeta{
		JID:   phoneOnlyJID,
		Name:  "",
		Phone: "+15552222222",
	}
	phoneTopic, err := coord.EnsureTopic(ctx, phoneOnlyJID, "")
	if err != nil {
		t.Fatal(err)
	}
	ch2, _ := st.Chat(ctx, phoneOnlyJID)
	if ch2.Title != "+15552222222" {
		t.Errorf("expected title '+15552222222', got %q", ch2.Title)
	}

	// 3. Contact without Name and without Phone (e.g. raw JID)
	bareJID := "15553333333@lid"
	waPort.contacts[bareJID] = domain.ContactMeta{
		JID:   bareJID,
		Name:  "",
		Phone: "",
	}
	bareTopic, err := coord.EnsureTopic(ctx, bareJID, "")
	if err != nil {
		t.Fatal(err)
	}
	ch3, _ := st.Chat(ctx, bareJID)
	if ch3.Title != bareJID {
		t.Errorf("expected title %q, got %q", bareJID, ch3.Title)
	}

	// Verify meta-cards have proper fields
	for _, req := range tgPort.sentRequests {
		if req.TelegramTopic.ThreadID == phoneTopic.ThreadID {
			if !strings.Contains(req.Content.Text, "ID: "+phoneOnlyJID) {
				t.Errorf("expected ID in phone-only card, got: %s", req.Content.Text)
			}
			if !strings.Contains(req.Content.Text, "Номер: +15552222222") {
				t.Errorf("expected Phone in phone-only card, got: %s", req.Content.Text)
			}
			if !strings.Contains(req.Content.Text, "Имя: не указано") {
				t.Errorf("expected 'Имя: не указано' in card, got: %s", req.Content.Text)
			}
		}
		if req.TelegramTopic.ThreadID == bareTopic.ThreadID {
			if !strings.Contains(req.Content.Text, "ID: "+bareJID) {
				t.Errorf("expected ID in bare card, got: %s", req.Content.Text)
			}
			if !strings.Contains(req.Content.Text, "Номер: не указан") {
				t.Errorf("expected 'Номер: не указан' in bare card, got: %s", req.Content.Text)
			}
			if !strings.Contains(req.Content.Text, "Имя: не указано") {
				t.Errorf("expected 'Имя: не указано' in bare card, got: %s", req.Content.Text)
			}
		}
		if req.TelegramTopic.ThreadID == namedTopic.ThreadID {
			if !strings.Contains(req.Content.Text, "ID: "+namedJID) {
				t.Errorf("expected ID in named card, got: %s", req.Content.Text)
			}
			if !strings.Contains(req.Content.Text, "Номер: +15551111111") {
				t.Errorf("expected Phone in named card, got: %s", req.Content.Text)
			}
			if !strings.Contains(req.Content.Text, "Имя: Dmitry") {
				t.Errorf("expected Name in named card, got: %s", req.Content.Text)
			}
		}
	}
}

func TestTopicMetaCardWithAvatarPhotoAndFallback(t *testing.T) {
	coord, _, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()
	waPort.contacts = make(map[string]domain.ContactMeta)
	waPort.avatars = make(map[string][]byte)

	avatarJID := "15557777777@s.whatsapp.net"
	waPort.contacts[avatarJID] = domain.ContactMeta{
		JID:   avatarJID,
		Name:  "Avatar User",
		Phone: "+15557777777",
	}
	fakeAvatarData := []byte("fake-jpeg-photo-content")
	waPort.avatars[avatarJID] = fakeAvatarData

	avatarTopic, err := coord.EnsureTopic(ctx, avatarJID, "")
	if err != nil {
		t.Fatal(err)
	}

	lastPhoto := tgPort.LastSentPhoto()
	if lastPhoto.TelegramTopic.ThreadID != avatarTopic.ThreadID {
		t.Fatalf("expected photo sent to topic %d, got %d", avatarTopic.ThreadID, lastPhoto.TelegramTopic.ThreadID)
	}
	if string(lastPhoto.Data) != string(fakeAvatarData) {
		t.Fatalf("unexpected photo data: %s", string(lastPhoto.Data))
	}
	if !strings.Contains(lastPhoto.Caption, "👤 Справка контакта:") || !strings.Contains(lastPhoto.Caption, "Имя: Avatar User") {
		t.Fatalf("expected caption to contain meta-card, got: %s", lastPhoto.Caption)
	}

	// Now test fallback when avatar is not present
	noAvatarJID := "15558888888@s.whatsapp.net"
	waPort.contacts[noAvatarJID] = domain.ContactMeta{
		JID:   noAvatarJID,
		Name:  "No Avatar",
		Phone: "+15558888888",
	}
	noAvatarTopic, err := coord.EnsureTopic(ctx, noAvatarJID, "")
	if err != nil {
		t.Fatal(err)
	}

	var foundText bool
	for _, req := range tgPort.sentRequests {
		if req.TelegramTopic.ThreadID == noAvatarTopic.ThreadID && strings.Contains(req.Content.Text, "Имя: No Avatar") {
			foundText = true
			break
		}
	}
	if !foundText {
		t.Fatalf("expected text meta-card fallback for contact without avatar")
	}
}

func TestHistorySyncMessageChronologicalDeliveryToTelegram(t *testing.T) {
	coord, st, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	chatJID := "120363555@g.us"
	authorJID := "15559999999@s.whatsapp.net"
	waPort.groups = map[string]domain.GroupMeta{
		chatJID: {JID: chatJID, Name: "Sync Chat"},
	}
	topic, err := coord.EnsureTopic(ctx, chatJID, "Sync Chat")
	if err != nil {
		t.Fatal(err)
	}

	// 3 history messages with chronological timestamps
	t1 := time.Unix(1000, 0)
	t2 := time.Unix(2000, 0)
	t3 := time.Unix(3000, 0)

	ev1 := makeWhatsAppMessageEvent("hist-1", chatJID, authorJID, false, "first oldest message")
	ev1.OccurredAt = t1
	ev1.Version.Order = t1.UnixMilli()

	ev2 := makeWhatsAppMessageEvent("hist-2", chatJID, authorJID, false, "second middle message")
	ev2.OccurredAt = t2
	ev2.Version.Order = t2.UnixMilli()

	ev3 := makeWhatsAppMessageEvent("hist-3", chatJID, authorJID, false, "third newest message")
	ev3.OccurredAt = t3
	ev3.Version.Order = t3.UnixMilli()

	// Ingest chronologically
	if _, err := st.Accept(ctx, ev1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Accept(ctx, ev2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Accept(ctx, ev3); err != nil {
		t.Fatal(err)
	}

	// Step inbox for each
	for i := 0; i < 3; i++ {
		if err := coord.StepInbox(ctx); err != nil {
			t.Fatalf("StepInbox %d failed: %v", i, err)
		}
	}
	// Step delivery for each
	for i := 0; i < 3; i++ {
		if err := coord.StepDelivery(ctx); err != nil {
			t.Fatalf("StepDelivery %d failed: %v", i, err)
		}
	}

	// Check delivery order in tgPort
	var sentMessages []string
	for _, req := range tgPort.sentRequests {
		if req.TelegramTopic.ThreadID == topic.ThreadID && !strings.Contains(req.Content.Text, "Справка") {
			sentMessages = append(sentMessages, req.Content.Text)
		}
	}

	if len(sentMessages) != 3 {
		t.Fatalf("expected 3 delivered messages, got %d: %+v", len(sentMessages), sentMessages)
	}

	if !strings.HasPrefix(sentMessages[0], "first oldest message") {
		t.Errorf("expected first sent message to be oldest, got: %s", sentMessages[0])
	}
	if !strings.HasPrefix(sentMessages[1], "second middle message") {
		t.Errorf("expected second sent message to be middle, got: %s", sentMessages[1])
	}
	if !strings.HasPrefix(sentMessages[2], "third newest message") {
		t.Errorf("expected third sent message to be newest, got: %s", sentMessages[2])
	}
}

func TestAutomaticMetaCardUpdateOnGroupChange(t *testing.T) {
	coord, _, tgPort, waPort, _ := setupTestEnvironment(t)
	ctx := context.Background()

	chatJID := "120363777@g.us"
	waPort.groups = map[string]domain.GroupMeta{
		chatJID: {
			JID:  chatJID,
			Name: "Initial Group Name",
			Participants: []domain.ContactMeta{
				{JID: "15550000001@s.whatsapp.net", Name: "User One", Phone: "+15550000001"},
			},
		},
	}
	topic, err := coord.EnsureTopic(ctx, chatJID, "Initial Group Name")
	if err != nil {
		t.Fatal(err)
	}

	// Verify meta card sent initially
	if len(tgPort.sentRequests) == 0 {
		t.Fatal("expected initial meta card sent")
	}

	// Update group metadata
	waPort.groups[chatJID] = domain.GroupMeta{
		JID:  chatJID,
		Name: "Updated Group Name",
		Participants: []domain.ContactMeta{
			{JID: "15550000001@s.whatsapp.net", Name: "User One", Phone: "+15550000001"},
			{JID: "15550000002@s.whatsapp.net", Name: "User Two", Phone: "+15550000002"},
		},
	}

	// Call UpdateMetaCard
	if err := coord.UpdateMetaCard(ctx, chatJID); err != nil {
		t.Fatalf("UpdateMetaCard failed: %v", err)
	}

	// Verify edit occurred
	if len(tgPort.editedMetaMessages) != 1 {
		t.Fatalf("expected 1 edited meta message, got %d", len(tgPort.editedMetaMessages))
	}
	editCall := tgPort.editedMetaMessages[0]
	if editCall.Topic.ThreadID != topic.ThreadID {
		t.Errorf("expected topic %d, got %d", topic.ThreadID, editCall.Topic.ThreadID)
	}
	if !strings.Contains(editCall.Content.Text, "Updated Group Name") {
		t.Errorf("expected edited content to have 'Updated Group Name', got: %s", editCall.Content.Text)
	}
	if !strings.Contains(editCall.Content.Text, "User Two") {
		t.Errorf("expected edited content to have 'User Two', got: %s", editCall.Content.Text)
	}
}
