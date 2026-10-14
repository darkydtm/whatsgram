package domain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf16"
)

var (
	ErrInvalid  = errors.New("invalid normalized data")
	ErrConflict = errors.New("source state conflict")
)

const MaxEventBytes = 1 << 20

type Platform string

const (
	Telegram Platform = "telegram"
	WhatsApp Platform = "whatsapp"
)

func (p Platform) Valid() bool { return p == Telegram || p == WhatsApp }

type MessageRef struct {
	Platform Platform
	Chat     string
	Author   string
	ID       string
	Device   string
}

func bounded(s string, max int) bool { return s != "" && len(s) <= max && !strings.ContainsRune(s, 0) }
func (r MessageRef) Validate() error {
	if !r.Platform.Valid() || !bounded(r.Chat, 256) || !bounded(r.Author, 256) || !bounded(r.ID, 256) || len(r.Device) > 256 {
		return ErrInvalid
	}
	return nil
}

type SourceVersion struct {
	Kind        string
	Order       int64
	Tie         string
	Digest      string
	StreamEpoch string
}

func (v SourceVersion) Validate() error {
	if !bounded(v.Kind, 64) || v.Order < 0 || len(v.Tie) > 256 || !bounded(v.Digest, 128) || len(v.StreamEpoch) > 256 {
		return ErrInvalid
	}
	return nil
}
func (v SourceVersion) Compare(other SourceVersion) (int, error) {
	if v.Validate() != nil || other.Validate() != nil {
		return 0, ErrInvalid
	}
	if v.Kind != other.Kind || v.StreamEpoch != other.StreamEpoch {
		return 0, ErrConflict
	}
	if v.Order < other.Order {
		return -1, nil
	}
	if v.Order > other.Order {
		return 1, nil
	}
	if v.Tie != other.Tie {
		return 0, ErrConflict
	}
	if v.Digest != other.Digest {
		return 0, ErrConflict
	}
	return 0, nil
}

type EventKind string

const (
	MessageCreate  EventKind = "message.create"
	MessageEdit    EventKind = "message.edit"
	MessageRevoke  EventKind = "message.revoke"
	ReactionSet    EventKind = "reaction.set"
	ChatDiscovered EventKind = "chat.discovered"
	ChatTitle      EventKind = "chat.title"
	ChatTimer      EventKind = "chat.timer"
	TopicState     EventKind = "topic.state"
	MessageKeep    EventKind = "message.keep"
)

func (k EventKind) Known() bool {
	switch k {
	case MessageCreate, MessageEdit, MessageRevoke, ReactionSet, ChatDiscovered, ChatTitle, ChatTimer, TopicState, MessageKeep:
		return true
	}
	return false
}

type Event struct {
	InboxID    int64
	Key        string
	Source     Platform
	Kind       EventKind
	Chat       string
	Target     *MessageRef
	Actor      string
	OccurredAt time.Time
	Version    SourceVersion
	Text       *TextContent
	Metadata   []byte
	Raw        []byte
}

func (e Event) Validate() error {
	if !e.Source.Valid() || !bounded(e.Key, 512) || !bounded(string(e.Kind), 64) || !bounded(e.Chat, 256) || !bounded(e.Actor, 256) || e.Version.Validate() != nil || len(e.Metadata)+len(e.Raw) > MaxEventBytes {
		return ErrInvalid
	}
	if e.Target != nil && (e.Target.Validate() != nil || e.Target.Platform != e.Source || e.Target.Chat != e.Chat) {
		return ErrInvalid
	}
	if e.Text != nil && e.Text.Validate() != nil {
		return ErrInvalid
	}
	return nil
}
func (Event) Format(s fmt.State, _ rune) { io.WriteString(s, "Event[redacted]") }

type TextContent struct {
	Text           string
	Entities       []TextEntity
	ReplyTo        *MessageRef
	ForwardLabel   string
	MentionTargets []IdentityLink
}

func (t TextContent) Validate() error {
	if len(t.Text)+len(t.ForwardLabel) > MaxEventBytes || len(t.Entities) > 1024 || len(t.MentionTargets) > 1024 {
		return ErrInvalid
	}
	if t.ReplyTo != nil && t.ReplyTo.Validate() != nil {
		return ErrInvalid
	}
	n := len(utf16.Encode([]rune(t.Text)))
	for _, e := range t.Entities {
		if !bounded(e.Kind, 64) || e.StartUTF16 < 0 || e.LengthUTF16 <= 0 || e.StartUTF16 > n || e.LengthUTF16 > n-e.StartUTF16 || len(e.URL) > 4096 || len(e.Mention) > 256 {
			return ErrInvalid
		}
	}
	for _, link := range t.MentionTargets {
		if link.TelegramUserID <= 0 || !bounded(link.WhatsAppJID, 256) {
			return ErrInvalid
		}
	}
	return nil
}

type TextEntity struct {
	Kind        string
	StartUTF16  int
	LengthUTF16 int
	URL         string
	Mention     string
}
type IdentityLink struct {
	TelegramUserID int64
	WhatsAppJID    string
}
type TopicRef struct {
	ChatID     int64
	ThreadID   int64
	Generation int64
}
type ChatCandidate struct {
	WhatsAppJID    string
	Title          string
	Source         string
	HistoryPartial bool
}
type Admission struct {
	Inserted bool
	InboxID  int64
}
type AdmissionStore interface {
	Accept(context.Context, Event) (Admission, error)
	AcceptTelegramBatch(context.Context, []Event, int64, string) error
}
type EventSink func(context.Context, Event) (Admission, error)
type SendTextRequest struct {
	OperationKey   string
	WhatsAppSendID string
	WhatsAppChat   string
	TelegramTopic  TopicRef
	Content        TextContent
}

