package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Pipeline manages media processing, transcoding, and guaranteed temp file cleanup.
type Pipeline struct {
	scratchDir string
	ffmpeg     *FFmpeg
	mu         sync.Mutex
}

// NewPipeline initializes a new Pipeline.
func NewPipeline(scratchDir string, ffmpeg *FFmpeg) (*Pipeline, error) {
	if scratchDir == "" {
		scratchDir = filepath.Join(os.TempDir(), "whatsgram-spool")
	}
	if err := os.MkdirAll(scratchDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create scratch directory: %w", err)
	}
	if ffmpeg == nil {
		ffmpeg = NewFFmpeg("", 0)
	}
	return &Pipeline{
		scratchDir: scratchDir,
		ffmpeg:     ffmpeg,
	}, nil
}

// ScratchDir returns the root scratch directory.
func (p *Pipeline) ScratchDir() string {
	return p.scratchDir
}

// TempFile creates a temporary file in the scratch spool directory with guaranteed cleanup.
func (p *Pipeline) TempFile(prefix, ext string) (*os.File, func(), error) {
	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, nil, err
	}
	name := fmt.Sprintf("%s-%s%s", prefix, hex.EncodeToString(randomBytes), ext)
	targetPath := filepath.Join(p.scratchDir, name)

	f, err := os.OpenFile(targetPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, nil, err
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = f.Close()
			_ = os.Remove(targetPath)
		})
	}
	return f, cleanup, nil
}

