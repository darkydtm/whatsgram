package media

import (
	"errors"
	"time"
)

// Supported media kinds.
type MediaKind string

const (
	KindVoice     MediaKind = "voice"
	KindAudio     MediaKind = "audio"
	KindVideo     MediaKind = "video"
	KindVideoNote MediaKind = "video_note"
	KindPhoto     MediaKind = "photo"
	KindSticker   MediaKind = "sticker"
	KindDocument  MediaKind = "document"
)

// File size and character limits.
const (
	MaxTelegramBotFileSize  = 50 * 1024 * 1024  // 50MB
	MaxWhatsAppMediaSize    = 64 * 1024 * 1024  // 64MB
	MaxWhatsAppDocumentSize = 100 * 1024 * 1024 // 100MB

	TelegramTextLimit    = 4096
	TelegramCaptionLimit = 1024
	WhatsAppTextLimit    = 65536
)

// Common media errors.
var (
	ErrFileTooLarge      = errors.New("file exceeds maximum allowed size")
	ErrUnsupportedFormat = errors.New("unsupported media format")
	ErrConversionFailed  = errors.New("media conversion failed")
	ErrInvalidMedia      = errors.New("invalid media payload")
)

// MediaFile represents a processed local media artifact ready for upload.
type MediaFile struct {
	Path     string
	Kind     MediaKind
	MimeType string
	Filename string
	Size     int64
	Duration time.Duration
	Width    int
	Height   int
	Cleanup  func() error
}

// StickerInfo holds sticker metadata and degradation flags.
type StickerInfo struct {
	Path           string
	MimeType       string
	IsAnimated     bool
	IsVideo        bool
	Degraded       bool
	FallbackNotice string
}

// DocumentInfo holds document metadata for pass-through transmission.
type DocumentInfo struct {
	Path     string
	Filename string
	MimeType string
	Size     int64
}
