package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const apiHost = "https://hubfly.space"

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s (status %d)", e.Message, e.Status)
}

type storeConfig struct {
	Token string `json:"token"`
}

type user struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Image string `json:"image"`
}

type region struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

type project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	Region    region `json:"region"`
}

type projectsResponse struct {
	Projects []project `json:"projects"`
}

type container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Tier    string `json:"tier"`
	Status  string `json:"status"`
	Created string `json:"createdAt"`
	Source  struct {
		Type string `json:"type"`
	} `json:"source"`
	Resources struct {
		CPU int `json:"cpu"`
		RAM int `json:"ram"`
	} `json:"resources"`
}

type projectDetails struct {
	Containers []container `json:"containers"`
}

type tunnel struct {
	TunnelID          string `json:"tunnelId"`
	ID                string `json:"id"`
	SSHHost           string `json:"sshHost"`
	SSHPort           int    `json:"sshPort"`
	SSHUser           string `json:"sshUser"`
	TargetPort        int    `json:"targetPort"`
	TargetContainer   string `json:"targetContainer"`
	TargetContainerID string `json:"targetContainerId"`
	TargetNetwork     struct {
		IPAddress string `json:"ipAddress"`
	} `json:"targetNetwork"`
	ExpiresAt string `json:"expiresAt"`
}

type createTunnelRequest struct {
	ProjectID       string `json:"projectId"`
	TargetContainer string `json:"targetContainer"`
	TargetPort      int    `json:"targetPort"`
	ContainerID     string `json:"containerId"`
	PublicKey       string `json:"publicKey"`
}

var stdin = bufio.NewReader(os.Stdin)

func Run(args []string) int {
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(args []string) error {
	if len(args) == 0 {
		_, err := ensureAuth(false)
		return err
	}

	switch args[0] {
	case "login":
		var provided string
		if len(args) >= 3 && args[1] == "--token" {
			provided = args[2]
		}
		return login(provided)
	case "logout":
		if err := deleteToken(); err != nil {
			return err
		}
		fmt.Println("Logged out successfully.")
		return nil
	case "whoami":
		_, err := ensureAuth(false)
		return err
	case "projects":
		return projectsFlow()
	case "tunnel":
		if len(args) != 4 {
			return errors.New("usage: hubfly tunnel <containerIdOrName> <localPort> <targetPort>")
		}
		localPort, err := strconv.Atoi(args[2])
		if err != nil || localPort <= 0 {
			return errors.New("invalid local port")
		}
		targetPort, err := strconv.Atoi(args[3])
		if err != nil || targetPort <= 0 {
			return errors.New("invalid target port")
		}
		return tunnelFlow(args[1], localPort, targetPort)
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage() {
	fmt.Println("Hubfly CLI")
	fmt.Println("Usage:")
	fmt.Println("  hubfly login [--token <token>]")
	fmt.Println("  hubfly logout")
	fmt.Println("  hubfly whoami")
	fmt.Println("  hubfly projects")
	fmt.Println("  hubfly tunnel <containerIdOrName> <localPort> <targetPort>")
	fmt.Println("  hubfly service [--port <port>]")
}

func login(providedToken string) error {
	token := strings.TrimSpace(providedToken)
	if token != "" {
		u, err := fetchWhoAmI(token)
		if err != nil {
			return err
		}
		if err := setToken(token); err != nil {
			return err
		}
		fmt.Printf("Successfully logged in as %s (%s)\n", u.Name, u.Email)
		return nil
	}

	for {
		fmt.Println("Please authenticate to continue. Go to https://hubfly.space/cli/auth to get the token")
		input, err := prompt("Enter your API token: ")
		if err != nil {
			return err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			fmt.Println("Token cannot be empty.")
			continue
		}

		u, authErr := fetchWhoAmI(input)
		if authErr != nil {
			fmt.Printf("Authentication failed: %v\n", authErr)
			continue
		}

		if err := setToken(input); err != nil {
			return err
		}
		fmt.Printf("Successfully logged in as %s (%s)\n", u.Name, u.Email)
		return nil
	}
}

func ensureAuth(silent bool) (string, error) {
	token, err := getToken()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		if !silent {
			fmt.Println("No valid session found.")
		}
		if err := login(""); err != nil {
			return "", err
		}
		return getToken()
	}

	u, err := fetchWhoAmI(token)
	if err == nil {
		if !silent {
			fmt.Printf("Logged in as %s (%s)\n", u.Name, u.Email)
		}
		return token, nil
	}

	var apiErr *apiError
	if errors.As(err, &apiErr) && (apiErr.Status == 401 || apiErr.Status == 403) {
		if !silent {
			fmt.Println("Session expired or invalid.")
		}
		_ = deleteToken()
		if loginErr := login(""); loginErr != nil {
			return "", loginErr
		}
		return getToken()
	}
	return "", err
}

func projectsFlow() error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	projects, err := fetchProjects(token)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Println("No projects found.")
		return nil
	}

	fmt.Println("Projects:")
	for i, p := range projects {
		fmt.Printf("%d. %s (%s) - %s\n", i+1, p.Name, p.Region.Name, p.Status)
	}
	choice, err := promptNumber("Select project number (0 to cancel): ", 0)
	if err != nil {
		return err
	}
	if choice == 0 {
		return nil
	}
	if choice < 1 || choice > len(projects) {
		return errors.New("invalid project selection")
	}

	return manageProject(token, projects[choice-1].ID)
}

