package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type Options struct {
	Logger             *slog.Logger
	Now                func() time.Time
	LookupMessage      func(context.Context, domain.MessageRef) (MessageMetadata, error)
	OperationTime      func(context.Context, string) (time.Time, error)
	ReconnectBase      time.Duration
	ReconnectMax       time.Duration
	MaxConnectAttempts int
}

type networkClient interface {
	IsConnected() bool
	IsLoggedIn() bool
	ConnectContext(context.Context) error
	Disconnect()
	SendMessage(context.Context, types.JID, *waE2E.Message, ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	GetQRChannel(context.Context) (<-chan whatsmeow.QRChannelItem, error)
	PairPhone(context.Context, string, bool, whatsmeow.PairClientType, string) (string, error)
	GetJoinedGroups(context.Context) ([]*types.GroupInfo, error)
	GetGroupInfo(context.Context, types.JID) (*types.GroupInfo, error)
	GetProfilePictureInfo(context.Context, types.JID, *whatsmeow.GetProfilePictureParams) (*types.ProfilePictureInfo, error)
	DownloadHistorySync(context.Context, *waE2E.HistorySyncNotification, bool) (*waHistorySync.HistorySync, error)
	ParseWebMessage(types.JID, *waWeb.WebMessageInfo) (*events.Message, error)
	Download(context.Context, whatsmeow.DownloadableMessage) ([]byte, error)
	Upload(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
}

type Client struct {
	client   *whatsmeow.Client
	network  networkClient
	device   *store.Device
	logger   *slog.Logger
	http     *http.Client
	mu       sync.Mutex
	options  Options
	running  bool
	terminal string
}

func New(ctx context.Context, device *store.Device, options Options) (*Client, error) {
	if ctx == nil || device == nil || device.Container == nil || device.NoiseKey == nil || device.IdentityKey == nil || device.SignedPreKey == nil || device.Deleted {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	device.Log = libraryLog{}
	cli := whatsmeow.NewClient(device, libraryLog{})
	cli.SynchronousAck = true
	cli.EnableDecryptedEventBuffer = true
	cli.EnableAutoReconnect = false
	cli.InitialAutoReconnect = false
	cli.DisableLoginAutoReconnect = true
	cli.BackgroundEventCtx = privateContext(ctx)
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReconnectBase == 0 {
		options.ReconnectBase = time.Second
	}
	if options.ReconnectMax == 0 {
		options.ReconnectMax = time.Minute
	}
	if options.MaxConnectAttempts == 0 {
		options.MaxConnectAttempts = 5
	}
	if options.ReconnectBase < time.Millisecond || options.ReconnectMax < options.ReconnectBase || options.ReconnectMax > 5*time.Minute || options.MaxConnectAttempts < 1 || options.MaxConnectAttempts > 20 {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	return &Client{client: cli, network: cli, device: device, logger: logger, options: options, http: &http.Client{Timeout: 20 * time.Second}}, nil
}

type libraryLog struct{}

func (libraryLog) Warnf(string, ...any)      {}
func (libraryLog) Errorf(string, ...any)     {}
func (libraryLog) Infof(string, ...any)      {}
func (libraryLog) Debugf(string, ...any)     {}
func (l libraryLog) Sub(string) waLog.Logger { return l }

func privateContext(ctx context.Context) context.Context {
	return zerolog.New(io.Discard).WithContext(ctx)
}

var errSafe = errors.New("whatsapp adapter failure")

func adapterError(class domain.ErrorClass, code string) error {
	return domain.AdapterError{Class: class, Code: code, MayHaveApplied: class == domain.Uncertain, Cause: errSafe}
}

func parseChat(value string) (types.JID, error) {
	j, err := types.ParseJID(value)
	if err != nil || j.User == "" || j.Device != 0 || j.RawAgent != 0 || j.String() != value || len(value) > 256 || strings.ContainsRune(value, 0) {
		return types.EmptyJID, adapterError(domain.Permanent, "invalid_target")
	}
	switch j.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		return j, nil
	default:
		return types.EmptyJID, adapterError(domain.Permanent, "invalid_target")
	}
}

func validID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "\x00\r\n")
}

func (c *Client) AllocateSendID() string { return c.client.GenerateMessageID() }

func (c *Client) IsPaired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.device.GetJID().IsEmpty() && !c.device.Deleted
}

func ContactName(info types.ContactInfo) string {
	if info.FullName != "" {
		return info.FullName
	}
	if info.FirstName != "" {
		return info.FirstName
	}
	if info.BusinessName != "" {
		return info.BusinessName
	}
	return ""
}

func FormatContactName(info types.ContactInfo, jid types.JID) string {
	if name := ContactName(info); name != "" {
		return name
	}
	if info.PushName != "" {
		return info.PushName
	}
	return ""
}

