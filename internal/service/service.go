package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
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
	Req       TunnelRequest
	Listener  net.Listener
	SSHClient *ssh.Client
	Quit      chan struct{}
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
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statuses)
}

func (m *manager) startTunnel(req TunnelRequest) error {
	signer, err := ssh.ParsePrivateKey([]byte(req.PrivateKey))
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            req.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	sshAddr := fmt.Sprintf("%s:%d", req.SSHHost, req.SSHPort)
	client, err := ssh.Dial("tcp", sshAddr, config)
	if err != nil {
		return fmt.Errorf("failed to dial ssh: %w", err)
	}

	localListener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", req.LocalPort))
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("failed to start local listener: %w", err)
	}

	t := &ActiveTunnel{Req: req, Listener: localListener, SSHClient: client, Quit: make(chan struct{})}

	m.mu.Lock()
	m.tunnels[req.ID] = t
	m.mu.Unlock()

	log.Printf("Tunnel %s started: localhost:%d -> %s:%d via %s", req.ID, req.LocalPort, req.RemoteHost, req.RemotePort, sshAddr)

	go func() {
		defer func() {
			_ = m.stopTunnel(req.ID)
		}()

		for {
			conn, acceptErr := localListener.Accept()
			if acceptErr != nil {
				select {
				case <-t.Quit:
					return
				default:
					log.Printf("Accept error on tunnel %s: %v", req.ID, acceptErr)
					return
				}
			}
			go forwardConnection(client, conn, req.RemoteHost, req.RemotePort)
		}
	}()

	return nil
}

func forwardConnection(client *ssh.Client, localConn net.Conn, remoteHost string, remotePort int) {
	defer func() { _ = localConn.Close() }()

	remoteAddr := fmt.Sprintf("%s:%d", remoteHost, remotePort)
	remoteConn, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		log.Printf("Failed to dial remote %s: %v", remoteAddr, err)
		return
	}
	defer func() { _ = remoteConn.Close() }()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(localConn, remoteConn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(remoteConn, localConn)
		done <- struct{}{}
	}()
	<-done
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

	close(t.Quit)
	_ = t.Listener.Close()
	_ = t.SSHClient.Close()
	log.Printf("Tunnel %s stopped", id)
	return nil
}