func manageProject(token string, projectID string) error {
	for {
		details, err := fetchProject(token, projectID)
		if err != nil {
			return err
		}

		if len(details.Containers) == 0 {
			fmt.Println("No containers found in this project.")
		} else {
			fmt.Println("Containers:")
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "#\tName\tStatus\tType\tCPU\tRAM\tTier")
			for i, c := range details.Containers {
				_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%d\t%s\n", i+1, c.Name, c.Status, c.Source.Type, c.Resources.CPU, c.Resources.RAM, c.Tier)
			}
			_ = tw.Flush()
		}

		fmt.Println("Actions: 1) Manage Container (Tunnels) 2) Refresh 3) Back")
		action, err := promptNumber("Choose action: ", 3)
		if err != nil {
			return err
		}
		switch action {
		case 1:
			if len(details.Containers) == 0 {
				fmt.Println("No containers available.")
				continue
			}
			containerIdx, selErr := promptNumber("Select container number: ", len(details.Containers))
			if selErr != nil {
				return selErr
			}
			if containerIdx < 1 || containerIdx > len(details.Containers) {
				fmt.Println("Invalid container selection.")
				continue
			}
			if err := manageContainer(token, projectID, details.Containers[containerIdx-1]); err != nil {
				return err
			}
		case 2:
			continue
		default:
			return nil
		}
	}
}

func manageContainer(token, projectID string, c container) error {
	for {
		fmt.Printf("\nContainer: %s\n", c.Name)
		tunnels, err := fetchTunnels(token, projectID)
		if err != nil {
			fmt.Println("Could not fetch tunnels.")
		}

		myTunnels := make([]tunnel, 0)
		for _, t := range tunnels {
			if t.TargetContainerID == c.ID {
				myTunnels = append(myTunnels, t)
			}
		}

		if len(myTunnels) > 0 {
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "#\tID\tSSH\tTarget\tExpires")
			for i, t := range myTunnels {
				_, _ = fmt.Fprintf(tw, "%d\t%s\t%s:%d\t%s:%d\t%s\n", i+1, t.TunnelID, t.SSHHost, t.SSHPort, t.TargetNetwork.IPAddress, t.TargetPort, t.ExpiresAt)
			}
			_ = tw.Flush()
		} else {
			fmt.Println("No active tunnels found for this container.")
		}

		fmt.Println("Actions: 1) Create New Tunnel 2) Connect to Tunnel 3) Back")
		action, err := promptNumber("Choose action: ", 3)
		if err != nil {
			return err
		}

		switch action {
		case 1:
			port, pErr := promptNumberWithDefault("Enter internal container port", 80)
			if pErr != nil {
				return pErr
			}
			if port <= 0 {
				continue
			}
			if err := createAndStoreTunnel(token, projectID, c, port); err != nil {
				fmt.Printf("Failed to create tunnel: %v\n", err)
			}
		case 2:
			if len(myTunnels) == 0 {
				fmt.Println("No tunnels available.")
				continue
			}
			idx, selErr := promptNumber("Select tunnel number: ", len(myTunnels))
			if selErr != nil {
				return selErr
			}
			if idx < 1 || idx > len(myTunnels) {
				fmt.Println("Invalid tunnel selection.")
				continue
			}
			selected := myTunnels[idx-1]
			local, lErr := promptNumberWithDefault("Enter local port to forward to", selected.TargetPort)
			if lErr != nil {
				return lErr
			}
			if local <= 0 {
				continue
			}
			keyPath := filepath.Join(keysDir(), "tunnel-"+selected.TunnelID)
			if err := runTunnelConnection(selected, keyPath, local, selected.TargetPort); err != nil {
				fmt.Printf("Tunnel connection failed: %v\n", err)
			}
		default:
			return nil
		}
	}
}