func FormatContactPhone(info types.ContactInfo, jid types.JID) string {
	if info.RedactedPhone != "" {
		return info.RedactedPhone
	}
	if jid.Server == types.DefaultUserServer && jid.User != "" {
		return "+" + jid.User
	}
	return ""
}

func FormatContactTitle(info types.ContactInfo, jid types.JID) string {
	if name := ContactName(info); name != "" {
		return name
	}
	if info.PushName != "" {
		return info.PushName
	}
	if phone := FormatContactPhone(info, jid); phone != "" {
		return phone
	}
	return jid.ToNonAD().String()
}

func (c *Client) DiscoverChats(ctx context.Context) ([]domain.ChatCandidate, error) {
	if ctx == nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	c.mu.Lock()
	if c.device.GetJID().IsEmpty() || c.device.Deleted {
		c.mu.Unlock()
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	c.mu.Unlock()

	candidates := make([]domain.ChatCandidate, 0)
	seen := make(map[string]bool)

	groups, err := c.network.GetJoinedGroups(ctx)
	if err != nil {
		return nil, adapterError(domain.Transient, "not_connected")
	}
	for _, g := range groups {
		if g != nil && !g.JID.IsEmpty() {
			jidStr := g.JID.ToNonAD().String()
			seen[jidStr] = true
			candidates = append(candidates, domain.ChatCandidate{
				WhatsAppJID:    jidStr,
				Title:          g.Name,
				Source:         "group",
				HistoryPartial: false,
			})
		}
	}

	if c.device != nil && c.device.Contacts != nil {
		contacts, err := c.device.Contacts.GetAllContacts(ctx)
		if err == nil {
			for jid, info := range contacts {
				if jid.Server == types.DefaultUserServer || jid.Server == types.HiddenUserServer {
					jidStr := jid.ToNonAD().String()
					if !seen[jidStr] {
						seen[jidStr] = true
						candidates = append(candidates, domain.ChatCandidate{
							WhatsAppJID:    jidStr,
							Title:          FormatContactTitle(info, jid),
							Source:         "contact",
							HistoryPartial: false,
						})
					}
				}
			}
		}
	}

	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, p := range g.Participants {
			pJID := p.JID
			if pJID.IsEmpty() {
				pJID = p.PhoneNumber
			}
			if pJID.IsEmpty() {
				continue
			}
			jidStr := pJID.ToNonAD().String()
			if !seen[jidStr] {
				seen[jidStr] = true
				var info types.ContactInfo
				if c.device != nil && c.device.Contacts != nil {
					info, _ = c.device.Contacts.GetContact(ctx, pJID)
				}
				name := p.DisplayName
				if name == "" {
					name = FormatContactTitle(info, pJID)
				}
				candidates = append(candidates, domain.ChatCandidate{
					WhatsAppJID:    jidStr,
					Title:          name,
					Source:         "contact",
					HistoryPartial: false,
				})
			}
		}
	}

	return candidates, nil
}

func (c *Client) GetContactMeta(ctx context.Context, jidStr string) (domain.ContactMeta, error) {
	if ctx == nil {
		return domain.ContactMeta{}, adapterError(domain.Permanent, "invalid_target")
	}
	parsed, err := parseChat(jidStr)
	if err != nil {
		return domain.ContactMeta{}, err
	}
	var info types.ContactInfo
	if c.device != nil && c.device.Contacts != nil {
		info, _ = c.device.Contacts.GetContact(ctx, parsed)
	}
	phone := FormatContactPhone(info, parsed)
	if phone == "" {
		if canon, err := c.canonical(ctx, parsed); err == nil && !canon.IsEmpty() {
			if c.device != nil && c.device.Contacts != nil && info.Found == false {
				info, _ = c.device.Contacts.GetContact(ctx, canon)
			}
			phone = FormatContactPhone(info, canon)
		}
	}
	return domain.ContactMeta{
		JID:   parsed.ToNonAD().String(),
		Name:  FormatContactName(info, parsed),
		Phone: phone,
	}, nil
}

func (c *Client) GetCommonGroups(ctx context.Context, jidStr string) ([]domain.GroupMeta, error) {
	if ctx == nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	parsed, err := parseChat(jidStr)
	if err != nil {
		return nil, err
	}
	groups, err := c.network.GetJoinedGroups(ctx)
	if err != nil {
		return nil, adapterError(domain.Transient, "not_connected")
	}
	userNonAD := parsed.ToNonAD().String()
	userVal := parsed.User
	var res []domain.GroupMeta
	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, p := range g.Participants {
			pNonAD := p.JID.ToNonAD().String()
			phoneNonAD := p.PhoneNumber.ToNonAD().String()
			lidNonAD := p.LID.ToNonAD().String()
			if pNonAD == userNonAD || phoneNonAD == userNonAD || lidNonAD == userNonAD || (userVal != "" && (p.JID.User == userVal || p.PhoneNumber.User == userVal)) {
				res = append(res, domain.GroupMeta{
					JID:  g.JID.ToNonAD().String(),
					Name: g.Name,
				})
				break
			}
		}
	}
	return res, nil
}

