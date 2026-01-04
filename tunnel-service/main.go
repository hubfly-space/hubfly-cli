package main

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

// TunnelRequest represents the payload to start a tunnel
type TunnelRequest struct {
	ID          string `json:"id"`
	SSHHost     string `json:"ssh_host"`
	SSHPort     int    `json:"ssh_port"`
	SSHUser     string `json:"ssh_user"`
	PrivateKey  string `json:"private_key"`
	LocalPort   int    `json:"local_port"`
	RemoteHost  string `json:"remote_host"`
	RemotePort  int    `json:"remote_port"`
}

// TunnelStatus represents the status of a single tunnel
type TunnelStatus struct {
	ID         string `json:"id"`
	LocalPort  int    `json:"local_port"`
	Target     string `json:"target"`
	Status     string `json:"status"`
	SSHServer  string `json:"ssh_server"`
	Error      string `json:"error,omitempty"`
}

// ActiveTunnel holds the runtime state of a tunnel
type ActiveTunnel struct {
	Req       TunnelRequest
	Listener  net.Listener
	SSHClient *ssh.Client
	Quit      chan struct{}
}

// TunnelManager manages multiple tunnels
type TunnelManager struct {
	mu      sync.Mutex
	tunnels map[string]*ActiveTunnel
}

var manager = &TunnelManager{
	tunnels: make(map[string]*ActiveTunnel),
}

func main() {
	http.HandleFunc("/health", enableCORS(handleHealth))
	http.HandleFunc("/start", enableCORS(handleStart))
	http.HandleFunc("/stop", enableCORS(handleStop))
	http.HandleFunc("/status", enableCORS(handleStatus))

	port := 5600
	fmt.Printf("Tunnel Service running on port %d...\n", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
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

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Basic validation
	if req.SSHHost == "" || req.SSHPort == 0 || req.PrivateKey == "" || req.LocalPort == 0 || req.RemoteHost == "" || req.RemotePort == 0 {
		http.Error(w, "Missing required fields (ssh_host, ssh_port, private_key, local_port, remote_host, remote_port)", http.StatusBadRequest)
		return
	}
	
	if req.SSHUser == "" {
		req.SSHUser = "root" // Default to root if not provided
	}

	if req.ID == "" {
		req.ID = fmt.Sprintf("tunnel-%d", req.LocalPort)
	}

	manager.mu.Lock()
	if _, exists := manager.tunnels[req.ID]; exists {
		manager.mu.Unlock()
		http.Error(w, fmt.Sprintf("Tunnel with ID %s already exists", req.ID), http.StatusConflict)
		return
	}
	manager.mu.Unlock()

	// Start tunnel in a goroutine
	go func() {
		if err := startTunnel(req); err != nil {
			log.Printf("Failed to start tunnel %s: %v", req.ID, err)
		}
	}()

	// We return success immediately, but the tunnel might fail to start asynchronously. 
	// In a production app, we might want to wait for the initial connection.
	// For simplicity, we'll wait a brief moment to check for immediate errors or verify connectivity.
	time.Sleep(500 * time.Millisecond)

	manager.mu.Lock()
	_, running := manager.tunnels[req.ID]
	manager.mu.Unlock()

	if running {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "initiated",
			"id":     req.ID,
		})
	} else {
		http.Error(w, "Failed to establish tunnel connection immediately. Check logs.", http.StatusInternalServerError)
	}
}

func handleStop(w http.ResponseWriter, r *http.Request) {
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

	if err := stopTunnel(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Tunnel stopped"))
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	statuses := []TunnelStatus{}
	for id, t := range manager.tunnels {
		statuses = append(statuses, TunnelStatus{
			ID:        id,
			LocalPort: t.Req.LocalPort,
			Target:    fmt.Sprintf("%s:%d", t.Req.RemoteHost, t.Req.RemotePort),
			SSHServer: fmt.Sprintf("%s:%d", t.Req.SSHHost, t.Req.SSHPort),
			Status:    "active",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}

func startTunnel(req TunnelRequest) error {
	// Parse Private Key
	signer, err := ssh.ParsePrivateKey([]byte(req.PrivateKey))
	if err != nil {
		return fmt.Errorf("invalid private key: %v", err)
	}

	// SSH Client Config
	config := &ssh.ClientConfig{
		User: req.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Note: In production, verify host keys
		Timeout:         10 * time.Second,
	}

	// Connect to SSH Server
	sshAddr := fmt.Sprintf("%s:%d", req.SSHHost, req.SSHPort)
	client, err := ssh.Dial("tcp", sshAddr, config)
	if err != nil {
		return fmt.Errorf("failed to dial ssh: %v", err)
	}

	// Start Local Listener
	localListener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", req.LocalPort))
	if err != nil {
		client.Close()
		return fmt.Errorf("failed to start local listener: %v", err)
	}

	t := &ActiveTunnel{
		Req:       req,
		Listener:  localListener,
		SSHClient: client,
		Quit:      make(chan struct{}),
	}

	manager.mu.Lock()
	manager.tunnels[req.ID] = t
	manager.mu.Unlock()

	log.Printf("Tunnel %s started: localhost:%d -> %s via %s", req.ID, req.LocalPort, fmt.Sprintf("%s:%d", req.RemoteHost, req.RemotePort), sshAddr)

	// Accept connections
	go func() {
		defer func() {
			stopTunnel(req.ID)
		}()
		
		for {
			conn, err := localListener.Accept()
			if err != nil {
				select {
				case <-t.Quit:
					return // listener closed intentionally
				default:
					log.Printf("Accept error on tunnel %s: %v", req.ID, err)
					return
				}
			}

			// Handle connection
			go forwardConnection(client, conn, req.RemoteHost, req.RemotePort)
		}
	}()

	return nil
}

func forwardConnection(client *ssh.Client, localConn net.Conn, remoteHost string, remotePort int) {
	defer localConn.Close()

	// Dial remote via SSH
	remoteAddr := fmt.Sprintf("%s:%d", remoteHost, remotePort)
	remoteConn, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		log.Printf("Failed to dial remote %s: %v", remoteAddr, err)
		return
	}
	defer remoteConn.Close()

	// Pipe data
	done := make(chan struct{}, 2)

	go func() {
		io.Copy(localConn, remoteConn)
		done <- struct{}{}
	}()

	go func() {
		io.Copy(remoteConn, localConn)
		done <- struct{}{}
	}()

	<-done
}

func stopTunnel(id string) error {
	manager.mu.Lock()
	t, exists := manager.tunnels[id]
	if !exists {
		manager.mu.Unlock()
		return fmt.Errorf("tunnel not found")
	}
	delete(manager.tunnels, id)
	manager.mu.Unlock()

	close(t.Quit)
	t.Listener.Close()
	t.SSHClient.Close()
	log.Printf("Tunnel %s stopped", id)
	return nil
}
