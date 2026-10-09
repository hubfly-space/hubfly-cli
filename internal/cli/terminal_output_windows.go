//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

func prepareTerminalOutput() func() {
	handle := windows.Handle(os.Stdout.Fd())
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		return func() {}
	}
	if err := windows.SetConsoleMode(handle, original|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		debugf("failed to enable terminal escape sequences: %v", err)
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(handle, original) }
}
