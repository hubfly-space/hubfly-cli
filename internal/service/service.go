package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TunnelRequest struct {
	ID         string `json:"id"`
	SSHHost    string `json:"ssh_host"`
	SSHPort    int    `json:"ssh_port"`
	SSHUser    string `json:"ssh_user"`
	PrivateKey string `json:"private_key"`
	LocalPort  int    `json:"local_port"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
}

type TunnelStatus struct {
	ID        string `json:"id"`
	LocalPort int    `json:"local_port"`
	Target    string `json:"target"`
	Status    string `json:"status"`
	SSHServer string `json:"ssh_server"`
	Error     string `json:"error,omitempty"`
}

type ActiveTunnel struct {
	Req            TunnelRequest
	Cmd            *exec.Cmd
	Quit           chan struct{}
	PrivateKeyPath string
	LastError      string
}

type manager struct {
	mu      sync.Mutex
	tunnels map[string]*ActiveTunnel
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

	if req.SSHHost == "" || req.SSHPort == 0 || req.PrivateKey == "" || req.LocalPort == 0 || req.RemoteHost == "" || req.RemotePort == 0 {
		http.Error(w, "Missing required fields (ssh_host, ssh_port, private_key, local_port, remote_host, remote_port)", http.StatusBadRequest)
		return
	}
	if req.SSHUser == "" {
		req.SSHUser = "root"
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
	m.mu.Unlock()

	go func() {
		if err := m.startTunnel(req); err != nil {
			log.Printf("Failed to start tunnel %s: %v", req.ID, err)
		}
	}()

	time.Sleep(500 * time.Millisecond)
	m.mu.Lock()
	_, running := m.tunnels[req.ID]
	m.mu.Unlock()

	if !running {
		http.Error(w, "Failed to establish tunnel connection immediately. Check logs.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "initiated",
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
			Target:    fmt.Sprintf("%s:%d", t.Req.RemoteHost, t.Req.RemotePort),
			SSHServer: fmt.Sprintf("%s:%d", t.Req.SSHHost, t.Req.SSHPort),
			Status:    "active",
			Error:     t.LastError,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statuses)
}

func (m *manager) startTunnel(req TunnelRequest) error {
	privateKeyPath, err := writeTunnelPrivateKey(req.ID, req.PrivateKey)
	if err != nil {
		return err
	}
	knownHostsPath, err := ensureManagedKnownHosts()
	if err != nil {
		_ = os.Remove(privateKeyPath)
		return err
	}

	cmd := tunnelCommand(req, privateKeyPath, knownHostsPath)
	var stderr bytes.Buffer
	cmd.Stdout = os.Stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		_ = os.Remove(privateKeyPath)
		return fmt.Errorf("failed to start ssh tunnel process: %w", err)
	}

	t := &ActiveTunnel{
		Req:            req,
		Cmd:            cmd,
		Quit:           make(chan struct{}),
		PrivateKeyPath: privateKeyPath,
	}

	m.mu.Lock()
	m.tunnels[req.ID] = t
	m.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		select {
		case <-t.Quit:
			return
		default:
		}
		stderrText := strings.TrimSpace(stderr.String())
		if waitErr != nil {
			if stderrText == "" {
				stderrText = waitErr.Error()
			}
			log.Printf("Tunnel %s exited unexpectedly: %s", req.ID, stderrText)
		} else if stderrText != "" {
			log.Printf("Tunnel %s exited: %s", req.ID, stderrText)
		}
		m.mu.Lock()
		if active, ok := m.tunnels[req.ID]; ok {
			active.LastError = stderrText
			delete(m.tunnels, req.ID)
		}
		m.mu.Unlock()
		_ = os.Remove(privateKeyPath)
	}()

	sshAddr := fmt.Sprintf("%s:%d", req.SSHHost, req.SSHPort)
	for attempt := 0; attempt < 10; attempt++ {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			stderrText := strings.TrimSpace(stderr.String())
			_ = m.stopTunnel(req.ID)
			if stderrText == "" {
				return fmt.Errorf("ssh tunnel process exited before becoming ready")
			}
			return fmt.Errorf("ssh tunnel failed before becoming ready: %s", stderrText)
		}
		if err := waitForLocalPort(req.LocalPort, 300*time.Millisecond); err == nil {
			log.Printf("Tunnel %s started: localhost:%d -> %s:%d via %s", req.ID, req.LocalPort, req.RemoteHost, req.RemotePort, sshAddr)
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}

	_ = m.stopTunnel(req.ID)
	stderrText := strings.TrimSpace(stderr.String())
	if stderrText == "" {
		stderrText = "ssh tunnel did not become ready in time"
	}
	return fmt.Errorf("ssh tunnel startup timeout: %s", stderrText)
}

func (m *manager) stopTunnel(id string) error {
	m.mu.Lock()
	t, exists := m.tunnels[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("tunnel not found")
	}
	delete(m.tunnels, id)
	m.mu.Unlock()

	select {
	case <-t.Quit:
	default:
		close(t.Quit)
	}
	if t.Cmd != nil && t.Cmd.Process != nil {
		_ = t.Cmd.Process.Kill()
	}
	if strings.TrimSpace(t.PrivateKeyPath) != "" {
		_ = os.Remove(t.PrivateKeyPath)
	}
	log.Printf("Tunnel %s stopped", id)
	return nil
}

func tunnelCommand(req TunnelRequest, privateKeyPath, knownHostsPath string) *exec.Cmd {
	if req.SSHUser == "" {
		req.SSHUser = "root"
	}
	hostAlias := "hubfly-" + strings.TrimSpace(req.ID)
	return exec.Command(
		"ssh",
		"-i", privateKeyPath,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile="+knownHostsPath,
		"-o", "HostKeyAlias="+hostAlias,
		"-p", strconv.Itoa(req.SSHPort),
		fmt.Sprintf("%s@%s", strings.TrimSpace(req.SSHUser), strings.TrimSpace(req.SSHHost)),
		"-L", fmt.Sprintf("%d:%s:%d", req.LocalPort, strings.TrimSpace(req.RemoteHost), req.RemotePort),
		"-N",
	)
}

func writeTunnelPrivateKey(id, privateKey string) (string, error) {
	if strings.TrimSpace(privateKey) == "" {
		return "", errors.New("missing private key")
	}
	dir := filepath.Join(hubflyDir(), "service-keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, sanitizeID(id))
	if err := os.WriteFile(path, []byte(privateKey), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func ensureManagedKnownHosts() (string, error) {
	dir := hubflyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "known_hosts")
	if file, err := os.OpenFile(path, os.O_CREATE, 0o600); err != nil {
		return "", err
	} else {
		_ = file.Close()
	}
	return path, nil
}

func waitForLocalPort(port int, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), timeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func hubflyDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".hubfly"
	}
	return filepath.Join(home, ".hubfly")
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
