package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
	"golang.org/x/term"
)

const (
	terminalDialTimeout  = 10 * time.Second
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

func sshFlow(targetIDOrName string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Printf("Searching for '%s'...\n", targetIDOrName)
	targetContainer, targetProjectID, err := findContainer(token, targetIDOrName)
	if err == nil && targetContainer != nil {
		return sshContainerTerminal(
			token,
			targetProjectID,
			targetContainer.ID,
			targetIDOrName,
		)
	}

	// Secondary check: Virtual machine (Box)
	box, boxProjectID, bErr := findBox(token, targetIDOrName)
	if bErr == nil && box != nil {
		return sshBoxFlow(token, boxProjectID, box)
	}

	return fmt.Errorf("target '%s' not found as container or virtual machine", targetIDOrName)
}

func sshBoxFlow(token, projectID string, box *Box) error {
	mappings, err := fetchBoxPortMappings(token, projectID)
	var sshPortMapping *BoxPortMapping
	if err == nil {
		for _, m := range mappings {
			if m.BoxID == box.ID && m.GuestPort == 22 && m.Protocol == "tcp" {
				sshPortMapping = &m
				break
			}
		}
	}

	if sshPortMapping != nil && sshPortMapping.HostPort > 0 {
		host := sshPortMapping.BindIP
		if host == "" || host == "0.0.0.0" {
			u, _ := url.Parse(apiHost)
			if u != nil && u.Hostname() != "" {
				host = u.Hostname()
			}
		}
		user := "root"
		fmt.Printf("Connecting to Box '%s' via SSH on port %d (%s)...\n", box.Name, sshPortMapping.HostPort, host)
		cmd := exec.Command("ssh", "-p", strconv.Itoa(sshPortMapping.HostPort), fmt.Sprintf("%s@%s", user, host))
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	fmt.Printf("\nBox:    %s (%s)\n", box.Name, box.ID)
	fmt.Printf("Status: %s\n", box.Status)
	if box.PrivateIPv4 != nil && *box.PrivateIPv4 != "" {
		fmt.Printf("Mesh IP: %s\n", *box.PrivateIPv4)
	}
	fmt.Println("\nNo public SSH port mapping found on port 22.")
	fmt.Println("To connect to this Box via SSH:")
	fmt.Printf("  1. Map a public SSH port:   hubfly vm port-map %s 22\n", box.Name)
	fmt.Printf("  2. Or open a mesh tunnel:   hubfly tunnel %s 2222 22\n", box.Name)
	fmt.Printf("     Then connect with:       ssh -p 2222 root@localhost\n")
	return nil
}

func sshContainerTerminal(
	token, projectID, containerID, displayName string,
) error {
	session, err := createTerminalSession(token, projectID, containerID)
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
