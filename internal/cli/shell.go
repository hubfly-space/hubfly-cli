package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
	"golang.org/x/term"
)

const (
	terminalDialTimeout = 10 * time.Second
	terminalPingInterval = 25 * time.Second
)

type terminalClientMessage struct {
	Type         string `json:"type"`
	SessionID    string `json:"sessionId,omitempty"`
	ConnectToken string `json:"connectToken,omitempty"`
	Data         string `json:"data,omitempty"`
	Rows         int    `json:"rows,omitempty"`
	Cols         int    `json:"cols,omitempty"`
}

type terminalServerMessage struct {
	Type            string `json:"type"`
	Code            string `json:"code,omitempty"`
	Message         string `json:"message,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
	Shell           string `json:"shell,omitempty"`
	Rows            int    `json:"rows,omitempty"`
	Cols            int    `json:"cols,omitempty"`
	Data            string `json:"data,omitempty"`
	ExitCode        int    `json:"exitCode,omitempty"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
}

func sshFlow(containerIDOrName string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Printf("Searching for container '%s'...\n", containerIDOrName)
	targetContainer, targetProjectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}

	session, err := createTerminalSession(token, targetProjectID, targetContainer.ID)
	if err != nil {
		return fmt.Errorf("failed to create terminal session: %w", err)
	}

	cols, rows := 80, 24
	if w, h, sizeErr := term.GetSize(int(os.Stdin.Fd())); sizeErr == nil && w > 0 && h > 0 {
		cols, rows = w, h
	}

	wsConfig, err := websocket.NewConfig(session.ConnectURL, apiHost)
	if err != nil {
		return fmt.Errorf("invalid terminal connect url: %w", err)
	}

	dialCtx, cancelDial := context.WithTimeout(context.Background(), terminalDialTimeout)
	defer cancelDial()
	conn, err := wsConfig.DialContext(dialCtx)
	if err != nil {
		return fmt.Errorf("failed to connect to terminal: %w", err)
	}
	defer conn.Close()

	if err := sendTerminalMessage(conn, terminalClientMessage{
		Type:         "authenticate",
		SessionID:    session.SessionID,
		ConnectToken: session.ConnectToken,
		Rows:         rows,
		Cols:         cols,
	}); err != nil {
		return fmt.Errorf("failed to authenticate terminal session: %w", err)
	}

	isTerminal := term.IsTerminal(int(os.Stdin.Fd()))
	var oldState *term.State
	if isTerminal {
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			debugf("failed to enter raw terminal mode: %v", err)
			isTerminal = false
		}
	}
	restoreTerminal := func() {
		if isTerminal && oldState != nil {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
		}
	}
	defer restoreTerminal()

	done := make(chan struct{})
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			close(done)
			_ = conn.Close()
		})
	}
	defer shutdown()

	var authenticated atomic.Bool
	var stdinStarted atomic.Bool

	go watchResize(conn, &authenticated, done)
	go sendTerminalPings(conn, &authenticated, done)

	startStdinPump := func() {
		if !stdinStarted.CompareAndSwap(false, true) {
			return
		}
		go pumpStdin(conn, done)
	}

	for {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("terminal connection closed unexpectedly")
			}
			return fmt.Errorf("terminal connection error: %w", err)
		}

		var msg terminalServerMessage
		if jsonErr := json.Unmarshal(raw, &msg); jsonErr != nil {
			debugf("failed to decode terminal message: %v", jsonErr)
			continue
		}

		switch msg.Type {
		case "hello":
			debugf("terminal hello: protocol version %d", msg.ProtocolVersion)
		case "authenticated":
			authenticated.Store(true)
			startStdinPump()
		case "output":
			_, _ = os.Stdout.WriteString(msg.Data)
		case "exit":
			shutdown()
			restoreTerminal()
			os.Exit(msg.ExitCode)
		case "error":
			fmt.Fprintf(os.Stderr, "\nterminal error: %s\n", msg.Message)
			return fmt.Errorf("terminal session failed: %s", msg.Message)
		case "pong":
			// liveness only, nothing to do
		default:
			debugf("unhandled terminal message type: %s", msg.Type)
		}
	}
}

func sendTerminalMessage(conn *websocket.Conn, msg terminalClientMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, payload)
}

func pumpStdin(conn *websocket.Conn, done <-chan struct{}) {
	buf := make([]byte, 4096)
	for {
		n, readErr := os.Stdin.Read(buf)
		if n > 0 {
			if sendErr := sendTerminalMessage(conn, terminalClientMessage{
				Type: "input",
				Data: string(buf[:n]),
			}); sendErr != nil {
				return
			}
		}
		if readErr != nil {
			// Local stdin closed (Ctrl+D) - forward end-of-transmission so the
			// remote shell sees EOF too, then stop reading. The WS stays open;
			// the remote shell decides whether to exit.
			_ = sendTerminalMessage(conn, terminalClientMessage{Type: "input", Data: "\x04"})
			return
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func sendTerminalPings(conn *websocket.Conn, authenticated *atomic.Bool, done <-chan struct{}) {
	ticker := time.NewTicker(terminalPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if authenticated.Load() {
				_ = sendTerminalMessage(conn, terminalClientMessage{Type: "ping"})
			}
		}
	}
}
