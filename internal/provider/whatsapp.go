package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/darky/whatsgram/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
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
	container, err := sqlstore.New(ctx, "pgx", databaseURL, waLog.Stdout("whatsmeow", "WARN", false))
	if err != nil {
		return WhatsApp{}, err
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return WhatsApp{}, err
	}
	client := whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "WARN", false))
	client.AddEventHandler(func(evt any) {
		switch event := evt.(type) {
		case *events.Message:
			if event.Info.IsFromMe {
				return
			}
			payload, err := jsonMessage(event)
			if err == nil {
				err = inbox.PutInbox(ctx, "whatsapp", string(event.Info.ID), payload)
			}
			if err != nil {
				log.Printf("whatsapp event: %v", err)
			}
		case *events.Receipt:
			for _, id := range event.MessageIDs {
				payload := []byte(fmt.Sprintf(`{"id":%q,"status":%q}`, id, event.Type))
				if err := inbox.PutInbox(ctx, "whatsapp", string(id)+":status", payload); err != nil {
					log.Printf("whatsapp receipt: %v", err)
				}
			}
		case *events.QR:
			for _, code := range event.Codes {
				log.Printf("WhatsApp QR: %s", code)
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
					log.Printf("WhatsApp QR: %s", item.Code)
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

func (w WhatsApp) SendText(ctx context.Context, recipient, body string) error {
	jid, err := parseJID(recipient)
	if err != nil {
		return err
	}
	_, err = w.Client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(body)})
	return err
}

func (w WhatsApp) SendMedia(ctx context.Context, recipient, mediaType string, content io.Reader, caption string) error {
	jid, err := parseJID(recipient)
	if err != nil {
		return err
	}
	appInfo, ok := map[string]whatsmeow.MediaType{
		"image": whatsmeow.MediaImage, "document": whatsmeow.MediaDocument,
		"video": whatsmeow.MediaVideo, "audio": whatsmeow.MediaAudio,
	}[strings.ToLower(mediaType)]
	if !ok {
		return fmt.Errorf("unsupported WhatsApp media type %q", mediaType)
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	upload, err := w.Client.Upload(ctx, data, appInfo)
	if err != nil {
		return err
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
	_, err = w.Client.SendMessage(ctx, jid, message)
	return err
}

func jsonMessage(event *events.Message) ([]byte, error) {
	event.UnwrapRaw()
	body := event.Message.GetConversation()
	if body == "" {
		body = event.Message.GetExtendedTextMessage().GetText()
	}
	return json.Marshal(struct {
		From    string `json:"from"`
		Name    string `json:"name"`
		ID      string `json:"id"`
		Type    string `json:"type"`
		Body    string `json:"body"`
		MediaID string `json:"media_id,omitempty"`
		Caption string `json:"caption,omitempty"`
	}{event.Info.Chat.String(), event.Info.PushName, string(event.Info.ID), event.Info.MediaType, body, string(event.Info.ID), mediaCaption(event.Message)})
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
