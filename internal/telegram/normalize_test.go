package telegram

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestNormalizePreservesAuthorizedNativeTextAndStableVersion(t *testing.T) {
	raw := json.RawMessage(`{"update_id":41,"message":{"message_id":8,"message_thread_id":7,"date":100,"from":{"id":12},"chat":{"id":-10042,"type":"supergroup"},"text":"😀hi","entities":[{"type":"bold","offset":2,"length":2}],"forward_origin":{"type":"hidden_user","date":90,"sender_user_name":"source"},"reply_to_message":{"message_id":6,"date":90,"from":{"id":20},"chat":{"id":-10042}}}}`)
	e, err := Normalize(testConfig("http://fixture"), raw, "epoch")
	if err != nil {
		t.Fatal(err)
	}
	if e.Validate() != nil || e.Kind != domain.MessageCreate || e.Actor != "12" || e.Chat != "-10042" || e.Target == nil || e.Target.ID != "8" || e.Target.Author != "12" || e.Version.Kind != "telegram.message" || e.Version.Order != 100 || e.Version.Tie != "41" || e.Version.StreamEpoch != "epoch" || e.OccurredAt.Unix() != 100 {
		t.Fatalf("incorrect normalized identity/version: %#v", e)
	}
	if e.Text == nil || e.Text.Text != "😀hi" || e.Text.ReplyTo == nil || e.Text.ReplyTo.ID != "6" || e.Text.Entities[0].StartUTF16 != 2 {
		t.Fatal("lost native text dimensions")
	}
	var meta Metadata
	if json.Unmarshal(e.Metadata, &meta) != nil || meta.ThreadID != 7 || meta.Admission != "allowed" || !bytes.Contains(meta.ForwardOrigin, []byte("hidden_user")) {
		t.Fatal("lost routing/ACL/forward")
	}
	duplicate, err := Normalize(testConfig("http://fixture"), raw, "epoch")
	if err != nil || !reflect.DeepEqual(e, duplicate) {
		t.Fatal("duplicate normalization changed")
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["update_id"] = json.RawMessage("42")
	wire["edited_message"] = json.RawMessage(`{"message_id":8,"message_thread_id":7,"date":100,"edit_date":100,"from":{"id":12},"chat":{"id":-10042},"text":"changed"}`)
	delete(wire, "message")
	editRaw, _ := json.Marshal(wire)
	edit, err := Normalize(testConfig("http://fixture"), editRaw, "epoch")
	if err != nil || edit.Kind != domain.MessageEdit {
		t.Fatal("missing edit")
	}
	if _, err := edit.Version.Compare(e.Version); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same-second different native update IDs must expose current domain conflict")
	}
}
