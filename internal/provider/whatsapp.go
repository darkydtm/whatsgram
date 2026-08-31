package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/darky/whatsgram/internal/store"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type WhatsApp struct {
	Client    *whatsmeow.Client
	Container *sqlstore.Container
}

func NewWhatsApp(ctx context.Context, databaseURL string, inbox *store.Store) (WhatsApp, error) {
	container, err := sqlstore.New(ctx, "pgx", databaseURL, waLog.Stdout("whatsmeow", "ERROR", false))
	if err != nil {
		return WhatsApp{}, err
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return WhatsApp{}, err
	}
	client := whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "ERROR", false))
	client.AddEventHandler(func(evt any) {
		switch event := evt.(type) {
		case *events.Message:
			if event.Info.IsFromMe {
				return
			}
			log.Printf("WhatsApp message: chat=%s id=%s type=%s ephemeral=%t", event.Info.Chat, event.Info.ID, event.Info.MediaType, event.IsEphemeral)
			chatName := ""
			if event.Info.IsGroup {
				if group, groupErr := client.GetGroupInfo(ctx, event.Info.Chat); groupErr == nil {
					chatName = group.Name
				}
			}
			payload, err := jsonMessage(event)
			if err == nil && chatName != "" {
				var data map[string]any
				if err = json.Unmarshal(payload, &data); err == nil {
					data["chat_name"] = chatName
					payload, err = json.Marshal(data)
				}
			}
			if err == nil {
				err = inbox.PutInbox(ctx, "whatsapp", string(event.Info.ID), payload)
			}
			if err != nil {
				log.Printf("whatsapp event: %v", err)
			}
		case *events.HistorySync:
			if event.Data == nil {
				return
			}
			for _, conversation := range event.Data.GetConversations() {
				chat, err := parseJID(conversation.GetID())
				if err != nil {
					log.Printf("whatsapp history chat: %v", err)
					continue
				}
				items := append([]*waHistorySync.HistorySyncMsg(nil), conversation.GetMessages()...)
				sort.SliceStable(items, func(i, j int) bool {
					if items[i].GetMessage() == nil || items[j].GetMessage() == nil {
						return items[i].GetMessage() != nil
					}
					return items[i].GetMessage().GetMessageTimestamp() < items[j].GetMessage().GetMessageTimestamp()
				})
				for _, item := range items {
					if item.GetMessage() == nil {
						continue
					}
					historyEvent, err := client.ParseWebMessage(chat, item.GetMessage())
					if err != nil || historyEvent.Info.IsFromMe {
						continue
					}
					if historyEvent.Info.IsGroup {
						chatName := conversation.GetDisplayName()
						if chatName == "" {
							chatName = conversation.GetName()
						}
						payload, err := jsonMessage(historyEvent)
						if err == nil && chatName != "" {
							var data map[string]any
							if err = json.Unmarshal(payload, &data); err == nil {
								data["chat_name"] = chatName
								payload, err = json.Marshal(data)
							}
						}
						if err == nil {
							err = inbox.PutInbox(ctx, "whatsapp", string(historyEvent.Info.ID), payload)
						}
						if err != nil {
							log.Printf("whatsapp history event: %v", err)
						}
						continue
					}
					payload, err := jsonMessage(historyEvent)
					if err == nil {
						err = inbox.PutInbox(ctx, "whatsapp", string(historyEvent.Info.ID), payload)
					}
					if err != nil {
						log.Printf("whatsapp history event: %v", err)
					}
				}
			}
		case *events.Receipt:
			for _, id := range event.MessageIDs {
				payload := []byte(fmt.Sprintf(`{"id":%q,"status":%q}`, id, event.Type))
				if err := inbox.PutInbox(ctx, "whatsapp", string(id)+":status", payload); err != nil {
					log.Printf("whatsapp receipt: %v", err)
				}
			}
		case *events.Connected:
			log.Printf("WhatsApp connected as %s", device.ID)
		case *events.LoggedOut:
			log.Printf("WhatsApp logged out: %s", event.Reason)
		case *events.Disconnected:
			log.Printf("WhatsApp disconnected")
		}
	})
	return WhatsApp{Client: client, Container: container}, nil
}