func createAndStoreTunnel(token, projectID string, c container, targetPort int) error {
	fmt.Println("Generating SSH keys...")
	tempID := fmt.Sprintf("temp-%d", time.Now().UnixNano())
	publicKey, err := generateKeyPairAndSave(tempID)
	if err != nil {
		return err
	}

	fmt.Println("Creating tunnel on server...")
	t, err := createTunnel(token, createTunnelRequest{
		ProjectID:       projectID,
		TargetContainer: c.Name,
		ContainerID:     c.ID,
		TargetPort:      targetPort,
		PublicKey:       publicKey,
	})
	if err != nil {
		_ = removeKeyPair(tempID)
		return err
	}

	if _, err := renameKeyFiles(tempID, "tunnel-"+t.TunnelID); err != nil {
		return err
	}
	fmt.Println("Tunnel created successfully. Keys saved.")
	return nil
}

func tunnelFlow(containerIDOrName string, localPort, targetPort int) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Printf("Searching for container '%s'...\n", containerIDOrName)
	projects, err := fetchProjects(token)
	if err != nil {
		return err
	}

	var targetContainer *container
	var targetProjectID string
	for _, p := range projects {
		details, fetchErr := fetchProject(token, p.ID)
		if fetchErr != nil {
			continue
		}
		for _, c := range details.Containers {
			if c.ID == containerIDOrName || c.Name == containerIDOrName {
				copyContainer := c
				targetContainer = &copyContainer
				targetProjectID = p.ID
				break
			}
		}
		if targetContainer != nil {
			break
		}
	}
	if targetContainer == nil {
		return fmt.Errorf("container '%s' not found in any project", containerIDOrName)
	}
	fmt.Printf("Found container: %s (%s)\n", targetContainer.Name, targetContainer.ID)

	fmt.Println("Checking for existing tunnels...")
	tunnels, err := fetchTunnels(token, targetProjectID)
	if err != nil {
		return err
	}

	var tunnelToUse *tunnel
	var keyPathToUse string
	now := time.Now()
	for _, t := range tunnels {
		if t.TargetContainerID != targetContainer.ID {
			continue
		}
		if t.TargetPort != targetPort {
			continue
		}
		expiresAt, parseErr := time.Parse(time.RFC3339, t.ExpiresAt)
		if parseErr == nil && expiresAt.Before(now) {
			continue
		}
		keyPath := filepath.Join(keysDir(), "tunnel-"+t.TunnelID)
		if _, statErr := os.Stat(keyPath); statErr == nil {
			copyTunnel := t
			tunnelToUse = &copyTunnel
			keyPathToUse = keyPath
			break
		}
	}

	if tunnelToUse == nil {
		fmt.Println("No suitable existing tunnel found. Creating new tunnel...")
		tempID := fmt.Sprintf("temp-%d", time.Now().UnixNano())
		publicKey, genErr := generateKeyPairAndSave(tempID)
		if genErr != nil {
			return genErr
		}

		newTunnel, createErr := createTunnel(token, createTunnelRequest{
			ProjectID:       targetProjectID,
			TargetContainer: targetContainer.Name,
			ContainerID:     targetContainer.ID,
			TargetPort:      targetPort,
			PublicKey:       publicKey,
		})
		if createErr != nil {
			_ = removeKeyPair(tempID)
			return createErr
		}
		newKeyPath, renameErr := renameKeyFiles(tempID, "tunnel-"+newTunnel.TunnelID)
		if renameErr != nil {
			return renameErr
		}
		tunnelToUse = &newTunnel
		keyPathToUse = newKeyPath
	}

	return runTunnelConnection(*tunnelToUse, keyPathToUse, localPort, targetPort)
}

func fetchWhoAmI(token string) (user, error) {
	var u user
	err := doJSONRequest(http.MethodGet, apiHost+"/api/auth/whoami", token, nil, &u)
	return u, err
}

func fetchProjects(token string) ([]project, error) {
	var payload projectsResponse
	err := doJSONRequest(http.MethodGet, apiHost+"/api/projects", token, nil, &payload)
	return payload.Projects, err
}

func fetchProject(token, projectID string) (projectDetails, error) {
	var payload projectDetails
	err := doJSONRequest(http.MethodGet, apiHost+"/api/projects/"+projectID+"/containers", token, nil, &payload)
	return payload, err
}

func fetchTunnels(token, projectID string) ([]tunnel, error) {
	var payload []tunnel
	err := doJSONRequest(http.MethodGet, apiHost+"/api/projects/"+projectID+"/tunnels", token, nil, &payload)
	return payload, err
}

func createTunnel(token string, req createTunnelRequest) (tunnel, error) {
	var t tunnel
	err := doJSONRequest(http.MethodPost, apiHost+"/api/tunnels", token, req, &t)
	return t, err
}

func doJSONRequest(method, url, token string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewBuffer(payload)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(respBytes))
		if msg == "" {
			msg = "request failed"
		}
		return &apiError{Status: resp.StatusCode, Message: msg}
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func hubflyDir() string {
	return filepath.Join(userHomeDir(), ".hubfly")
}

