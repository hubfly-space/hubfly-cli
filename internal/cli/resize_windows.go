//go:build windows

package cli

import (
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
	"golang.org/x/term"
)

// Windows has no SIGWINCH; poll the console viewport while the session is open.
func watchResize(conn *websocket.Conn, authenticated *atomic.Bool, done <-chan struct{}) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	pollTerminalResize(authenticated, done, ticker.C, func() (int, int, error) {
		// GetConsoleScreenBufferInfo requires a console output handle on Windows.
		return term.GetSize(int(os.Stdout.Fd()))
	}, func(cols, rows int) error {
		return sendTerminalMessage(conn, terminalClientMessage{Type: "resize", Rows: rows, Cols: cols})
	})
}