func (w WhatsApp) Start(ctx context.Context) error {
	if w.Client.Store.ID == nil {
		qr, err := w.Client.GetQRChannel(ctx)
		if err != nil {
			return err
		}
		go func() {
			for item := range qr {
				if item.Event == "error" {
					log.Printf("WhatsApp pairing failed: %v", item.Error)
					continue
				}
				if item.Code != "" {
					log.Println("WhatsApp QR:")
					qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
				}
			}
		}()
		return w.Client.ConnectContext(ctx)
	}
	return w.Client.ConnectContext(ctx)
}

func (w WhatsApp) Close() error {
	if w.Client != nil {
		w.Client.Disconnect()
	}
	if w.Container != nil {
		return w.Container.Close()
	}
	return nil
}

func parseJID(value string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(value))
	if err != nil || jid.IsEmpty() || jid.User == "" || (jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer && jid.Server != types.GroupServer) {
		if err == nil {
			err = fmt.Errorf("empty JID")
		}
		return types.EmptyJID, fmt.Errorf("invalid WhatsApp JID %q: %w", value, err)
	}
	return jid, nil
}

func chatKey(jid types.JID) string {
	return types.NewJID(jid.User, jid.Server).String()
}

func (w WhatsApp) SendText(ctx context.Context, recipient, body string) (string, error) {
	jid, err := parseJID(recipient)
	if err != nil {
		return "", err
	}
	response, err := w.Client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(body)})
	return string(response.ID), err
}

func (w WhatsApp) SendMedia(ctx context.Context, recipient, mediaType string, content io.Reader, caption string) (string, error) {
	jid, err := parseJID(recipient)
	if err != nil {
		return "", err
	}
	appInfo, ok := map[string]whatsmeow.MediaType{
		"image": whatsmeow.MediaImage, "document": whatsmeow.MediaDocument,
		"video": whatsmeow.MediaVideo, "audio": whatsmeow.MediaAudio,
	}[strings.ToLower(mediaType)]
	if !ok {
		return "", fmt.Errorf("unsupported WhatsApp media type %q", mediaType)
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return "", err
	}
	upload, err := w.Client.Upload(ctx, data, appInfo)
	if err != nil {
		return "", err
	}
	message := &waE2E.Message{}
	switch strings.ToLower(mediaType) {
	case "image":
		message.ImageMessage = &waE2E.ImageMessage{URL: &upload.URL, DirectPath: &upload.DirectPath, MediaKey: upload.MediaKey, FileSHA256: upload.FileSHA256, FileEncSHA256: upload.FileEncSHA256, FileLength: &upload.FileLength, Caption: proto.String(caption)}
	case "document":
		message.DocumentMessage = &waE2E.DocumentMessage{URL: &upload.URL, DirectPath: &upload.DirectPath, MediaKey: upload.MediaKey, FileSHA256: upload.FileSHA256, FileEncSHA256: upload.FileEncSHA256, FileLength: &upload.FileLength, Caption: proto.String(caption)}
	case "video":
		message.VideoMessage = &waE2E.VideoMessage{URL: &upload.URL, DirectPath: &upload.DirectPath, MediaKey: upload.MediaKey, FileSHA256: upload.FileSHA256, FileEncSHA256: upload.FileEncSHA256, FileLength: &upload.FileLength, Caption: proto.String(caption)}
	case "audio":
		message.AudioMessage = &waE2E.AudioMessage{URL: &upload.URL, DirectPath: &upload.DirectPath, MediaKey: upload.MediaKey, FileSHA256: upload.FileSHA256, FileEncSHA256: upload.FileEncSHA256, FileLength: &upload.FileLength}
	}
	response, err := w.Client.SendMessage(ctx, jid, message)
	return string(response.ID), err
}

func (w WhatsApp) EditText(ctx context.Context, recipient, messageID, body string) error {
	jid, err := parseJID(recipient)
	if err != nil {
		return err
	}
	_, err = w.Client.SendMessage(ctx, jid, w.Client.BuildEdit(jid, types.MessageID(messageID), &waE2E.Message{Conversation: proto.String(body)}))
	return err
}