// WriteToTemp streams an io.Reader into a temporary scratch file, verifying byte limit.
func (p *Pipeline) WriteToTemp(r io.Reader, maxBytes int64, prefix, ext string) (string, func(), error) {
	f, cleanup, err := p.TempFile(prefix, ext)
	if err != nil {
		return "", nil, err
	}

	lr := io.LimitReader(r, maxBytes+1)
	n, err := io.Copy(f, lr)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if n > maxBytes {
		cleanup()
		return "", nil, ErrFileTooLarge
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}

// ProcessVoiceWAToTG converts a WhatsApp Opus audio/ogg into Telegram voice format (OGG Opus).
func (p *Pipeline) ProcessVoiceWAToTG(ctx context.Context, src io.Reader, size int64) (*MediaFile, error) {
	if size > MaxTelegramBotFileSize {
		return nil, ErrFileTooLarge
	}

	srcPath, cleanupSrc, err := p.WriteToTemp(src, MaxTelegramBotFileSize, "wa-voice-in", ".ogg")
	if err != nil {
		return nil, err
	}
	defer cleanupSrc()

	dstFile, cleanupDst, err := p.TempFile("tg-voice-out", ".ogg")
	if err != nil {
		return nil, err
	}
	dstPath := dstFile.Name()
	_ = dstFile.Close()

	if err := p.ffmpeg.ConvertAudioToTelegramVoice(ctx, srcPath, dstPath); err != nil {
		cleanupDst()
		return nil, err
	}

	stat, err := os.Stat(dstPath)
	if err != nil || stat.Size() == 0 {
		cleanupDst()
		return nil, ErrConversionFailed
	}
	if stat.Size() > MaxTelegramBotFileSize {
		cleanupDst()
		return nil, ErrFileTooLarge
	}

	return &MediaFile{
		Path:     dstPath,
		Kind:     KindVoice,
		MimeType: "audio/ogg",
		Filename: filepath.Base(dstPath),
		Size:     stat.Size(),
		Cleanup: func() error {
			cleanupDst()
			return nil
		},
	}, nil
}

// ProcessVoiceTGToWA converts Telegram voice/audio into WhatsApp PTT format (OGG Opus mono 48kHz).
func (p *Pipeline) ProcessVoiceTGToWA(ctx context.Context, src io.Reader, size int64) (*MediaFile, error) {
	if size > MaxWhatsAppMediaSize {
		return nil, ErrFileTooLarge
	}

	srcPath, cleanupSrc, err := p.WriteToTemp(src, MaxWhatsAppMediaSize, "tg-voice-in", ".ogg")
	if err != nil {
		return nil, err
	}
	defer cleanupSrc()

	dstFile, cleanupDst, err := p.TempFile("wa-ptt-out", ".ogg")
	if err != nil {
		return nil, err
	}
	dstPath := dstFile.Name()
	_ = dstFile.Close()

	if err := p.ffmpeg.ConvertAudioToWhatsAppPTT(ctx, srcPath, dstPath); err != nil {
		cleanupDst()
		return nil, err
	}

	stat, err := os.Stat(dstPath)
	if err != nil || stat.Size() == 0 {
		cleanupDst()
		return nil, ErrConversionFailed
	}
	if stat.Size() > MaxWhatsAppMediaSize {
		cleanupDst()
		return nil, ErrFileTooLarge
	}

	return &MediaFile{
		Path:     dstPath,
		Kind:     KindVoice,
		MimeType: "audio/ogg; codecs=opus",
		Filename: filepath.Base(dstPath),
		Size:     stat.Size(),
		Cleanup: func() error {
			cleanupDst()
			return nil
		},
	}, nil
}

// ProcessVideo transcodes video to standard MP4 H.264/AAC. If isVideoNote is true, converts to round 1:1 format.
func (p *Pipeline) ProcessVideo(ctx context.Context, src io.Reader, size int64, isVideoNote bool) (*MediaFile, error) {
	limit := int64(MaxTelegramBotFileSize)
	if size > limit {
		return nil, ErrFileTooLarge
	}

	srcPath, cleanupSrc, err := p.WriteToTemp(src, limit, "video-in", ".mp4")
	if err != nil {
		return nil, err
	}
	defer cleanupSrc()

	dstFile, cleanupDst, err := p.TempFile("video-out", ".mp4")
	if err != nil {
		return nil, err
	}
	dstPath := dstFile.Name()
	_ = dstFile.Close()

	if isVideoNote {
		err = p.ffmpeg.ConvertVideoNote(ctx, srcPath, dstPath)
	} else {
		err = p.ffmpeg.ConvertVideo(ctx, srcPath, dstPath)
	}
	if err != nil {
		cleanupDst()
		return nil, err
	}

	stat, err := os.Stat(dstPath)
	if err != nil || stat.Size() == 0 {
		cleanupDst()
		return nil, ErrConversionFailed
	}
	if stat.Size() > limit {
		cleanupDst()
		return nil, ErrFileTooLarge
	}

	kind := KindVideo
	if isVideoNote {
		kind = KindVideoNote
	}

	return &MediaFile{
		Path:     dstPath,
		Kind:     kind,
		MimeType: "video/mp4",
		Filename: filepath.Base(dstPath),
		Size:     stat.Size(),
		Cleanup: func() error {
			cleanupDst()
			return nil
		},
	}, nil
}

// ProcessPhoto accepts JPEG/PNG images within bounds with guaranteed cleanup.
func (p *Pipeline) ProcessPhoto(ctx context.Context, src io.Reader, filename, mimeType string, size int64) (*MediaFile, error) {
	if size > MaxTelegramBotFileSize {
		return nil, ErrFileTooLarge
	}

	ext := ".jpg"
	if strings.Contains(mimeType, "png") || strings.HasSuffix(strings.ToLower(filename), ".png") {
		ext = ".png"
	}

	path, cleanup, err := p.WriteToTemp(src, MaxTelegramBotFileSize, "photo", ext)
	if err != nil {
		return nil, err
	}

	stat, err := os.Stat(path)
	if err != nil || stat.Size() == 0 {
		cleanup()
		return nil, ErrInvalidMedia
	}

	return &MediaFile{
		Path:     path,
		Kind:     KindPhoto,
		MimeType: mimeType,
		Filename: filename,
		Size:     stat.Size(),
		Cleanup: func() error {
			cleanup()
			return nil
		},
	}, nil
}

// ProcessDocument performs pass-through validation for generic documents.
func (p *Pipeline) ProcessDocument(ctx context.Context, filename string, mimeType string, size int64) (DocumentInfo, error) {
	if size > MaxWhatsAppDocumentSize {
		return DocumentInfo{}, ErrFileTooLarge
	}
	if filename == "" {
		filename = "document"
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return DocumentInfo{
		Filename: filename,
		MimeType: mimeType,
		Size:     size,
	}, nil
}
