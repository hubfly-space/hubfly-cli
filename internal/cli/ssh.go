package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/net/websocket"
)

const tunnelDialTimeout = 10 * time.Second

type tunnelClientMessage struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocolVersion"`
	TunnelID        string `json:"tunnelId,omitempty"`
	ConnectToken    string `json:"connectToken,omitempty"`
}

type tunnelServerMessage struct {
	Type            string `json:"type"`
	Code            string `json:"code,omitempty"`
	Message         string `json:"message,omitempty"`
	TunnelID        string `json:"tunnelId,omitempty"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
}

type tunnelStreamConnectRequest struct {
	Type     string `json:"type"`
	TargetID string `json:"targetId"`
}

type tunnelStreamConnectResponse struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func tunnelTicketPath(tunnelID string) string {
	return filepath.Join(tunnelsDir(), sanitizeID(tunnelID)+".json")
}

func saveTunnelTicket(t tunnel) error {
	if strings.TrimSpace(t.TunnelID) == "" || strings.TrimSpace(t.ConnectToken) == "" {
		return nil
	}
	if err := os.MkdirAll(tunnelsDir(), 0o700); err != nil {
		return err
	}
	payload, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(tunnelTicketPath(t.TunnelID), payload, 0o600)
}

func loadTunnelTicket(tunnelID string) (tunnel, error) {
	content, err := os.ReadFile(tunnelTicketPath(tunnelID))
	if err != nil {
		return tunnel{}, err
	}
	var t tunnel
	if err := json.Unmarshal(content, &t); err != nil {
		return tunnel{}, err
	}
	return t, nil
}

func removeTunnelTicket(tunnelID string) error {
	err := os.Remove(tunnelTicketPath(tunnelID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func mergeTunnelMetadata(base, overlay tunnel) tunnel {
	if strings.TrimSpace(base.TunnelID) == "" {
		base.TunnelID = overlay.TunnelID
	}
	if strings.TrimSpace(base.ID) == "" {
		base.ID = overlay.ID
	}
	if strings.TrimSpace(base.ProjectID) == "" {
		base.ProjectID = overlay.ProjectID
	}
	if strings.TrimSpace(base.ProjectName) == "" {
		base.ProjectName = overlay.ProjectName
	}
	if strings.TrimSpace(base.TargetContainer) == "" {
		base.TargetContainer = overlay.TargetContainer
	}
	if strings.TrimSpace(base.TargetContainerID) == "" {
		base.TargetContainerID = overlay.TargetContainerID
	}
	if strings.TrimSpace(base.ConnectURL) == "" {
		base.ConnectURL = overlay.ConnectURL
	}
	if strings.TrimSpace(base.ConnectToken) == "" {
		base.ConnectToken = overlay.ConnectToken
	}
	if base.ProtocolVersion == 0 {
		base.ProtocolVersion = overlay.ProtocolVersion
	}
	if strings.TrimSpace(base.Mode) == "" {
		base.Mode = overlay.Mode
	}
	if len(base.Targets) == 0 {
		base.Targets = overlay.Targets
	}
	if base.Limits.MaxStreams == 0 {
		base.Limits = overlay.Limits
	}
	if strings.TrimSpace(base.ExpiresAt) == "" {
		base.ExpiresAt = overlay.ExpiresAt
	}
	return base
}

func hydrateTunnelTicket(t tunnel) (tunnel, error) {
	if strings.TrimSpace(t.ConnectToken) != "" {
		return t, nil
	}
	cached, err := loadTunnelTicket(t.TunnelID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tunnel{}, fmt.Errorf("tunnel session token unavailable for %s; create a new tunnel", t.TunnelID)
		}
		return tunnel{}, err
	}
	return mergeTunnelMetadata(cached, t), nil
}

func primaryTunnelTarget(t tunnel, requestedTargetPort int) (tunnelTarget, error) {
	if len(t.Targets) == 0 {
		return tunnelTarget{}, errors.New("tunnel has no targets")
	}
	if requestedTargetPort > 0 {
		for _, target := range t.Targets {
			if target.TargetPort == requestedTargetPort {
				return target, nil
			}
		}
	}
	return t.Targets[0], nil
}

func runTunnelConnection(t tunnel, _ string, localPort, targetPort int) error {
	loaded, err := hydrateTunnelTicket(t)
	if err != nil {
		return err
	}
	target, err := primaryTunnelTarget(loaded, targetPort)
	if err != nil {
		return err
	}
	if localPort <= 0 {
		localPort = target.LocalPort
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Println("Establishing tunnel...")
	fmt.Printf("Local: localhost:%d -> Remote: %s:%d\n", localPort, resolveTunnelForwardHost(loaded), target.TargetPort)
	fmt.Printf("Gateway: %s\n", loaded.ConnectURL)

	if err := serveTunnelGateway(ctx, loaded, target, localPort); err != nil {
		return err
	}
	_ = removeTunnelTicket(loaded.TunnelID)
	return nil
}

func startTunnelConnectionBackground(t tunnel, _ string, localPort, targetPort int) (*exec.Cmd, error) {
	loaded, err := hydrateTunnelTicket(t)
	if err != nil {
		return nil, err
	}
	if err := saveTunnelTicket(loaded); err != nil {
		return nil, err
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(
		exe,
		"__connect-tunnel",
		loaded.TunnelID,
		strconv.Itoa(localPort),
		strconv.Itoa(targetPort),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func connectStoredTunnelFlow(tunnelID string, localPort, targetPort int) error {
	t, err := loadTunnelTicket(tunnelID)
	if err != nil {
		return err
	}
	return runTunnelConnection(t, "", localPort, targetPort)
}

func serveTunnelGateway(
	ctx context.Context,
	t tunnel,
	target tunnelTarget,
	localPort int,
) error {
	wsConfig, err := websocket.NewConfig(t.ConnectURL, apiHost)
	if err != nil {
		return fmt.Errorf("invalid tunnel connect url: %w", err)
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, tunnelDialTimeout)
	defer cancelDial()
	conn, err := wsConfig.DialContext(dialCtx)
	if err != nil {
		return fmt.Errorf("failed to connect to tunnel gateway: %w", err)
	}
	defer conn.Close()

	if err := sendTunnelMessage(conn, tunnelClientMessage{
		Type:            "authenticate",
		ProtocolVersion: max(1, t.ProtocolVersion),
		TunnelID:        t.TunnelID,
		ConnectToken:    t.ConnectToken,
	}); err != nil {
		return fmt.Errorf("failed to authenticate tunnel session: %w", err)
	}

	for {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			return fmt.Errorf("tunnel handshake failed: %w", err)
		}
		var msg tunnelServerMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "hello":
			debugf("tunnel hello: protocol version %d", msg.ProtocolVersion)
		case "authenticated":
			goto authenticated
		case "error":
			return fmt.Errorf("tunnel session failed: %s", msg.Message)
		}
	}

authenticated:
	session, err := yamux.Client(conn, nil)
	if err != nil {
		return fmt.Errorf("failed to initialize tunnel session: %w", err)
	}
	defer session.Close()

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return fmt.Errorf("failed to listen on localhost:%d: %w", localPort, err)
	}
	defer listener.Close()

	fmt.Println("Tunnel connected.")
	fmt.Println("Press Ctrl+C to stop.")

	go func() {
		<-ctx.Done()
		_ = listener.Close()
		_ = session.Close()
	}()

	var wg sync.WaitGroup
	acceptErrCh := make(chan error, 1)
	go func() {
		for {
			clientConn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
					acceptErrCh <- nil
					return
				}
				acceptErrCh <- err
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := proxyTunnelConnection(ctx, session, target, clientConn); err != nil {
					debugf("tunnel proxy error: %v", err)
				}
			}()
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-acceptErrCh:
		if err != nil {
			return err
		}
	}

	wg.Wait()
	return nil
}

func proxyTunnelConnection(
	ctx context.Context,
	session *yamux.Session,
	target tunnelTarget,
	clientConn net.Conn,
) error {
	defer clientConn.Close()

	stream, err := session.OpenStream()
	if err != nil {
		return err
	}
	defer stream.Close()

	header, err := json.Marshal(tunnelStreamConnectRequest{
		Type:     "connect",
		TargetID: target.TargetID,
	})
	if err != nil {
		return err
	}
	if _, err := stream.Write(append(header, '\n')); err != nil {
		return err
	}

	reader := bufio.NewReader(stream)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("failed to read tunnel stream response: %w", err)
	}
	var response tunnelStreamConnectResponse
	if err := json.Unmarshal(bytesTrimSpace(line), &response); err != nil {
		return fmt.Errorf("invalid tunnel stream response: %w", err)
	}
	if response.Type != "connected" {
		message := response.Message
		if message == "" {
			message = response.Code
		}
		return fmt.Errorf("tunnel stream rejected: %s", message)
	}

	copyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stream, clientConn)
		cancel()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, reader)
		cancel()
	}()
	<-copyCtx.Done()
	_ = clientConn.Close()
	_ = stream.Close()
	wg.Wait()
	return nil
}

func sendTunnelMessage(conn *websocket.Conn, msg tunnelClientMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, payload)
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func resolveTunnelForwardHost(t tunnel) string {
	if target, err := primaryTunnelTarget(t, 0); err == nil {
		if strings.TrimSpace(target.ContainerName) != "" {
			return strings.TrimSpace(target.ContainerName)
		}
		if strings.TrimSpace(target.ContainerID) != "" {
			return strings.TrimSpace(target.ContainerID)
		}
	}
	if strings.TrimSpace(t.TargetContainer) != "" {
		return strings.TrimSpace(t.TargetContainer)
	}
	if strings.TrimSpace(t.TargetContainerID) != "" {
		return strings.TrimSpace(t.TargetContainerID)
	}
	return "container"
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "tunnel"
	}
	var builder strings.Builder
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			builder.WriteRune(ch)
			continue
		}
		builder.WriteByte('-')
	}
	out := strings.Trim(builder.String(), "-")
	if out == "" {
		return "tunnel"
	}
	return out
}
