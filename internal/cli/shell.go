package cli

import (
	"bytes"
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

	"hubfly-cli/internal/version"
)

const (
	terminalDialTimeout  = 10 * time.Second
	terminalPingInterval = 25 * time.Second

	// terminalDetachByte (Ctrl+]) leaves an interactive session locally
	// without sending anything to the remote shell.
	terminalDetachByte = 0x1d
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

	fmt.Fprintf(os.Stderr, "Searching for '%s'...\n", targetIDOrName)
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
	return runTerminalSessionWithRenew(session, displayName, func() (terminalSession, error) {
		return createTerminalSession(token, projectID, containerID)
	})
}

// terminalReconnectDelays is the backoff between reconnect attempts after a
// transient websocket drop. Overridden in tests.
var terminalReconnectDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}

// terminalIdleTimeout ends a connection that has received nothing (not even a
// pong to our keepalive pings) for this long, so a half-open socket is
// detected and reconnected instead of hanging forever.
var terminalIdleTimeout = 3 * terminalPingInterval

// terminalOutcome is how one websocket connection of a session ended.
type terminalOutcome int

const (
	terminalDropped  terminalOutcome = iota // transient: reconnect
	terminalFinished                        // remote exit, detach, signal or fatal error
)

// runTerminalSession attaches the local terminal to a Dashboard terminal
// session (container terminals and managed database shells share the
// protocol). It does not reconnect: see runTerminalSessionWithRenew.
func runTerminalSession(session terminalSession, label string) error {
	return runTerminalSessionWithRenew(session, label, nil)
}

// runTerminalSessionWithRenew attaches the local terminal to a terminal
// session. When the remote shell exits, it returns a remoteExitError with the
// shell's exit code (nil for 0) after the terminal is restored. On an
// interactive terminal, Ctrl+] detaches without sending exit/EOF upstream.
// When the websocket drops for a transient reason and renew is set, it opens
// a new session (connect tokens are single-use) with backoff; the previous
// shell's processes are gone, and the user is told so.
func runTerminalSessionWithRenew(session terminalSession, label string, renew func() (terminalSession, error)) (err error) {
	debugf("opening terminal session %s for %s", session.SessionID, label)

	isTerminal := term.IsTerminal(int(os.Stdin.Fd()))
	restoreOutput := prepareTerminalOutput()
	var oldState *term.State
	var restoreOnce sync.Once
	restoreTerminal := func() {
		restoreOnce.Do(func() {
			restoreOutput()
			if oldState != nil {
				_ = term.Restore(int(os.Stdin.Fd()), oldState)
			}
		})
	}
	// Restore the terminal on every exit path, including a panic.
	defer func() {
		if r := recover(); r != nil {
			restoreTerminal()
			panic(r)
		}
		restoreTerminal()
	}()
	if isTerminal {
		state, rawErr := term.MakeRaw(int(os.Stdin.Fd()))
		if rawErr != nil {
			debugf("failed to enter raw terminal mode: %v", rawErr)
			isTerminal = false
		} else {
			oldState = state
		}
	}

	// One stdin reader for the whole session, so a reconnect never leaves a
	// stale reader swallowing keystrokes meant for the new connection.
	input := make(chan []byte, 16)
	stdinEOF := make(chan struct{})
	go readTerminalStdin(input, stdinEOF)

	signals := make(chan os.Signal, 1)
	notifyTerminalSignals(signals)
	defer stopTerminalSignals(signals)

	attempt := 0
	for {
		outcome, connErr := runTerminalConnection(session, label, isTerminal, input, stdinEOF, signals, attempt > 0)
		if outcome == terminalFinished {
			return connErr
		}
		if renew == nil {
			return connErr
		}
		debugf("terminal connection dropped: %v", connErr)
		var renewed bool
		for attempt < len(terminalReconnectDelays) {
			delay := terminalReconnectDelays[attempt]
			attempt++
			fmt.Fprintf(os.Stderr, "\r\nConnection to %s lost (%v). Reconnecting in %s (attempt %d/%d)...\r\n", label, connErr, delay, attempt, len(terminalReconnectDelays))
			select {
			case <-signals:
				return &remoteExitError{code: 130}
			case <-time.After(delay):
			}
			next, renewErr := renew()
			if renewErr != nil {
				connErr = renewErr
				var apiErr *apiError
				if errors.As(renewErr, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 408 && apiErr.Status != 429 {
					return fmt.Errorf("could not reopen the terminal: %w", renewErr)
				}
				continue
			}
			session, renewed = next, true
			break
		}
		if !renewed {
			return fmt.Errorf("terminal connection lost and %d reconnect attempts failed: %w", len(terminalReconnectDelays), connErr)
		}
	}
}

// runTerminalConnection runs one websocket connection of a terminal session.
func runTerminalConnection(
	session terminalSession, label string, isTerminal bool,
	input <-chan []byte, stdinEOF <-chan struct{}, signals <-chan os.Signal, reconnected bool,
) (terminalOutcome, error) {
	cols, rows := 80, 24
	if w, h, sizeErr := term.GetSize(int(os.Stdout.Fd())); sizeErr == nil && w > 0 && h > 0 {
		cols, rows = w, h
	}
	wsConfig, err := websocket.NewConfig(session.ConnectURL, apiHost)
	if err != nil {
		return terminalFinished, fmt.Errorf("invalid terminal connect url: %w", err)
	}
	wsConfig.Header.Set("User-Agent", version.UserAgent())
	dialCtx, cancelDial := context.WithTimeout(context.Background(), terminalDialTimeout)
	conn, err := wsConfig.DialContext(dialCtx)
	cancelDial()
	if err != nil {
		if reconnected {
			return terminalDropped, err
		}
		return terminalFinished, fmt.Errorf("failed to connect to terminal: %w", err)
	}
	defer conn.Close()

	if err := sendTerminalMessage(conn, terminalClientMessage{
		Type: "authenticate", SessionID: session.SessionID, ConnectToken: session.ConnectToken, Rows: rows, Cols: cols,
	}); err != nil {
		return terminalDropped, fmt.Errorf("failed to authenticate terminal session: %w", err)
	}

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
	var detached, interrupted, everAuthenticated atomic.Bool
	go watchResize(conn, &authenticated, done)
	go sendTerminalPings(conn, &authenticated, done)
	go func() {
		select {
		case <-done:
		case <-signals:
			interrupted.Store(true)
			shutdown()
		}
	}()
	startPump := sync.OnceFunc(func() {
		go forwardTerminalInput(conn, input, stdinEOF, done, isTerminal, func() {
			detached.Store(true)
			shutdown()
		})
	})

	for {
		_ = conn.SetReadDeadline(time.Now().Add(terminalIdleTimeout))
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			if detached.Load() {
				fmt.Fprintf(os.Stderr, "\r\nDetached from %s.\r\n", label)
				return terminalFinished, nil
			}
			if interrupted.Load() {
				return terminalFinished, &remoteExitError{code: 130}
			}
			if errors.Is(err, io.EOF) {
				err = errors.New("connection closed unexpectedly")
			}
			if !everAuthenticated.Load() && !reconnected {
				return terminalFinished, fmt.Errorf("terminal connection error: %w", err)
			}
			return terminalDropped, err
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
			if !everAuthenticated.Swap(true) {
				if reconnected {
					fmt.Fprintf(os.Stderr, "\r\nReconnected to %s in a new shell; programs from the previous shell ended.\r\n", label)
				} else if isTerminal {
					fmt.Fprintf(os.Stderr, "Connected to %s. Press Ctrl+] to detach.\r\n", label)
				}
			}
			startPump()
		case "output":
			_, _ = os.Stdout.WriteString(msg.Data)
		case "exit":
			return terminalFinished, remoteExitStatus(msg.ExitCode)
		case "error":
			fmt.Fprintf(os.Stderr, "\r\nterminal error: %s\r\n", msg.Message)
			return terminalFinished, fmt.Errorf("terminal session failed: %s", msg.Message)
		case "pong":
			// liveness only; the read deadline was already extended
		default:
			debugf("unhandled terminal message type: %s", msg.Type)
		}
	}
}

