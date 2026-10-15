package media

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSplitCaption(t *testing.T) {
	// 1. Short caption: no overflow
	short := "Hello world"
	p, o := SplitCaption(short, 1024)
	if p != short || o != "" {
		t.Fatalf("expected (%q, \"\"), got (%q, %q)", short, p, o)
	}

	// 2. Caption exceeding limit: splits cleanly at whitespace
	words := make([]string, 200)
	for i := range words {
		words[i] = "word"
	}
	long := strings.Join(words, " ")
	p, o = SplitCaption(long, 50)
	if len([]rune(p)) > 50 {
		t.Fatalf("primary caption length %d > 50", len([]rune(p)))
	}
	if o == "" {
		t.Fatalf("expected non-empty overflow")
	}

	// 3. Caption with no whitespace
	solid := strings.Repeat("A", 100)
	p, o = SplitCaption(solid, 40)
	if len([]rune(p)) != 40 || len([]rune(o)) != 60 {
		t.Fatalf("expected 40 and 60 runes, got %d and %d", len([]rune(p)), len([]rune(o)))
	}
}

func TestSplitText(t *testing.T) {
	// 1. Empty text
	chunks := SplitText("", 100)
	if len(chunks) != 1 || chunks[0] != "" {
		t.Fatalf("expected 1 empty chunk, got %v", chunks)
	}

	// 2. Text within limit
	chunks = SplitText("sample text", 100)
	if len(chunks) != 1 || chunks[0] != "sample text" {
		t.Fatalf("expected 1 chunk, got %v", chunks)
	}

	// 3. Multi-chunk splitting with unicode runes
	runes := []rune{}
	for i := 0; i < 150; i++ {
		runes = append(runes, '🚀')
	}
	chunks = SplitText(string(runes), 50)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) != 50 {
			t.Fatalf("chunk %d has length %d != 50", i, len([]rune(c)))
		}
	}
}

func TestProcessSticker(t *testing.T) {
	ctx := context.Background()

	// 1. WebP sticker
	s1 := ProcessSticker(ctx, "image/webp", "test.webp")
	if s1.Degraded || s1.IsAnimated || s1.MimeType != "image/webp" {
		t.Fatalf("unexpected sticker info for webp: %+v", s1)
	}

	// 2. Animated TGS sticker degradation
	s2 := ProcessSticker(ctx, "application/x-tgsticker", "sticker.tgs")
	if !s2.Degraded || !s2.IsAnimated || s2.FallbackNotice == "" {
		t.Fatalf("expected graceful degradation for animated tgs sticker: %+v", s2)
	}

	// 3. Video sticker
	s3 := ProcessSticker(ctx, "video/webm", "video.webm")
	if s3.Degraded || !s3.IsVideo {
		t.Fatalf("expected video sticker: %+v", s3)
	}
}

func TestProcessDocument(t *testing.T) {
	pipe, err := NewPipeline(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewPipeline failed: %v", err)
	}

	// Valid document within bounds
	doc, err := pipe.ProcessDocument(context.Background(), "report.pdf", "application/pdf", 1024)
	if err != nil {
		t.Fatalf("ProcessDocument failed: %v", err)
	}
	if doc.Filename != "report.pdf" || doc.Size != 1024 {
		t.Fatalf("unexpected doc: %+v", doc)
	}

	// Exceeding limit
	_, err = pipe.ProcessDocument(context.Background(), "large.bin", "application/octet-stream", MaxWhatsAppDocumentSize+1)
	if err != ErrFileTooLarge {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestPipelineTempCleanup(t *testing.T) {
	scratch := t.TempDir()
	pipe, err := NewPipeline(scratch, nil)
	if err != nil {
		t.Fatalf("NewPipeline failed: %v", err)
	}

	f, cleanup, err := pipe.TempFile("test-clean", ".tmp")
	if err != nil {
		t.Fatalf("TempFile failed: %v", err)
	}
	path := f.Name()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp file was not created on disk: %v", err)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected temp file to be deleted by cleanup, err: %v", err)
	}
}

func TestPipelineContextCancellation(t *testing.T) {
	scratch := t.TempDir()
	pipe, err := NewPipeline(scratch, nil)
	if err != nil {
		t.Fatalf("NewPipeline failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	input := bytes.NewReader([]byte("dummy audio content"))
	_, err = pipe.ProcessVoiceWAToTG(ctx, input, int64(input.Len()))
	if err == nil {
		t.Fatalf("expected error on cancelled context, got nil")
	}

	// Verify no orphaned files in scratch directory
	entries, readErr := os.ReadDir(scratch)
	if readErr != nil {
		t.Fatalf("ReadDir failed: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected scratch dir to be empty after cancelled conversion, found %d entries", len(entries))
	}
}

func TestPipelineFFmpegConversion(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available on host, skipping actual transcode test")
	}

	scratch := t.TempDir()
	pipe, err := NewPipeline(scratch, nil)
	if err != nil {
		t.Fatalf("NewPipeline failed: %v", err)
	}

	// Generate a 0.2 second synthetic silent audio using ffmpeg
	synthPath := filepath.Join(scratch, "synth.wav")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-t", "0.2", synthPath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to generate synthetic audio: %v", err)
	}
	defer os.Remove(synthPath)

	data, err := os.ReadFile(synthPath)
	if err != nil {
		t.Fatalf("failed to read synthetic audio: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. WhatsApp to Telegram voice
	mfTG, err := pipe.ProcessVoiceWAToTG(ctx, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ProcessVoiceWAToTG failed: %v", err)
	}
	if mfTG.Kind != KindVoice || mfTG.Size == 0 {
		t.Fatalf("invalid media file: %+v", mfTG)
	}
	if err := mfTG.Cleanup(); err != nil {
		t.Fatalf("mfTG cleanup failed: %v", err)
	}
	if _, err := os.Stat(mfTG.Path); !os.IsNotExist(err) {
		t.Fatalf("expected mfTG file removed after cleanup")
	}

	// 2. Telegram to WhatsApp PTT
	mfWA, err := pipe.ProcessVoiceTGToWA(ctx, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ProcessVoiceTGToWA failed: %v", err)
	}
	if mfWA.Kind != KindVoice || mfWA.Size == 0 {
		t.Fatalf("invalid media file: %+v", mfWA)
	}
	if err := mfWA.Cleanup(); err != nil {
		t.Fatalf("mfWA cleanup failed: %v", err)
	}
	if _, err := os.Stat(mfWA.Path); !os.IsNotExist(err) {
		t.Fatalf("expected mfWA file removed after cleanup")
	}
}
