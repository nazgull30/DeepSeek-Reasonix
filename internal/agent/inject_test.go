package agent

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"

	_ "reasonix/internal/tool/builtin"
)

func writeTestPNG(t *testing.T, dir string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 100, G: 200, B: 50, A: 255})
	path := filepath.Join(dir, "test.png")
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

// readImageTurn builds chunks that call read_image, followed by a final text
// answer referencing the image.
func readImageTurn(imagePath string) []provider.Chunk {
	return []provider.Chunk{
		{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{
			ID:        "img-1",
			Name:      "read_image",
			Arguments: fmt.Sprintf(`{"path":"%s"}`, imagePath),
		}},
		{Type: provider.ChunkDone},
	}
}

func finalTextTurn(text string) []provider.Chunk {
	return []provider.Chunk{
		{Type: provider.ChunkText, Text: text},
		{Type: provider.ChunkDone},
	}
}

func TestInjectReadImageResults_VisionEnabled(t *testing.T) {
	dir := t.TempDir()
	imgPath := writeTestPNG(t, dir)
	sess := NewSession("sys")
	reg := tool.NewRegistry()
	for _, bt := range tool.Builtins() {
		reg.Add(bt)
	}
	prov := &mockProvider{
		name: "test",
		streams: [][]provider.Chunk{
			readImageTurn(imgPath),
			finalTextTurn("I see the image"),
		},
	}
	a := New(prov, reg, sess, Options{VisionEnabled: true, MaxSteps: 5}, event.Discard)
	if err := a.Run(context.Background(), "look at the image"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Must contain a synthetic user message with Images
	found := false
	for _, m := range sess.Messages {
		if m.Role == provider.RoleUser && len(m.Images) > 0 {
			found = true
			if !strings.Contains(m.Content, "[image(s) you asked to view") {
				t.Errorf("synthetic image message content = %q, missing prefix", m.Content)
			}
			if !strings.Contains(m.Images[0], "data:image/png;base64,") {
				t.Errorf("image URL = %q, missing data: prefix", m.Images[0])
			}
			break
		}
	}
	if !found {
		t.Error("no synthetic user message with Images found in session")
	}
}

func TestInjectReadImageResults_VisionDisabled(t *testing.T) {
	dir := t.TempDir()
	imgPath := writeTestPNG(t, dir)
	sess := NewSession("sys")
	reg := tool.NewRegistry()
	for _, bt := range tool.Builtins() {
		reg.Add(bt)
	}
	prov := &mockProvider{
		name: "test",
		streams: [][]provider.Chunk{
			readImageTurn(imgPath),
			finalTextTurn("I see the image"),
		},
	}
	a := New(prov, reg, sess, Options{VisionEnabled: false, MaxSteps: 5}, event.Discard)
	if err := a.Run(context.Background(), "look at the image"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range sess.Messages {
		if m.Role == provider.RoleUser && len(m.Images) > 0 {
			t.Error("unexpected synthetic user message with Images when VisionEnabled=false")
		}
	}
}

func TestInjectReadImageResults_MultipleImages(t *testing.T) {
	dir := t.TempDir()
	img1 := writeTestPNG(t, dir)
	img2 := writeTestPNG(t, dir)
	// Two read_image calls, then final answer
	prov := &mockProvider{
		name: "test",
		streams: [][]provider.Chunk{
			{
				{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "i1", Name: "read_image", Arguments: fmt.Sprintf(`{"path":"%s"}`, img1)}},
				{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "i2", Name: "read_image", Arguments: fmt.Sprintf(`{"path":"%s"}`, img2)}},
				{Type: provider.ChunkDone},
			},
			finalTextTurn("done"),
		},
	}
	sess := NewSession("sys")
	reg := tool.NewRegistry()
	for _, bt := range tool.Builtins() {
		reg.Add(bt)
	}
	a := New(prov, reg, sess, Options{VisionEnabled: true, MaxSteps: 5}, event.Discard)
	if err := a.Run(context.Background(), "look at both"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	count := 0
	for _, m := range sess.Messages {
		if m.Role == provider.RoleUser && len(m.Images) > 0 {
			count += len(m.Images)
		}
	}
	if count != 2 {
		t.Errorf("expected 2 image URLs in synthetic messages, got %d", count)
	}
}

func TestReadImageToolNameConst(t *testing.T) {
	if readImageToolName != "read_image" {
		t.Errorf("readImageToolName = %q, want read_image", readImageToolName)
	}
}