func (c *Client) GetGroupMeta(ctx context.Context, jidStr string) (domain.GroupMeta, error) {
	if ctx == nil {
		return domain.GroupMeta{}, adapterError(domain.Permanent, "invalid_target")
	}
	parsed, err := parseChat(jidStr)
	if err != nil {
		return domain.GroupMeta{}, err
	}
	groups, err := c.network.GetJoinedGroups(ctx)
	if err != nil {
		return domain.GroupMeta{}, adapterError(domain.Transient, "not_connected")
	}
	var target *types.GroupInfo
	for _, g := range groups {
		if g != nil && g.JID.ToNonAD() == parsed.ToNonAD() {
			target = g
			break
		}
	}
	if target == nil {
		target, err = c.network.GetGroupInfo(ctx, parsed)
		if err != nil {
			return domain.GroupMeta{}, adapterError(domain.Permanent, "invalid_target")
		}
	}
	meta := domain.GroupMeta{
		JID:  target.JID.ToNonAD().String(),
		Name: target.Name,
	}
	for _, p := range target.Participants {
		pJID := p.JID
		if pJID.IsEmpty() {
			pJID = p.PhoneNumber
		}
		if pJID.IsEmpty() {
			continue
		}
		var info types.ContactInfo
		if c.device != nil && c.device.Contacts != nil {
			info, _ = c.device.Contacts.GetContact(ctx, pJID)
		}
		name := p.DisplayName
		if name == "" {
			name = FormatContactName(info, pJID)
		}
		phone := FormatContactPhone(info, pJID)
		if phone == "" && !p.PhoneNumber.IsEmpty() {
			phone = FormatContactPhone(info, p.PhoneNumber)
		}
		if phone == "" {
			if canon, err := c.canonical(ctx, pJID); err == nil && !canon.IsEmpty() {
				phone = FormatContactPhone(info, canon)
			}
		}
		meta.Participants = append(meta.Participants, domain.ContactMeta{
			JID:   pJID.ToNonAD().String(),
			Name:  name,
			Phone: phone,
		})
	}
	return meta, nil
}

func (c *Client) GetAvatar(ctx context.Context, jidStr string) ([]byte, error) {
	if ctx == nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	parsed, err := parseChat(jidStr)
	if err != nil {
		return nil, err
	}
	if c.network == nil {
		return nil, adapterError(domain.Transient, "not_connected")
	}
	info, err := c.network.GetProfilePictureInfo(ctx, parsed, &whatsmeow.GetProfilePictureParams{Preview: false})
	if err != nil || info == nil || info.URL == "" {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return nil, adapterError(domain.Transient, "storage")
	}
	client := c.http
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, adapterError(domain.Transient, "not_connected")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, adapterError(domain.Transient, "storage")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil || len(data) == 0 {
		return nil, adapterError(domain.Transient, "storage")
	}
	return data, nil
}

func buildText(chat types.JID, content domain.TextContent) (*waE2E.Message, error) {
	if content.Validate() != nil || content.Text == "" {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	if content.ReplyTo == nil {
		return &waE2E.Message{Conversation: proto.String(content.Text)}, nil
	}
	r := content.ReplyTo
	if r.Platform != domain.WhatsApp || r.Chat != chat.String() {
		return &waE2E.Message{Conversation: proto.String(content.Text)}, nil
	}
	if _, err := parseChat(r.Author); err != nil {
		return &waE2E.Message{Conversation: proto.String(content.Text)}, nil
	}
	return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(content.Text), ContextInfo: &waE2E.ContextInfo{
		StanzaID: proto.String(r.ID), Participant: proto.String(r.Author), RemoteJID: proto.String(r.Chat),
	}}}, nil
}