func (w WhatsApp) RequestHistory(ctx context.Context, recipient, messageID string, count int) error {
	jid, err := parseJID(recipient)
	if err != nil {
		return err
	}
	_, err = w.Client.SendPeerMessage(ctx, w.Client.BuildHistorySyncRequest(&types.MessageInfo{
		MessageSource: types.MessageSource{Chat: jid},
		ID:            types.MessageID(messageID),
	}, count))
	return err
}

func jsonMessage(event *events.Message) ([]byte, error) {
	if event.RawMessage != nil {
		event = event.UnwrapRaw()
	}
	body := messageBody(event.Message)
	if strings.TrimSpace(body) == "" {
		body = mediaCaption(event.Message)
	}
	if strings.TrimSpace(body) == "" {
		body = "[WHATSAPP MESSAGE]"
	}
	name := event.Info.PushName
	if name == "" {
		name = event.Info.Sender.User
	}
	action, targetID, reaction := messageAction(event)
	return json.Marshal(struct {
		From     string `json:"from"`
		Name     string `json:"name"`
		ChatName string `json:"chat_name,omitempty"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Body     string `json:"body"`
		MediaID  string `json:"media_id,omitempty"`
		Caption  string `json:"caption,omitempty"`
		Action   string `json:"action,omitempty"`
		TargetID string `json:"target_id,omitempty"`
		Reaction string `json:"reaction,omitempty"`
	}{
		From: chatKey(event.Info.Chat), Name: name, ID: string(event.Info.ID),
		Type: event.Info.MediaType, Body: body, MediaID: string(event.Info.ID),
		Caption: mediaCaption(event.Message), Action: action, TargetID: targetID, Reaction: reaction,
	})
}

func messageAction(event *events.Message) (string, string, string) {
	if event == nil || event.Message == nil {
		return "", "", ""
	}
	if reaction := event.Message.GetReactionMessage(); reaction != nil {
		if reaction.GetKey() == nil {
			return "", "", ""
		}
		return "reaction", reaction.GetKey().GetID(), reaction.GetText()
	}
	if event.Info.Edit == types.EditAttributeMessageEdit {
		contextInfo := event.Message.GetMessageContextInfo()
		if contextInfo == nil || contextInfo.GetMessageAssociation() == nil || contextInfo.GetMessageAssociation().GetParentMessageKey() == nil {
			return "", "", ""
		}
		return "edit", contextInfo.GetMessageAssociation().GetParentMessageKey().GetID(), ""
	}
	if event.Info.Edit == types.EditAttributeSenderRevoke || event.Info.Edit == types.EditAttributeAdminRevoke {
		protocol := event.Message.GetProtocolMessage()
		if protocol == nil || protocol.GetKey() == nil {
			return "", "", ""
		}
		return "delete", protocol.GetKey().GetID(), ""
	}
	return "", "", ""
}

func messageBody(message *waE2E.Message) string {
	if message == nil {
		return ""
	}
	for _, nested := range []*waE2E.Message{
		message.GetDeviceSentMessage().GetMessage(),
		message.GetEphemeralMessage().GetMessage(),
		message.GetViewOnceMessage().GetMessage(),
		message.GetViewOnceMessageV2().GetMessage(),
		message.GetViewOnceMessageV2Extension().GetMessage(),
		message.GetEditedMessage().GetMessage(),
	} {
		if body := messageBody(nested); body != "" {
			return body
		}
	}
	if body := message.GetConversation(); body != "" {
		return body
	}
	if body := message.GetExtendedTextMessage().GetText(); body != "" {
		return body
	}
	if body := message.GetListResponseMessage().GetTitle(); body != "" {
		return body
	}
	if body := message.GetButtonsResponseMessage().GetSelectedDisplayText(); body != "" {
		return body
	}
	if body := message.GetTemplateButtonReplyMessage().GetSelectedDisplayText(); body != "" {
		return body
	}
	return message.GetPollCreationMessage().GetName()
}

func mediaCaption(message *waE2E.Message) string {
	if message == nil {
		return ""
	}
	if message.GetImageMessage() != nil {
		return message.GetImageMessage().GetCaption()
	}
	if message.GetDocumentMessage() != nil {
		return message.GetDocumentMessage().GetCaption()
	}
	if message.GetVideoMessage() != nil {
		return message.GetVideoMessage().GetCaption()
	}
	return ""
}
