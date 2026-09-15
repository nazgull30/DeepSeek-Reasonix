package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"reasonix/internal/tool"
	"reasonix/internal/vision"
)

func init() { tool.RegisterBuiltin(readImage{}) }

// readImage "reads" an image file for a vision-capable model. It validates the
// file is in-budget and a supported format, then returns an inline marker the
// agent's mid-turn injector recognises and delivers to the model as an image
// content block. The pixels themselves are never base64'd into the transcript —
// history and exports carry only the path marker, so a non-vision model sees
// plain text ("attached inline for visual analysis"), not a wall of base64.
type readImage struct{ workDir string }

func (readImage) Name() string { return "read_image" }

func (readImage) Description() string {
	return "Attach an image file (png/jpeg/gif/webp/tiff, up to 10 MiB) so the model can see its contents. Use for screenshots, diagrams, UI mockups, and other files whose meaning lives in pixels rather than text. The image is delivered to the model on the following turn as an inline attachment — call it, then follow up with the analysis or instruction (e.g. 'describe this chart', 'what error is shown?')."
}

func (readImage) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Image file path"}
},
"required":["path"]
}`)
}

func (readImage) ReadOnly() bool { return true }

func (r readImage) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	p.Path = resolveIn(r.workDir, p.Path)

	// A directory can be opened but never decodes as an image — catch it up front
	// with an actionable message so the model switches to the ls tool instead of
	// retrying the same path.
	if info, err := os.Stat(p.Path); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s is a directory — pass the path of an image file inside it", p.Path)
	}

	raw, mime, err := vision.ReadFile(p.Path)
	if err != nil {
		return "", fmt.Errorf("read_image %s: %w", p.Path, err)
	}
	return fmt.Sprintf("%s mime=%s, %d bytes — attached inline for visual analysis", vision.Marker(p.Path), mime, len(raw)), nil
}
