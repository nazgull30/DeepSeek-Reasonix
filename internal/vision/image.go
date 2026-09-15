// Package vision is the leaf home of local-image plumbing shared by the
// controller (user-attached @-refs), the read_image built-in tool, and the
// agent's mid-turn injection. It imports nothing from the rest of the tree so
// every package that needs to read, sniff, or compress an image can do so
// without an import cycle.
package vision

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// MaxImageBytes caps an image this package will read. It bounds both the
// read_image tool's input and the base64 payload the agent injects into a
// vision-capable user message — DeepSeek allows up to 32 MiB per image and
// 48 MiB per request, so the 10 MiB cap keeps a single attachment well inside
// the budget even after compression backfires and passes raw bytes through.
const MaxImageBytes = 10 * 1024 * 1024

// DetectImageMime sniffs raw image bytes and returns their MIME type. A
// content byte-sniff wins over the file extension (mirroring how attachments
// are validated), and a few known extensions back up a format http mime-sniffs
// as application/octet-stream (e.g. an SVG or a TIFF that swapped its header).
func DetectImageMime(raw []byte, path string) string {
	if len(raw) == 0 {
		return ""
	}
	mime := http.DetectContentType(raw[:min(len(raw), 512)])
	if strings.HasPrefix(mime, "image/") {
		return mime
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".tiff", ".tif":
		return "image/tiff"
	}
	return ""
}

// ReadFile reads a regular image file at path, returning its raw bytes and
// detected MIME when it is an in-cap image. os.Stat (not Lstat) is used so
// symlinked images keep working like every other workspace read; the open-time
// re-stat catches a target swapped between the size check and the read.
func ReadFile(path string) (raw []byte, mime string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if info.IsDir() || info.Size() <= 0 || info.Size() > MaxImageBytes {
		return nil, "", fmt.Errorf("image must be a regular file between 1 byte and %d bytes", MaxImageBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(info, opened) {
		return nil, "", fmt.Errorf("file changed while opening")
	}
	raw, err = io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 || len(raw) > MaxImageBytes {
		return nil, "", fmt.Errorf("image must be between 1 byte and %d bytes", MaxImageBytes)
	}
	if after, err := f.Stat(); err != nil {
		return nil, "", err
	} else if !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return nil, "", fmt.Errorf("file changed while reading")
	}
	mime = DetectImageMime(raw, path)
	if mime == "" {
		return nil, "", fmt.Errorf("not a supported image (png, jpeg, gif, webp, tiff)")
	}
	return raw, mime, nil
}

// DataURL encodes raw bytes as a data: URL for a vision content block.
func DataURL(raw []byte, mime string) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
}
