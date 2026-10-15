package media

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// FFmpeg wraps external ffmpeg binary execution with safety and resource limits.
type FFmpeg struct {
	Path    string
	Timeout time.Duration
}

// NewFFmpeg creates an FFmpeg instance.
func NewFFmpeg(path string, timeout time.Duration) *FFmpeg {
	if path == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			path = p
		} else {
			path = "ffmpeg"
		}
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &FFmpeg{
		Path:    path,
		Timeout: timeout,
	}
}

// run executes ffmpeg command with args using direct exec.CommandContext (no shell interpolation).
func (f *FFmpeg) run(ctx context.Context, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, f.Path, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%w: ffmpeg process timed out", ErrConversionFailed)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Return sanitized error without payload details.
		return fmt.Errorf("%w: exit error", ErrConversionFailed)
	}
	return nil
}

// ConvertAudioToTelegramVoice converts input audio into Telegram voice message format (OGG Opus).
func (f *FFmpeg) ConvertAudioToTelegramVoice(ctx context.Context, src, dst string) error {
	args := []string{"-y", "-i", src, "-c:a", "libopus", "-b:a", "32k", "-vbr", "on", dst}
	return f.run(ctx, args...)
}

// ConvertAudioToTelegramAudio converts input audio into MP3 format.
func (f *FFmpeg) ConvertAudioToTelegramAudio(ctx context.Context, src, dst string) error {
	args := []string{"-y", "-i", src, "-c:a", "libmp3lame", "-q:a", "2", dst}
	return f.run(ctx, args...)
}

// ConvertAudioToWhatsAppPTT converts voice/audio into WhatsApp PTT format (OGG Opus, mono, 48kHz).
func (f *FFmpeg) ConvertAudioToWhatsAppPTT(ctx context.Context, src, dst string) error {
	args := []string{"-y", "-i", src, "-c:a", "libopus", "-ac", "1", "-ar", "48000", dst}
	return f.run(ctx, args...)
}

// ConvertAudioToWhatsAppAudio converts voice/audio into general WhatsApp audio format.
func (f *FFmpeg) ConvertAudioToWhatsAppAudio(ctx context.Context, src, dst string) error {
	args := []string{"-y", "-i", src, "-c:a", "libopus", "-b:a", "64k", dst}
	return f.run(ctx, args...)
}

// ConvertVideo transcodes video to MP4 (H.264 / AAC) with faststart flags.
func (f *FFmpeg) ConvertVideo(ctx context.Context, src, dst string) error {
	args := []string{
		"-y", "-i", src,
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-movflags", "+faststart",
		dst,
	}
	return f.run(ctx, args...)
}

// ConvertVideoNote crops and scales input video to a square 1:1 round video note.
func (f *FFmpeg) ConvertVideoNote(ctx context.Context, src, dst string) error {
	filter := "scale=if(gte(iw\\,ih)\\,min(640\\,ih)\\,-1):if(gte(iw\\,ih)\\,-1\\,min(640\\,iw)),crop=min(iw\\,ih):min(iw\\,ih)"
	args := []string{
		"-y", "-i", src,
		"-vf", filter,
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-movflags", "+faststart",
		dst,
	}
	return f.run(ctx, args...)
}
