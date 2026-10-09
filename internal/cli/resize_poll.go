package cli

import (
	"sync/atomic"
	"time"
)

func pollTerminalResize(authenticated *atomic.Bool, done <-chan struct{}, ticks <-chan time.Time,
	size func() (int, int, error), send func(cols, rows int) error,
) {
	lastCols, lastRows := 0, 0
	for {
		select {
		case <-done:
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if !authenticated.Load() {
				continue
			}
			cols, rows, err := size()
			if err != nil || cols <= 0 || rows <= 0 || (cols == lastCols && rows == lastRows) {
				continue
			}
			if err := send(cols, rows); err != nil {
				return
			}
			lastCols, lastRows = cols, rows
		}
	}
}
