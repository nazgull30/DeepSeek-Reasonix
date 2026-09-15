package vision

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectImageMimeContentSniff(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if got := DetectImageMime(raw, "foo.txt"); got != "image/png" {
		t.Errorf("content sniff: got %q, want image/png", got)
	}
}

func TestDetectImageMimeEmpty(t *testing.T) {
	if got := DetectImageMime(nil, "foo.png"); got != "" {
		t.Errorf("empty bytes: got %q, want empty", got)
	}
}

func TestDetectImageMimeOctetFallback(t *testing.T) {
	raw := []byte{0x00, 0x01, 0x02, 0x03}
	if got := DetectImageMime(raw, "foo.png"); got != "image/png" {
		t.Errorf("non-sniffable extension png: got %q, want image/png", got)
	}
	if got := DetectImageMime(raw, "foo.unknown"); got != "" {
		t.Errorf("non-sniffable extension unknown: got %q, want empty", got)
	}
}

func makeTinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadFileHappy(t *testing.T) {
	raw := makeTinyPNG(t)
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, mime, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("bytes mismatch: got %d, want %d", len(got), len(raw))
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}

func TestReadFileRejectsDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	os.Mkdir(dir, 0o755)
	_, _, err := ReadFile(dir)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Errorf("expected regular file error, got %v", err)
	}
}

func TestReadFileRejectsBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.bin")
	os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0x03}, 0o644)
	_, _, err := ReadFile(path)
	if err == nil || !strings.Contains(err.Error(), "supported image") {
		t.Errorf("expected supported image error, got %v", err)
	}
}

func TestDataURL(t *testing.T) {
	got := DataURL([]byte("abc"), "image/png")
	if got != "data:image/png;base64,YWJj" {
		t.Errorf("DataURL = %q", got)
	}
}
