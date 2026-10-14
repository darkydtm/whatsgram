package telegram

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestNormalizeCommandsAndUnauthorizedInputsNeverExecute(t *testing.T) {
	cases := []struct{ name, payload, kind, reason string }{
		{"command", `"text":"/unknown@Other arg"`, "telegram.command", ""},
		{"anonymous", `"text":"hi","sender_chat":{"id":-10042}`, "telegram.rejected", "unauthorized"},
		{"caption_command", `"caption":"/del"`, "telegram.command", ""},
		{"invalid_entity", `"text":"😀","entities":[{"type":"bold","offset":1,"length":1}]`, "telegram.invalid", "invalid_payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"update_id":42,"message":{"message_id":8,"message_thread_id":7,"date":100,"from":{"id":12},"chat":{"id":-10042},` + tc.payload + `}}`)
			e, err := Normalize(testConfig("http://fixture"), raw, "epoch")
			if err != nil || e.Validate() != nil || string(e.Kind) != tc.kind {
				t.Fatalf("classification = %s, %v", e.Kind, err)
			}
			if e.Text != nil {
				t.Fatal("command/unsafe input became ordinary text")
			}
			var meta Metadata
			_ = json.Unmarshal(e.Metadata, &meta)
			if meta.Reason != tc.reason {
				t.Errorf("reason = %s", meta.Reason)
			}
			if !bytes.Contains(e.Raw, []byte(tc.payload)) && tc.name != "invalid_entity" {
				t.Fatal("private command/payload lost")
			}
		})
	}
	for _, source := range []string{`"id":13`, `"id":12,"is_bot":true`} {
		raw := json.RawMessage(`{"update_id":50,"message":{"message_id":8,"date":100,"from":{` + source + `},"chat":{"id":-10042},"text":"hi"}}`)
		e, err := Normalize(testConfig("http://fixture"), raw, "epoch")
		if err != nil || e.Kind.Known() {
			t.Fatal("unauthorized actor executable")
		}
	}
	raw := json.RawMessage(`{"update_id":50,"message":{"message_id":8,"date":100,"from":{"id":12},"chat":{"id":12,"type":"private"},"text":"/pair"}}`)
	e, err := Normalize(testConfig("http://fixture"), raw, "epoch")
	if err != nil || e.Kind.Known() {
		t.Fatal("private input executable")
	}
}

func TestNormalizeTaggedReactionsPollsAndUnknownUpdates(t *testing.T) {
	cases := []struct{ payload, kind, actor, reason string }{
		{`"message_reaction":{"chat":{"id":-10042},"message_id":8,"date":101,"user":{"id":12},"old_reaction":[],"new_reaction":[{"type":"emoji","emoji":"👍"}]}`, "telegram.reaction", "12", "mapping_required"},
		{`"message_reaction":{"chat":{"id":-10042},"message_id":8,"date":101,"user":{"id":20},"old_reaction":[],"new_reaction":[]}`, "telegram.rejected", "20", "account_driver_only"},
		{`"message_reaction_count":{"chat":{"id":-10042},"message_id":8,"date":101,"reactions":[{"type":{"type":"emoji","emoji":"👍"},"total_count":3}]}`, "telegram.reaction_count", "unresolved", "aggregate_no_actor"},
		{`"poll_answer":{"poll_id":"poll1","user":{"id":12},"option_ids":[]}`, "telegram.poll_answer", "12", "poll_scope_required"},
		{`"poll":{"id":"poll1","question":"Q","options":[],"is_closed":true}`, "telegram.poll", "unresolved", "poll_scope_required"},
		{`"future_update":{"secret_payload":"fixture"}`, "telegram.unknown", "unresolved", "unsupported_update"},
		{`"message":"malformed"`, "telegram.invalid", "unresolved", "invalid_payload"},
	}
	for i, tc := range cases {
		raw := json.RawMessage(`{"update_id":` + strconv.Itoa(i) + `,` + tc.payload + `}`)
		e, err := Normalize(testConfig("http://fixture"), raw, "epoch")
		if err != nil || e.Validate() != nil || string(e.Kind) != tc.kind || e.Actor != tc.actor {
			t.Fatalf("case %d kind=%s actor=%s err=%v", i, e.Kind, e.Actor, err)
		}
		var meta Metadata
		_ = json.Unmarshal(e.Metadata, &meta)
		if meta.Reason != tc.reason || e.Kind.Known() || e.Target != nil {
			t.Fatalf("case %d invented executable actor/mapping", i)
		}
	}
}

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
