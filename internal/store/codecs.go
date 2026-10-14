package store

import (
	"encoding/json"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

type storedText domain.TextContent

type storedEvent struct {
	InboxID          int64
	Key              string
	Source           domain.Platform
	Kind             domain.EventKind
	Chat             string
	Target           *domain.MessageRef
	Actor            string
	OccurredAt       time.Time
	Version          domain.SourceVersion
	Text             *storedText
	Metadata         []byte
	Raw              []byte
	TelegramThreadID int64 `json:",omitempty"`
}

type storedDelivery struct {
	ID              int64
	OperationKey    string
	Platform        domain.Platform
	Kind            string
	ChatID          int64
	TopicGeneration int64
	MessageID       int64
	DependsOnID     int64
	Target          *domain.MessageRef
	ExpectedVersion domain.SourceVersion
	Text            *storedText
	WhatsAppSendID  string
	EchoActor       string
	LeaseToken      string
	LeaseUntil      time.Time
	SendStartedAt   *time.Time
}

func marshalEvent(e domain.Event) ([]byte, error) {
	return json.Marshal(storedEvent{e.InboxID, e.Key, e.Source, e.Kind, e.Chat, e.Target, e.Actor, e.OccurredAt, e.Version, (*storedText)(e.Text), e.Metadata, e.Raw, e.TelegramThreadID})
}

func marshalText(text *domain.TextContent) ([]byte, error) {
	return json.Marshal((*storedText)(text))
}

func marshalDelivery(d domain.Delivery) ([]byte, error) {
	return json.Marshal(storedDelivery{d.ID, d.OperationKey, d.Platform, d.Kind, d.ChatID, d.TopicGeneration, d.MessageID, d.DependsOnID, d.Target, d.ExpectedVersion, (*storedText)(d.Text), d.WhatsAppSendID, d.EchoActor, d.LeaseToken, d.LeaseUntil, d.SendStartedAt})
}