func keysDir() string {
	return filepath.Join(hubflyDir(), "keys")
}

func configPath() string {
	return filepath.Join(hubflyDir(), "config.json")
}

func userHomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func getToken() (string, error) {
	content, err := os.ReadFile(configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	var cfg storeConfig
	if err := json.Unmarshal(content, &cfg); err != nil {
		return "", err
	}
	return cfg.Token, nil
}

func setToken(token string) error {
	if err := os.MkdirAll(hubflyDir(), 0o700); err != nil {
		return err
	}
	cfg := storeConfig{Token: token}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), payload, 0o600)
}

func deleteToken() error {
	err := os.Remove(configPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func generateKeyPairAndSave(identifier string) (string, error) {
	if err := os.MkdirAll(keysDir(), 0o700); err != nil {
		return "", err
	}
	privateKeyPath := filepath.Join(keysDir(), identifier)
	publicKeyPath := privateKeyPath + ".pub"

	cmd := exec.Command("ssh-keygen", "-q", "-t", "rsa", "-b", "4096", "-N", "", "-f", privateKeyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	pub, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(pub)), nil
}

func removeKeyPair(identifier string) error {
	base := filepath.Join(keysDir(), identifier)
	_ = os.Remove(base)
	_ = os.Remove(base + ".pub")
	return nil
}

func renameKeyFiles(oldIdentifier, newIdentifier string) (string, error) {
	oldPriv := filepath.Join(keysDir(), oldIdentifier)
	oldPub := oldPriv + ".pub"
	newPriv := filepath.Join(keysDir(), newIdentifier)
	newPub := newPriv + ".pub"
	if err := os.Rename(oldPriv, newPriv); err != nil {
		return "", err
	}
	if err := os.Rename(oldPub, newPub); err != nil {
		return "", err
	}
	return newPriv, nil
}

func runTunnelConnection(t tunnel, privateKeyPath string, localPort, targetPort int) error {
	fmt.Println("Establishing tunnel...")
	fmt.Printf("Local: localhost:%d -> Remote: %s:%d\n", localPort, t.TargetNetwork.IPAddress, targetPort)
	fmt.Println("Run manually if needed:")
	fmt.Printf("ssh -i %s -p %d %s@%s -L %d:%s:%d -N\n", privateKeyPath, t.SSHPort, strings.TrimSpace(t.SSHUser), strings.TrimSpace(t.SSHHost), localPort, t.TargetNetwork.IPAddress, targetPort)

	maxRetries := 3
	retryDelay := 2 * time.Second

	for attempt := 1; attempt <= maxRetries+1; attempt++ {
		if attempt > 1 {
			fmt.Printf("Connection failed. Retrying in %.0fs... (Attempt %d/%d)\n", retryDelay.Seconds(), attempt, maxRetries+1)
			time.Sleep(retryDelay)
		}

		cmd := exec.Command("ssh",
			"-i", privateKeyPath,
			"-p", strconv.Itoa(t.SSHPort),
			fmt.Sprintf("%s@%s", strings.TrimSpace(t.SSHUser), strings.TrimSpace(t.SSHHost)),
			"-L", fmt.Sprintf("%d:%s:%d", localPort, t.TargetNetwork.IPAddress, targetPort),
			"-N",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin

		err := cmd.Run()
		exitCode := 1
		if err == nil {
			exitCode = 0
		} else if ee := (&exec.ExitError{}); errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}

		if exitCode == 0 || exitCode == 130 {
			fmt.Printf("Tunnel connection closed (code %d)\n", exitCode)
			return nil
		}
		if attempt == maxRetries+1 {
			return fmt.Errorf("tunnel connection closed with code %d", exitCode)
		}
	}
	return nil
}

func prompt(label string) (string, error) {
	fmt.Print(label)
	line, err := stdin.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptNumber(label string, max int) (int, error) {
	for {
		text, err := prompt(label)
		if err != nil {
			return 0, err
		}
		value, convErr := strconv.Atoi(text)
		if convErr != nil {
			fmt.Println("Please enter a valid number.")
			continue
		}
		if value < 0 || value > max {
			fmt.Println("Number out of range.")
			continue
		}
		return value, nil
	}
}

func promptNumberWithDefault(label string, defaultValue int) (int, error) {
	for {
		text, err := prompt(fmt.Sprintf("%s (default %d): ", label, defaultValue))
		if err != nil {
			return 0, err
		}
		if text == "" {
			return defaultValue, nil
		}
		value, convErr := strconv.Atoi(text)
		if convErr != nil {
			fmt.Println("Please enter a valid number.")
			continue
		}
		return value, nil
	}
}
