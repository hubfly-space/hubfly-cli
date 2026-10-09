//go:build !windows

package cli

func prepareTerminalOutput() func() { return func() {} }
