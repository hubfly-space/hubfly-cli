//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"golang.org/x/net/websocket"
	"golang.org/x/term"
)

func watchResize(conn *websocket.Conn, authenticated *atomic.Bool, done <-chan struct{}) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGWINCH)
	defer signal.Stop(sigs)

	for {
		select {
		case <-done:
			return
		case <-sigs:
			if !authenticated.Load() {
				continue
			}
			if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && w > 0 && h > 0 {
				_ = sendTerminalMessage(conn, terminalClientMessage{Type: "resize", Rows: h, Cols: w})
			}
		}
	}
}