func (c *Client) SendText(ctx context.Context, request domain.SendTextRequest) (domain.SendResult, error) {
	if request.OperationKey == "" || !validID(request.WhatsAppSendID) {
		return domain.SendResult{}, adapterError(domain.Permanent, "invalid_target")
	}
	chat, err := parseChat(request.WhatsAppChat)
	if err != nil {
		return domain.SendResult{}, err
	}
	message, err := buildText(chat, request.Content)
	if err != nil {
		return domain.SendResult{}, err
	}
	resp, err := c.send(ctx, chat, message, request.WhatsAppSendID)
	if err != nil {
		return domain.SendResult{}, err
	}
	author := resp.Sender
	if author.IsEmpty() {
		author = c.device.GetJID()
	}
	ref := domain.MessageRef{Platform: domain.WhatsApp, Chat: chat.String(), Author: author.ToNonAD().String(), ID: request.WhatsAppSendID, Device: author.String()}
	part := domain.DeliveredPart{Ref: ref, Role: domain.RolePrimary, BridgeOwned: true, Capabilities: domain.CanEditText | domain.CanRevoke | domain.CanReact}
	if part.Validate() != nil || resp.ID != request.WhatsAppSendID {
		return domain.SendResult{}, adapterError(domain.Uncertain, "uncertain")
	}
	return domain.SendResult{Parts: []domain.DeliveredPart{part}, AcceptedAt: resp.Timestamp}, nil
}

func (c *Client) DownloadMedia(ctx context.Context, rawEnvelopeBytes []byte) ([]byte, error) {
	if ctx == nil || len(rawEnvelopeBytes) == 0 {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	var env rawEnvelope
	if err := json.Unmarshal(rawEnvelopeBytes, &env); err != nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	if len(env.Payload) == 0 {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	var raw waE2E.Message
	if err := proto.Unmarshal(env.Payload, &raw); err != nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	body, _ := unwrapMessage(&raw)
	if body == nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	if img := body.GetImageMessage(); img != nil && c.network != nil {
		return c.network.Download(ctx, img)
	}
	return nil, adapterError(domain.Permanent, "invalid_target")
}

func (c *Client) SendPhoto(ctx context.Context, request domain.SendPhotoRequest) (domain.SendResult, error) {
	if request.OperationKey == "" || !validID(request.WhatsAppSendID) || len(request.Data) == 0 {
		return domain.SendResult{}, adapterError(domain.Permanent, "invalid_target")
	}
	chat, err := parseChat(request.WhatsAppChat)
	if err != nil {
		return domain.SendResult{}, err
	}
	resp, err := c.network.Upload(ctx, request.Data, whatsmeow.MediaImage)
	if err != nil {
		return domain.SendResult{}, adapterError(domain.Transient, "not_connected")
	}
	fileLen := uint64(len(request.Data))
	imageMsg := &waE2E.ImageMessage{
		Caption:       proto.String(request.Caption),
		Mimetype:      proto.String("image/jpeg"),
		URL:           proto.String(resp.URL),
		DirectPath:    proto.String(resp.DirectPath),
		MediaKey:      resp.MediaKey,
		FileEncSHA256: resp.FileEncSHA256,
		FileSHA256:    resp.FileSHA256,
		FileLength:    proto.Uint64(fileLen),
	}
	msg := &waE2E.Message{ImageMessage: imageMsg}
	sendResp, err := c.send(ctx, chat, msg, request.WhatsAppSendID)
	if err != nil {
		return domain.SendResult{}, err
	}
	author := sendResp.Sender
	if author.IsEmpty() {
		author = c.device.GetJID()
	}
	ref := domain.MessageRef{Platform: domain.WhatsApp, Chat: chat.String(), Author: author.ToNonAD().String(), ID: request.WhatsAppSendID, Device: author.String()}
	part := domain.DeliveredPart{Ref: ref, Role: domain.RolePrimary, BridgeOwned: true, Capabilities: domain.CanEditText | domain.CanRevoke | domain.CanReact}
	if part.Validate() != nil || sendResp.ID != request.WhatsAppSendID {
		return domain.SendResult{}, adapterError(domain.Uncertain, "uncertain")
	}
	return domain.SendResult{Parts: []domain.DeliveredPart{part}, AcceptedAt: sendResp.Timestamp}, nil
}

func (c *Client) send(ctx context.Context, chat types.JID, message *waE2E.Message, id string) (whatsmeow.SendResponse, error) {
	c.mu.Lock()
	terminal := c.terminal
	c.mu.Unlock()
	if terminal != "" {
		return whatsmeow.SendResponse{}, adapterError(domain.Permanent, "invalid_target")
	}
	if ctx.Err() != nil {
		return whatsmeow.SendResponse{}, adapterError(domain.Transient, "not_connected")
	}
	if !c.network.IsConnected() || !c.network.IsLoggedIn() || c.device.GetJID().IsEmpty() {
		return whatsmeow.SendResponse{}, adapterError(domain.Transient, "not_connected")
	}
	resp, err := c.network.SendMessage(privateContext(ctx), chat, message, whatsmeow.SendRequestExtra{ID: id})
	if err != nil {
		return resp, sendError(err)
	}
	return resp, nil
}