// readTerminalStdin copies stdin into input until it fails, then closes eof.
func readTerminalStdin(input chan<- []byte, eof chan<- struct{}) {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			input <- append([]byte(nil), buf[:n]...)
		}
		if err != nil {
			close(eof)
			return
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

// splitAtDetach returns the bytes to forward before a detach key, and whether
// one was seen. Anything after the detach key is dropped.
func splitAtDetach(b []byte) ([]byte, bool) {
	if i := bytes.IndexByte(b, terminalDetachByte); i >= 0 {
		return b[:i], true
	}
	return b, false
}

// forwardTerminalInput sends local input to one connection until it ends.
func forwardTerminalInput(conn *websocket.Conn, input <-chan []byte, eof <-chan struct{}, done <-chan struct{}, detachEnabled bool, onDetach func()) {
	for {
		select {
		case <-done:
			return
		case <-eof:
			for drained := false; !drained; {
				select {
				case chunk := <-input:
					_ = sendTerminalMessage(conn, terminalClientMessage{Type: "input", Data: string(chunk)})
				default:
					drained = true
				}
			}
			// Local stdin closed (Ctrl+D on a pipe): forward end-of-transmission
			// so the remote shell sees EOF too; it decides whether to exit.
			_ = sendTerminalMessage(conn, terminalClientMessage{Type: "input", Data: "\x04"})
			return
		case chunk := <-input:
			data, detach := chunk, false
			if detachEnabled {
				data, detach = splitAtDetach(data)
			}
			if len(data) > 0 {
				if err := sendTerminalMessage(conn, terminalClientMessage{Type: "input", Data: string(data)}); err != nil {
					return
				}
			}
			if detach {
				onDetach()
				return
			}
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
