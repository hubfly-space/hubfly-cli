package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/net/websocket"
)

const tunnelDialTimeout = 10 * time.Second

type TunnelRequest struct {
	ID              string         `json:"id"`
	ConnectURL      string         `json:"connect_url"`
	ConnectToken    string         `json:"connect_token"`
	ProtocolVersion int            `json:"protocol_version"`
	LocalPort       int            `json:"local_port"`
	TargetPort      int            `json:"target_port"`
	Targets         []TunnelTarget `json:"targets"`
}

type TunnelTarget struct {
	TargetID      string `json:"target_id"`
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
	TargetPort    int    `json:"target_port"`
	LocalPort     int    `json:"local_port"`
}

type TunnelStatus struct {
	ID        string `json:"id"`
	LocalPort int    `json:"local_port"`
	Target    string `json:"target"`
	Status    string `json:"status"`
	Gateway   string `json:"gateway"`
	Error     string `json:"error,omitempty"`
}

type ActiveTunnel struct {
	Req       TunnelRequest
	Cancel    context.CancelFunc
	Done      chan struct{}
	Ready     chan struct{}
	Status    string
	LastError string
}

type manager struct {
	mu      sync.Mutex
	tunnels map[string]*ActiveTunnel
}

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

func Run(port int) error {
	m := &manager{tunnels: make(map[string]*ActiveTunnel)}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", enableCORS(handleHealth))
	mux.HandleFunc("/start", enableCORS(m.handleStart))
	mux.HandleFunc("/stop", enableCORS(m.handleStop))
	mux.HandleFunc("/status", enableCORS(m.handleStatus))

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Tunnel Service running on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func enableCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func (m *manager) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ConnectURL) == "" || strings.TrimSpace(req.ConnectToken) == "" || req.LocalPort <= 0 {
		http.Error(w, "Missing required fields (connect_url, connect_token, local_port)", http.StatusBadRequest)
		return
	}
	if len(req.Targets) == 0 {
		http.Error(w, "Missing required tunnel targets", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("tunnel-%d", req.LocalPort)
	}

	m.mu.Lock()
	if _, exists := m.tunnels[req.ID]; exists {
		m.mu.Unlock()
		http.Error(w, fmt.Sprintf("Tunnel with ID %s already exists", req.ID), http.StatusConflict)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &ActiveTunnel{
		Req:    req,
		Cancel: cancel,
		Done:   make(chan struct{}),
		Ready:  make(chan struct{}),
		Status: "starting",
	}
	m.tunnels[req.ID] = active
	m.mu.Unlock()

	go m.runTunnel(ctx, active)

	select {
	case <-active.Ready:
	case <-time.After(3 * time.Second):
	case <-active.Done:
	}

	m.mu.Lock()
	current, ok := m.tunnels[req.ID]
	status := active.Status
	lastError := active.LastError
	if ok {
		status = current.Status
		lastError = current.LastError
	}
	m.mu.Unlock()

	if status == "error" {
		http.Error(w, lastError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": status,
		"id":     req.ID,
	})
}

func (m *manager) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := m.stopTunnel(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Tunnel stopped"))
}

func (m *manager) handleStatus(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	statuses := make([]TunnelStatus, 0, len(m.tunnels))
	for id, t := range m.tunnels {
		statuses = append(statuses, TunnelStatus{
			ID:        id,
			LocalPort: t.Req.LocalPort,
			Target:    describeTarget(t.Req),
			Gateway:   t.Req.ConnectURL,
			Status:    t.Status,
			Error:     t.LastError,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statuses)
}

func (m *manager) runTunnel(ctx context.Context, active *ActiveTunnel) {
	defer close(active.Done)
	target, err := primaryTarget(active.Req, active.Req.TargetPort)
	if err != nil {
		m.failTunnel(active.Req.ID, err)
		return
	}
	err = serveTunnelGateway(
		ctx,
		active.Req,
		target,
		active.Req.LocalPort,
		active.Ready,
		func() {
			m.setTunnelStatus(active.Req.ID, "active", "")
		},
	)
	if err != nil && !errors.Is(err, context.Canceled) {
		m.failTunnel(active.Req.ID, err)
		return
	}
	m.finishTunnel(active.Req.ID, "closed", "")
}

func (m *manager) stopTunnel(id string) error {
	m.mu.Lock()
	t, exists := m.tunnels[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("tunnel not found")
	}
	delete(m.tunnels, id)
	t.Status = "closed"
	m.mu.Unlock()

	if t.Cancel != nil {
		t.Cancel()
	}
	<-t.Done
	log.Printf("Tunnel %s stopped", id)
	return nil
}

func (m *manager) failTunnel(id string, err error) {
	m.finishTunnel(id, "error", err.Error())
}

func (m *manager) setTunnelStatus(id, status, lastError string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tunnels[id]
	if !ok {
		return
	}
	t.Status = status
	t.LastError = lastError
}

func (m *manager) finishTunnel(id, status, lastError string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tunnels[id]
	if !ok {
		return
	}
	t.Status = status
	t.LastError = lastError
	if status != "active" {
		delete(m.tunnels, id)
	}
}

func serveTunnelGateway(
	ctx context.Context,
	req TunnelRequest,
	target TunnelTarget,
	localPort int,
	ready chan struct{},
	onReady func(),
) error {
	wsConfig, err := websocket.NewConfig(req.ConnectURL, apiHost())
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
		ProtocolVersion: max(1, req.ProtocolVersion),
		TunnelID:        req.ID,
		ConnectToken:    req.ConnectToken,
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
	if onReady != nil {
		onReady()
	}
	close(ready)

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
					log.Printf("Tunnel proxy error for %s: %v", req.ID, err)
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
	return ctx.Err()
}

func proxyTunnelConnection(
	ctx context.Context,
	session *yamux.Session,
	target TunnelTarget,
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

func primaryTarget(req TunnelRequest, requestedTargetPort int) (TunnelTarget, error) {
	if len(req.Targets) == 0 {
		return TunnelTarget{}, errors.New("tunnel has no targets")
	}
	if requestedTargetPort > 0 {
		for _, target := range req.Targets {
			if target.TargetPort == requestedTargetPort {
				return target, nil
			}
		}
	}
	return req.Targets[0], nil
}

func describeTarget(req TunnelRequest) string {
	target, err := primaryTarget(req, req.TargetPort)
	if err != nil {
		return "unknown"
	}
	host := strings.TrimSpace(target.ContainerName)
	if host == "" {
		host = strings.TrimSpace(target.ContainerID)
	}
	if host == "" {
		host = "container"
	}
	return fmt.Sprintf("%s:%d", host, target.TargetPort)
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func apiHost() string {
	if url := os.Getenv("HUBFLY_API_URL"); url != "" {
		return url
	}
	return "https://api.hubfly.space"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
