package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/darkydtm/whatsgram/internal/config"
	"github.com/darkydtm/whatsgram/internal/domain"
)

const testToken = "901:synthetic-only"

func testConfig(endpoint string) config.Telegram {
	return config.Telegram{Token: config.NewSecret(testToken), ChatID: -10042, AllowedUserIDs: []int64{12, 20}, Endpoint: endpoint, LocalAPI: true}
}

func writeOK(w http.ResponseWriter, result string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"result":%s}`, result)
}

func initialization(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/bot" + testToken + "/getMe":
		writeOK(w, `{"id":901,"is_bot":true,"first_name":"fixture"}`)
	case "/bot" + testToken + "/getChat":
		writeOK(w, `{"id":-10042,"type":"supergroup","is_forum":true}`)
	case "/bot" + testToken + "/getChatMember":
		writeOK(w, `{"status":"administrator","user":{"id":901,"is_bot":true},"can_manage_topics":true,"can_delete_messages":true}`)
	default:
		return false
	}
	return true
}

func TestSendTextUsesSelectedClientAndNativeIdentity(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if initialization(w, r) {
			return
		}
		if r.URL.Path != "/bot"+testToken+"/sendMessage" {
			t.Errorf("unexpected method path")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		if r.FormValue("chat_id") != "-10042" || r.FormValue("message_thread_id") != "7" || r.FormValue("text") != "<b>plain 😀</b>" || r.FormValue("parse_mode") != "" {
			t.Errorf("incorrect text/topic encoding")
		}
		var reply struct {
			MessageID int `json:"message_id"`
		}
		if json.Unmarshal([]byte(r.FormValue("reply_parameters")), &reply) != nil || reply.MessageID != 5 {
			t.Errorf("incorrect mapped reply")
		}
		writeOK(w, `{"message_id":33,"message_thread_id":7,"date":10,"chat":{"id":-10042,"type":"supergroup"},"from":{"id":901,"is_bot":true},"text":"<b>plain 😀</b>"}`)
	}))
	defer srv.Close()
	c, err := New(testConfig(srv.URL), srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("constructor made a network request")
	}
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	reply := domain.MessageRef{Platform: domain.Telegram, Chat: "-10042", Author: "12", ID: "5"}
	result, err := c.SendText(context.Background(), domain.SendTextRequest{TelegramTopic: domain.TopicRef{ChatID: -10042, ThreadID: 7, Generation: 2}, Content: domain.TextContent{Text: "<b>plain 😀</b>", ReplyTo: &reply}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Parts) != 1 || result.Parts[0].Ref.Author != "901" || result.Parts[0].Ref.ID != "33" || !result.Parts[0].BridgeOwned || result.Parts[0].Capabilities&domain.CanEditText == 0 || result.AcceptedAt.IsZero() {
		t.Fatalf("missing native delivery identity: %#v", result)
	}
	if strings.Contains(fmt.Sprintf("%#v", c), testToken) {
		t.Fatal("client formatting leaked token")
	}
}

type partRegistry struct{ part domain.DeliveredPart }

func (p partRegistry) Part(context.Context, domain.MessageRef) (domain.DeliveredPart, error) {
	return p.part, nil
}

func TestMappedLifecycleAndForumsUseActualWireMethods(t *testing.T) {
	calls := make(chan string, 12)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if initialization(w, r) {
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		method := strings.TrimPrefix(r.URL.Path, "/bot"+testToken+"/")
		calls <- method
		if r.FormValue("chat_id") != "-10042" {
			t.Error("incorrect target chat")
		}
		switch method {
		case "editMessageText":
			if r.FormValue("message_id") != "33" || r.FormValue("text") != "changed" {
				t.Error("incorrect edit")
			}
			writeOK(w, `{"message_id":33,"chat":{"id":-10042},"from":{"id":901,"is_bot":true}}`)
		case "deleteMessage":
			if r.FormValue("message_id") != "33" {
				t.Error("incorrect delete")
			}
			writeOK(w, "true")
		case "setMessageReaction":
			if r.FormValue("message_id") != "33" {
				t.Error("incorrect reaction target")
			}
			if value := r.FormValue("reaction"); value != `[{"type":"emoji","emoji":"👍"}]` && value != "[]" {
				t.Errorf("incorrect reaction: %s", value)
			}
			writeOK(w, "true")
		case "createForumTopic":
			if r.FormValue("name") != "Group" {
				t.Error("incorrect topic name")
			}
			writeOK(w, `{"message_thread_id":9,"name":"Group","icon_color":0}`)
		case "editForumTopic":
			if r.FormValue("message_thread_id") != "9" || r.FormValue("name") != "Renamed" {
				t.Error("incorrect rename")
			}
			writeOK(w, "true")
		default:
			t.Error("unexpected method")
		}
	}))
	defer srv.Close()
	c, err := New(testConfig(srv.URL), srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	ref := domain.MessageRef{Platform: domain.Telegram, Chat: "-10042", Author: "901", ID: "33"}
	c.BindParts(partRegistry{domain.DeliveredPart{Ref: ref, Role: domain.RolePrimary, BridgeOwned: true, Capabilities: domain.CanEditText | domain.CanDelete | domain.CanReact}})
	if err := c.EditText(ctx, ref, domain.TextContent{Text: "changed"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetOwnReaction(ctx, ref, "👍"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetOwnReaction(ctx, ref, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	topic, err := c.CreateTopic(ctx, -10042, "Group")
	if err != nil || topic.ChatID != -10042 || topic.ThreadID != 9 || topic.Generation != 0 {
		t.Fatalf("topic result = %#v, %v", topic, err)
	}
	if err := c.RenameTopic(ctx, topic, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 6 {
		t.Fatalf("wire calls = %d", len(calls))
	}
	ref.Author = "12"
	if err := c.EditText(ctx, ref, domain.TextContent{Text: "not owned"}); err == nil {
		t.Fatal("edited user message")
	}
	if len(calls) != 6 {
		t.Fatal("unauthorized edit reached wire")
	}
}

var _ io.Reader
