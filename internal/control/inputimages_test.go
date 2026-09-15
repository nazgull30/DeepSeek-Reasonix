package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerInputImagesResolvesAttachment(t *testing.T) {
	t.Chdir(t.TempDir())
	ref, err := SaveImageDataURL("data:image/png;base64," + tinyPNG)
	if err != nil {
		t.Fatalf("SaveImageDataURL: %v", err)
	}
	urls := New(Options{VisionEnabled: true}).inputImages("look at @" + ref)
	if len(urls) != 1 {
		t.Fatalf("inputImages = %v, want one resolved data URL", urls)
	}
	if !strings.HasPrefix(urls[0], "data:image/png;base64,") {
		t.Errorf("resolved url = %q, want a png data URL", urls[0])
	}
}

func TestControllerInputImagesIgnoresNonAttachmentRefs(t *testing.T) {
	t.Chdir(t.TempDir())
	if urls := New(Options{VisionEnabled: true}).inputImages("plain text with @missing.png"); len(urls) != 0 {
		t.Errorf("inputImages = %v, want none for a non-existent / non-attachment ref", urls)
	}
}

func TestControllerInputImagesSkipsWhenNotVisionCapable(t *testing.T) {
	t.Chdir(t.TempDir())
	ref, err := SaveImageDataURL("data:image/png;base64," + tinyPNG)
	if err != nil {
		t.Fatalf("SaveImageDataURL: %v", err)
	}
	if urls := New(Options{}).inputImages("look at @" + ref); len(urls) != 0 {
		t.Errorf("inputImages = %v, want none when the model is not vision-capable", urls)
	}
}

func TestControllerInputImagesEmbedsWorktreeImage(t *testing.T) {
	workspace := t.TempDir()
	png := filepath.Join(workspace, "docs", "diagram.png")
	if err := os.MkdirAll(filepath.Dir(png), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Controller{cpRoot: workspace, visionEnabled: true}
	urls := c.inputImages("see @" + png)
	if len(urls) != 1 {
		t.Fatalf("inputImages = %v, want one embedded worktree image", urls)
	}
	if !strings.HasPrefix(urls[0], "data:image/png;base64,") {
		t.Errorf("resolved url = %q, want a png data URL", urls[0])
	}
}

func TestControllerInputImagesSkipsNonImageWorktreeRef(t *testing.T) {
	workspace := t.TempDir()
	note := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(note, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Controller{cpRoot: workspace, visionEnabled: true}
	if urls := c.inputImages("see @" + note); len(urls) != 0 {
		t.Errorf("inputImages = %v, want none for a text file", urls)
	}
}

func TestResolveRefsWorktreeImageNeutralMarkerWhenVision(t *testing.T) {
	workspace := t.TempDir()
	png := filepath.Join(workspace, "docs", "diagram.png")
	if err := os.MkdirAll(filepath.Dir(png), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Controller{cpRoot: workspace, visionEnabled: true}
	block, errs := c.ResolveRefs(context.Background(), "see @"+png)
	if len(errs) != 0 {
		t.Fatalf("ResolveRefs errors = %v", errs)
	}
	if !strings.Contains(block, `<image path="docs/diagram.png">`) {
		t.Fatalf("vision ResolveRefs should resolve the worktree png to an image marker:\n%s", block)
	}
	if strings.Contains(block, "image bytes are not inlined") {
		t.Fatalf("vision ResolveRefs must not emit the not-inlined note:\n%s", block)
	}
}

func TestResolveRefsWorktreeImageKeepsNoteWithoutVision(t *testing.T) {
	workspace := t.TempDir()
	png := filepath.Join(workspace, "docs", "diagram.png")
	if err := os.MkdirAll(filepath.Dir(png), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Controller{cpRoot: workspace}
	block, errs := c.ResolveRefs(context.Background(), "see @"+png)
	if len(errs) != 0 {
		t.Fatalf("ResolveRefs errors = %v", errs)
	}
	if !strings.Contains(block, "image bytes are not inlined") {
		t.Fatalf("non-vision ResolveRefs should keep the not-inlined note:\n%s", block)
	}
}
