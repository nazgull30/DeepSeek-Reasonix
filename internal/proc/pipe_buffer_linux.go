//go:build linux

package proc

import (
	"os"
	"syscall"
)

// SetPipeBufferSize increases the pipe buffer to the given size on Linux.
// This prevents deadlocks where a large MCP JSON-RPC response (e.g. git diff)
// exceeds the default 64KB pipe buffer, causing the writer to block while
// the reader waits for the line terminator.
func SetPipeBufferSize(f *os.File, size int) error {
	_, _, err := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETPIPE_SZ, uintptr(size))
	if err != 0 {
		return err
	}
	return nil
}

// DefaultPipeBufferSize is the recommended pipe buffer for MCP stdio transports.
const DefaultPipeBufferSize = 1 << 20 // 1 MB
