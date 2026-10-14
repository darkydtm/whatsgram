package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/darkydtm/whatsgram/internal/config"
	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Client struct {
	cfg    config.Telegram
	api    *bot.Bot
	http   *http.Client
	logger *slog.Logger
	mu     sync.RWMutex
	botID  int64
	ready  bool
	parts  PartStore
}

func (*Client) Format(s fmt.State, _ rune) { io.WriteString(s, "TelegramClient[redacted]") }
func (*Client) LogValue() slog.Value       { return slog.StringValue("TelegramClient[redacted]") }

func adapterError(class domain.ErrorClass, code string) error {
	return domain.AdapterError{Class: class, Code: code, MayHaveApplied: class == domain.Uncertain}
}

func New(cfg config.Telegram, client *http.Client, logger *slog.Logger) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(cfg.LocalAPI && u.Scheme == "http")) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || cfg.ChatID >= 0 || len(cfg.AllowedUserIDs) == 0 {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	cfg.AllowedUserIDs = append([]int64(nil), cfg.AllowedUserIDs...)
	for _, id := range cfg.AllowedUserIDs {
		if id <= 0 {
			return nil, adapterError(domain.Permanent, "invalid_target")
		}
	}
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cloned.Timeout <= 0 || cloned.Timeout > 35*time.Second {
		cloned.Timeout = 35 * time.Second
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c := &Client{cfg: cfg, http: &cloned, logger: logger}
	c.api, err = bot.New(cfg.Token.Reveal(), bot.WithSkipGetMe(), bot.WithServerURL(cfg.Endpoint), bot.WithHTTPClient(20*time.Second, boundedClient{&cloned}), bot.WithErrorsHandler(func(error) {}), bot.WithDebugHandler(func(string, ...any) {}))
	if err != nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	return c, nil
}

func (c *Client) Initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = false
	me, err := c.api.GetMe(ctx)
	if err != nil {
		return classify(ctx, err, false)
	}
	if me == nil || !me.IsBot || me.ID <= 0 {
		return adapterError(domain.Permanent, "invalid_target")
	}
	chat, err := c.api.GetChat(ctx, &bot.GetChatParams{ChatID: c.cfg.ChatID})
	if err != nil {
		return classify(ctx, err, false)
	}
	if chat == nil || chat.ID != c.cfg.ChatID || chat.Type != models.ChatTypeSupergroup || !chat.IsForum {
		return adapterError(domain.Permission, "permission_denied")
	}
	member, err := c.api.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: c.cfg.ChatID, UserID: me.ID})
	if err != nil {
		return classify(ctx, err, false)
	}
	if member == nil || member.Administrator == nil || member.Administrator.User.ID != me.ID || !member.Administrator.CanManageTopics || !member.Administrator.CanDeleteMessages {
		return adapterError(domain.Permission, "permission_denied")
	}
	c.botID, c.ready = me.ID, true
	return nil
}

func (c *Client) identity() (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.ready {
		return 0, adapterError(domain.Permission, "not_connected")
	}
	return c.botID, nil
}

func (c *Client) SendText(ctx context.Context, request domain.SendTextRequest) (domain.SendResult, error) {
	id, err := c.identity()
	if err != nil {
		return domain.SendResult{}, err
	}
	if request.TelegramTopic.ChatID != c.cfg.ChatID || request.TelegramTopic.ThreadID <= 0 || request.TelegramTopic.Generation < 0 || request.Content.Validate() != nil || utf8.RuneCountInString(request.Content.Text) == 0 || utf8.RuneCountInString(request.Content.Text) > 4096 {
		return domain.SendResult{}, adapterError(domain.Permanent, "invalid_target")
	}
	params := &bot.SendMessageParams{ChatID: c.cfg.ChatID, MessageThreadID: int(request.TelegramTopic.ThreadID), Text: request.Content.Text}
	if ref := request.Content.ReplyTo; ref != nil {
		chat, message, err := c.target(*ref)
		if err != nil || chat != c.cfg.ChatID {
			return domain.SendResult{}, adapterError(domain.Permanent, "invalid_target")
		}
		params.ReplyParameters = &models.ReplyParameters{MessageID: message}
	}
	message, err := c.api.SendMessage(ctx, params)
	if err != nil {
		return domain.SendResult{}, classify(ctx, err, true)
	}
	if message == nil || message.ID <= 0 || message.Chat.ID != c.cfg.ChatID || message.From == nil || message.From.ID != id || message.MessageThreadID != params.MessageThreadID {
		return domain.SendResult{}, adapterError(domain.Uncertain, "uncertain")
	}
	part := domain.DeliveredPart{Ref: domain.MessageRef{Platform: domain.Telegram, Chat: strconv.FormatInt(c.cfg.ChatID, 10), Author: strconv.FormatInt(id, 10), ID: strconv.Itoa(message.ID)}, Role: domain.RolePrimary, BridgeOwned: true, Capabilities: domain.CanEditText | domain.CanDelete | domain.CanReact}
	return domain.SendResult{Parts: []domain.DeliveredPart{part}, AcceptedAt: time.Now().UTC()}, nil
}

func (c *Client) target(ref domain.MessageRef) (int64, int, error) {
	chat, e1 := strconv.ParseInt(ref.Chat, 10, 64)
	message, e2 := strconv.Atoi(ref.ID)
	author, e3 := strconv.ParseInt(ref.Author, 10, 64)
	if ref.Validate() != nil || ref.Platform != domain.Telegram || e1 != nil || e2 != nil || e3 != nil || chat != c.cfg.ChatID || message <= 0 || author <= 0 || strconv.FormatInt(chat, 10) != ref.Chat || strconv.Itoa(message) != ref.ID || strconv.FormatInt(author, 10) != ref.Author {
		return 0, 0, adapterError(domain.Permanent, "invalid_target")
	}
	return chat, message, nil
}
