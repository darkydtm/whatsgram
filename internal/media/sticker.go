package media

import (
	"context"
	"strings"
)

// ProcessSticker inspects and prepares sticker media.
// Static WebP stickers are accepted. Animated TGS and video stickers undergo graceful degradation.
func ProcessSticker(ctx context.Context, mimeType string, filename string) StickerInfo {
	mime := strings.ToLower(strings.TrimSpace(mimeType))
	name := strings.ToLower(strings.TrimSpace(filename))

	// Animated TGS stickers (Telegram Lottie).
	if mime == "application/x-tgsticker" || strings.HasSuffix(name, ".tgs") {
		return StickerInfo{
			MimeType:       mime,
			IsAnimated:     true,
			Degraded:       true,
			FallbackNotice: "[Unsupported animated sticker: forwarded as document]",
		}
	}

	// Video stickers (WebM).
	if mime == "video/webm" || strings.HasSuffix(name, ".webm") {
		return StickerInfo{
			MimeType:       mime,
			IsVideo:        true,
			Degraded:       false,
			FallbackNotice: "",
		}
	}

	// WebP stickers.
	if mime == "image/webp" || strings.HasSuffix(name, ".webp") {
		return StickerInfo{
			MimeType:   "image/webp",
			IsAnimated: false,
			Degraded:   false,
		}
	}

	// Unknown or generic sticker degradation.
	return StickerInfo{
		MimeType:       mime,
		Degraded:       true,
		FallbackNotice: "[Unsupported sticker format]",
	}
}
