package worker

import (
	"encoding/json"
	"testing"
)

func TestTelegramMediaKinds(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		kind    string
		fileID  string
	}{
		{"photo", `{"photo":[{"file_id":"small"},{"file_id":"large"}]}`, "image", "large"},
		{"sticker", `{"sticker":{"file_id":"s"}}`, "sticker", "s"},
		{"animated sticker", `{"sticker":{"file_id":"s","is_animated":true}}`, "document", "s"},
		{"video sticker", `{"sticker":{"file_id":"s","is_video":true}}`, "document", "s"},
		{"animation", `{"animation":{"file_id":"a"}}`, "animation", "a"},
		{"video note", `{"video_note":{"file_id":"vn"}}`, "video_note", "vn"},
		{"video", `{"video":{"file_id":"v"}}`, "video", "v"},
		{"voice", `{"voice":{"file_id":"vo"}}`, "voice", "vo"},
		{"audio", `{"audio":{"file_id":"au"}}`, "audio", "au"},
		{"document", `{"document":{"file_id":"d","file_name":"a.pdf"}}`, "document", "d"},
		{"text", `{"text":"hello"}`, "", ""},
	}
	for _, item := range cases {
		var message telegramMessage
		if err := json.Unmarshal([]byte(item.payload), &message); err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		kind, file := telegramMedia(&message)
		if kind != item.kind || file.FileID != item.fileID {
			t.Fatalf("%s: kind = %q file = %q", item.name, kind, file.FileID)
		}
	}
}

func TestTelegramMediaKeepsFileMetadata(t *testing.T) {
	var message telegramMessage
	if err := json.Unmarshal([]byte(`{"document":{"file_id":"d","file_name":"a.pdf","mime_type":"application/pdf"}}`), &message); err != nil {
		t.Fatal(err)
	}
	_, file := telegramMedia(&message)
	if file.FileName != "a.pdf" || file.MimeType != "application/pdf" {
		t.Fatalf("file = %+v", file)
	}
}
