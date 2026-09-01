package provider

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestWhatsAppMediaKinds(t *testing.T) {
	cases := []struct {
		name     string
		message  *waE2E.Message
		kind     string
		filename string
	}{
		{"image", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg"), Caption: proto.String("hi")}}, "image", ""},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp")}}, "sticker", ""},
		{"animated sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{IsAnimated: proto.Bool(true)}}, "document", "sticker.webp"},
		{"gif", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{GifPlayback: proto.Bool(true)}}, "animation", ""},
		{"video", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Mimetype: proto.String("video/mp4")}}, "video", ""},
		{"video note", &waE2E.Message{PtvMessage: &waE2E.VideoMessage{}}, "video_note", ""},
		{"voice", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}}, "voice", ""},
		{"audio", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "audio", ""},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("report.pdf")}}, "document", "report.pdf"},
		{"nested", &waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}},
		}}, "image", ""},
		{"text", &waE2E.Message{Conversation: proto.String("hello")}, "", ""},
	}
	for _, item := range cases {
		info := whatsAppMedia(item.message)
		if item.kind == "" {
			if info != nil {
				t.Fatalf("%s: expected no media, got %+v", item.name, info)
			}
			continue
		}
		if info == nil {
			t.Fatalf("%s: expected media kind %q", item.name, item.kind)
		}
		if info.Kind != item.kind || info.Filename != item.filename {
			t.Fatalf("%s: kind = %q filename = %q", item.name, info.Kind, info.Filename)
		}
		if len(info.Encoded) == 0 {
			t.Fatalf("%s: encoded message is empty", item.name)
		}
	}
}

func TestWhatsAppMediaEncodesUnwrappedMessage(t *testing.T) {
	info := whatsAppMedia(&waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{
		Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{DirectPath: proto.String("/path")}},
	}})
	if info == nil {
		t.Fatal("expected document media")
	}
	var decoded waE2E.Message
	if err := proto.Unmarshal(info.Encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetDocumentMessage().GetDirectPath() != "/path" {
		t.Fatalf("encoded message lost attachment: %+v", &decoded)
	}
	if downloadable(&decoded) == nil {
		t.Fatal("encoded message is not downloadable")
	}
}

func TestWhatsAppMediaMessageKeepsCaptionAsBody(t *testing.T) {
	if got := mediaCaption(&waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look")}},
	}}); got != "look" {
		t.Fatalf("caption = %q", got)
	}
}

func TestMediaFilenameFallback(t *testing.T) {
	if got := mediaFilename(Media{Kind: "voice"}); got != "file.ogg" {
		t.Fatalf("filename = %q", got)
	}
	if got := mediaFilename(Media{Kind: "document", Filename: "a.pdf"}); got != "a.pdf" {
		t.Fatalf("filename = %q", got)
	}
}

func TestWhatsAppUploadCoversTelegramKinds(t *testing.T) {
	for kind := range telegramUploads {
		if _, ok := whatsappUploads[kind]; !ok {
			t.Fatalf("kind %q has no WhatsApp upload mapping", kind)
		}
		if _, ok := defaultMimetypes[kind]; !ok {
			t.Fatalf("kind %q has no default mimetype", kind)
		}
		if _, ok := defaultExtensions[kind]; !ok {
			t.Fatalf("kind %q has no default extension", kind)
		}
	}
}

func TestMessageBodyNonMediaFormats(t *testing.T) {
	poll := messageBody(&waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{
		Name:    proto.String("Lunch?"),
		Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Pizza")}},
	}})
	if poll != "Lunch?\n- Pizza" {
		t.Fatalf("poll body = %q", poll)
	}
	location := messageBody(&waE2E.Message{LocationMessage: &waE2E.LocationMessage{
		DegreesLatitude: proto.Float64(1), DegreesLongitude: proto.Float64(2), Name: proto.String("Home"),
	}})
	if !strings.Contains(location, "Home") || !strings.Contains(location, "q=1.000000,2.000000") {
		t.Fatalf("location body = %q", location)
	}
	contact := messageBody(&waE2E.Message{ContactMessage: &waE2E.ContactMessage{
		DisplayName: proto.String("Bob"), Vcard: proto.String("BEGIN:VCARD"),
	}})
	if !strings.Contains(contact, "Bob") || !strings.Contains(contact, "BEGIN:VCARD") {
		t.Fatalf("contact body = %q", contact)
	}
}
