package builtin

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/vision"
)

func writeTestPNG(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	img.Set(0, 0, color.RGBA{R: 255, G: 128, B: 0, A: 255})
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadImageHappy(t *testing.T) {
	dir := t.TempDir()
	path := writeTestPNG(t, dir, "pic.png")
	out, err := readImage{workDir: dir}.Execute(context.Background(), argsJSON(t, map[string]any{"path": "pic.png"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[reasonix-image: ") {
		t.Errorf("output missing marker prefix: %s", out)
	}
	if !strings.Contains(out, "mime=image/png") {
		t.Errorf("output missing mime: %s", out)
	}
	if !strings.Contains(out, "attached inline") {
		t.Errorf("output missing inline note: %s", out)
	}
	// Parse the marker path — must be resolvable
	markerPath, ok := vision.ParseMarker(out)
	if !ok || markerPath != path {
		t.Errorf("ParseMarker returned (%q, %v), want (%q, true)", markerPath, ok, path)
	}
}

func TestReadImageRejectsDir(t *testing.T) {
	dir := t.TempDir()
	_, err := readImage{workDir: dir}.Execute(context.Background(), argsJSON(t, map[string]any{"path": "."}))
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("expected directory error, got %v", err)
	}
}

func TestReadImageRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	os.WriteFile(path, []byte{0x00, 0x01, 0x02}, 0o644)
	_, err := readImage{workDir: dir}.Execute(context.Background(), argsJSON(t, map[string]any{"path": "data.bin"}))
	if err == nil || !strings.Contains(err.Error(), "read_image") {
		t.Errorf("expected read_image error, got %v", err)
	}
}

func TestReadImageRequiresPath(t *testing.T) {
	_, err := readImage{}.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("expected path required error, got %v", err)
	}
}

func TestReadImageReadOnly(t *testing.T) {
	r := readImage{}
	if !r.ReadOnly() {
		t.Error("read_image must be read-only")
	}
}
