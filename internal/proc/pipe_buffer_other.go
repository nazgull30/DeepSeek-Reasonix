//go:build !linux

package proc

import (
	"os"
)

// SetPipeBufferSize is a no-op on non-Linux platforms where the pipe buffer
// is either large enough or dynamically sized.
func SetPipeBufferSize(_ *os.File, _ int) error {
	return nil
}

// DefaultPipeBufferSize is the recommended pipe buffer for MCP stdio transports.
const DefaultPipeBufferSize = 1 << 20 // 1 MB
