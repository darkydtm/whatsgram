package media

import (
	"strings"
)

// SplitCaption splits a caption into primary and overflow parts based on rune length.
// If caption is within maxLen, overflow is empty.
func SplitCaption(caption string, maxLen int) (string, string) {
	if maxLen <= 0 {
		maxLen = TelegramCaptionLimit
	}
	runes := []rune(caption)
	if len(runes) <= maxLen {
		return caption, ""
	}

	splitIdx := maxLen
	// Attempt to split at newline or whitespace within the last 64 runes.
	searchStart := maxLen - 64
	if searchStart < 0 {
		searchStart = 0
	}
	for i := maxLen - 1; i >= searchStart; i-- {
		if runes[i] == '\n' || runes[i] == ' ' || runes[i] == '\t' {
			splitIdx = i + 1
			break
		}
	}

	primary := strings.TrimRight(string(runes[:splitIdx]), " \t\r\n")
	overflow := strings.TrimLeft(string(runes[splitIdx:]), " \t\r\n")
	if primary == "" {
		primary = string(runes[:maxLen])
		overflow = string(runes[maxLen:])
	}
	return primary, overflow
}

// SplitText divides a text into chunks of at most maxLen runes each.
func SplitText(text string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = TelegramTextLimit
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	if len(runes) <= maxLen {
		return []string{text}
	}

	var chunks []string
	remainder := runes
	for len(remainder) > 0 {
		if len(remainder) <= maxLen {
			chunks = append(chunks, string(remainder))
			break
		}

		splitIdx := maxLen
		// Try to break at newline or space within the last 128 runes.
		searchStart := maxLen - 128
		if searchStart < 0 {
			searchStart = 0
		}
		for i := maxLen - 1; i >= searchStart; i-- {
			if remainder[i] == '\n' {
				splitIdx = i + 1
				break
			}
			if remainder[i] == ' ' && splitIdx == maxLen {
				splitIdx = i + 1
			}
		}

		chunk := string(remainder[:splitIdx])
		chunks = append(chunks, chunk)
		remainder = remainder[splitIdx:]
	}
	return chunks
}