const (
	RolePrimary         = "primary"
	RoleMedia           = "media"
	RoleText            = "text"
	RoleCaptionOverflow = "caption_overflow"
	RoleFallback        = "fallback"
	RoleRevisionNotice  = "revision_notice"
)
const (
	CanEditText uint64 = 1 << iota
	CanEditCaption
	CanEditMedia
	CanDelete
	CanRevoke
	CanReact
	CanPin
)

type DeliveredPart struct {
	Ref          MessageRef
	Index        int
	Role         string
	BridgeOwned  bool
	Capabilities uint64
	Deleted      bool
	Retired      bool
}

func (p DeliveredPart) Validate() error {
	if p.Ref.Validate() != nil || p.Index < 0 || p.Capabilities & ^uint64(CanEditText|CanEditCaption|CanEditMedia|CanDelete|CanRevoke|CanReact|CanPin) != 0 {
		return ErrInvalid
	}
	if !p.BridgeOwned && p.Capabilities&(CanEditText|CanEditCaption|CanEditMedia) != 0 {
		return ErrInvalid
	}
	switch p.Role {
	case RolePrimary, RoleMedia, RoleText, RoleCaptionOverflow, RoleFallback, RoleRevisionNotice:
		return nil
	}
	return ErrInvalid
}

type SendResult struct {
	Parts      []DeliveredPart
	AcceptedAt time.Time
}
type TelegramPort interface {
	Run(context.Context, EventSink) error
	CreateTopic(context.Context, int64, string) (TopicRef, error)
	RenameTopic(context.Context, TopicRef, string) error
	SendText(context.Context, SendTextRequest) (SendResult, error)
	EditText(context.Context, MessageRef, TextContent) error
	Delete(context.Context, MessageRef) error
	SetOwnReaction(context.Context, MessageRef, string) error
}
type WhatsAppPort interface {
	Run(context.Context, EventSink) error
	DiscoverChats(context.Context) ([]ChatCandidate, error)
	AllocateSendID() string
	SendText(context.Context, SendTextRequest) (SendResult, error)
	EditOwnText(context.Context, MessageRef, TextContent, string) error
	Revoke(context.Context, MessageRef, string) error
	SetOwnReaction(context.Context, MessageRef, string, string) error
}
type PairingNotice struct {
	Kind      string
	Secret    string
	ExpiresAt time.Time
}

func (PairingNotice) Format(s fmt.State, _ rune) { io.WriteString(s, "PairingNotice[redacted]") }

type PairingPort interface {
	BeginQR(context.Context) (<-chan PairingNotice, error)
	BeginPhoneCode(context.Context, string) (PairingNotice, error)
}
type ErrorClass string

const (
	Transient   ErrorClass = "transient"
	RateLimited ErrorClass = "rate_limited"
	Permission  ErrorClass = "permission"
	Permanent   ErrorClass = "permanent"
	Conflict    ErrorClass = "conflict"
	Uncertain   ErrorClass = "uncertain"
)

type AdapterError struct {
	Class          ErrorClass
	Code           string
	RetryAfter     time.Duration
	MayHaveApplied bool
	Cause          error
}

func (e AdapterError) Error() string {
	class := e.Class
	switch class {
	case Transient, RateLimited, Permission, Permanent, Conflict, Uncertain:
	default:
		class = Permanent
	}
	code := "adapter_error"
	switch e.Code {
	case "not_connected", "rate_limited", "permission_denied", "invalid_target", "lease_lost", "saturated", "busy", "storage", "conflict", "uncertain":
		code = e.Code
	}
	return string(class) + ": " + code
}
func (e AdapterError) Format(s fmt.State, _ rune) { io.WriteString(s, e.Error()) }
func (e AdapterError) Unwrap() error              { return e.Cause }

type Delivery struct {
	ID              int64
	OperationKey    string
	Platform        Platform
	Kind            string
	ChatID          int64
	TopicGeneration int64
	MessageID       int64
	DependsOnID     int64
	Target          *MessageRef
	ExpectedVersion SourceVersion
	Text            *TextContent
	WhatsAppSendID  string
	EchoActor       string
	LeaseToken      string
	LeaseUntil      time.Time
	SendStartedAt   *time.Time
}
type Completion struct {
	Result         SendResult
	AppliedVersion SourceVersion
	EchoKeys       []string
}
type RecoveryDecision struct {
	State       string
	ErrorClass  ErrorClass
	AvailableAt time.Time
	Code        string
}

func (Event) LogValue() slog.Value                 { return slog.StringValue("Event[redacted]") }
func (PairingNotice) LogValue() slog.Value         { return slog.StringValue("PairingNotice[redacted]") }
func (e AdapterError) LogValue() slog.Value        { return slog.StringValue(e.Error()) }
func (TextContent) Format(s fmt.State, _ rune)     { io.WriteString(s, "TextContent[redacted]") }
func (TextContent) LogValue() slog.Value           { return slog.StringValue("TextContent[redacted]") }
func (Delivery) Format(s fmt.State, _ rune)        { io.WriteString(s, "Delivery[redacted]") }
func (Delivery) LogValue() slog.Value              { return slog.StringValue("Delivery[redacted]") }
func (SendTextRequest) Format(s fmt.State, _ rune) { io.WriteString(s, "SendTextRequest[redacted]") }
func (SendTextRequest) LogValue() slog.Value       { return slog.StringValue("SendTextRequest[redacted]") }
