//go:build windows

package cli

import (
	"sync/atomic"

	"golang.org/x/net/websocket"
)

// Windows has no SIGWINCH equivalent wired up here, so resize support is
// unix-only for now; the session still works, it just won't live-resize.
func watchResize(conn *websocket.Conn, authenticated *atomic.Bool, done <-chan struct{}) {
	<-done
}
