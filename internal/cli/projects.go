package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

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

	printProjectsTable(projects)
	project, cancelled, err := selectProject(projects)
	if err != nil {
		return err
	}
	if cancelled {
		return nil
	}

	return manageProject(token, project)
}

func manageProject(token string, p project) error {
	for {
		details, err := fetchProject(token, p.ID)
		if err != nil {
			return err
		}

		fmt.Printf("\nProject: %s (%s)\n", p.Name, p.ID)
		fmt.Printf("Region: %s | Status: %s | Role: %s | Spent: %s\n", p.Region.Name, p.Status, p.Role, valueOrDash(p.Spent))

		if len(details.Containers) == 0 {
			fmt.Println("No containers found in this project.")
		} else {
			printContainersTable(details.Containers)
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
			c, cancelled, selErr := selectContainer(details.Containers)
			if selErr != nil {
				return selErr
			}
			if cancelled {
				continue
			}
			if err := manageContainer(token, p.ID, c); err != nil {
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
		fmt.Printf("\nContainer: %s (%s)\n", c.Name, c.ID)
		fmt.Printf("Status: %s | Type: %s | Tier: %s | CPU: %.2f | RAM: %.0fMB | Storage: %.0fGB | Ports: %d\n",
			c.Status, c.Source.Type, c.Tier, c.Resources.CPU, c.Resources.RAM, c.Resources.Storage, len(c.Networking.Ports))
		if c.PrimaryNetworkAlias != "" {
			fmt.Printf("Network Alias: %s\n", c.PrimaryNetworkAlias)
		}

		tunnels, err := fetchTunnels(token, projectID)
		if err != nil {
			fmt.Printf("Could not fetch tunnels: %v\n", err)
		}

		myTunnels := make([]tunnel, 0)
		for _, t := range tunnels {
			if t.TargetContainerID == c.ID {
				myTunnels = append(myTunnels, t)
			}
		}

		if len(myTunnels) > 0 {
			printTunnelsTable(myTunnels)
		} else {
			fmt.Println("No active tunnels found for this container.")
		}

		fmt.Println("Actions: 1) Create New Tunnel 2) Connect to Tunnel 3) Refresh 4) Back")
		action, err := promptNumber("Choose action: ", 4)
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
			selected, cancelled, selErr := selectTunnel(myTunnels)
			if selErr != nil {
				return selErr
			}
			if cancelled {
				continue
			}
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
		case 3:
			continue
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

func printProjectsTable(projects []project) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tName\tRegion\tStatus\tRole\tSpent\tID")
	for i, p := range projects {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", i+1, p.Name, p.Region.Name, p.Status, p.Role, valueOrDash(p.Spent), p.ID)
	}
	_ = tw.Flush()
}

func printContainersTable(containers []container) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tName\tStatus\tType\tCPU\tRAM(MB)\tTier\tPorts\tID")
	for i, c := range containers {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%.2f\t%.0f\t%s\t%d\t%s\n",
			i+1, c.Name, c.Status, c.Source.Type, c.Resources.CPU, c.Resources.RAM, c.Tier, len(c.Networking.Ports), c.ID)
	}
	_ = tw.Flush()
}

func printTunnelsTable(tunnels []tunnel) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tTunnel ID\tSSH\tTarget\tExpires\tState")
	for i, t := range tunnels {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s:%d\t%s:%d\t%s\t%s\n",
			i+1, t.TunnelID, t.SSHHost, t.SSHPort, t.TargetNetwork.IPAddress, t.TargetPort, t.ExpiresAt, tunnelState(t.ExpiresAt))
	}
	_ = tw.Flush()
}

func tunnelState(expiresAt string) string {
	when, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return "unknown"
	}
	if when.Before(time.Now()) {
		return "expired"
	}
	return "active"
}

func selectProject(projects []project) (project, bool, error) {
	for {
		raw, err := prompt("Select project (number/name/id, 0 to cancel): ")
		if err != nil {
			return project{}, false, err
		}
		if raw == "0" {
			return project{}, true, nil
		}
		if idx, convErr := strconv.Atoi(raw); convErr == nil {
			if idx >= 1 && idx <= len(projects) {
				return projects[idx-1], false, nil
			}
			fmt.Println("Invalid project number.")
			continue
		}
		needle := strings.TrimSpace(strings.ToLower(raw))
		for _, p := range projects {
			if strings.EqualFold(p.ID, raw) || strings.EqualFold(p.Name, raw) || strings.Contains(strings.ToLower(p.Name), needle) {
				return p, false, nil
			}
		}
		fmt.Println("Project not found. Enter a valid number, name, or id.")
	}
}

func selectContainer(containers []container) (container, bool, error) {
	for {
		raw, err := prompt("Select container (number/name/id, 0 to cancel): ")
		if err != nil {
			return container{}, false, err
		}
		if raw == "0" {
			return container{}, true, nil
		}
		if idx, convErr := strconv.Atoi(raw); convErr == nil {
			if idx >= 1 && idx <= len(containers) {
				return containers[idx-1], false, nil
			}
			fmt.Println("Invalid container number.")
			continue
		}
		needle := strings.TrimSpace(strings.ToLower(raw))
		for _, c := range containers {
			if strings.EqualFold(c.ID, raw) || strings.EqualFold(c.Name, raw) || strings.Contains(strings.ToLower(c.Name), needle) {
				return c, false, nil
			}
		}
		fmt.Println("Container not found. Enter a valid number, name, or id.")
	}
}

func selectTunnel(tunnels []tunnel) (tunnel, bool, error) {
	for {
		raw, err := prompt("Select tunnel (number/tunnel id, 0 to cancel): ")
		if err != nil {
			return tunnel{}, false, err
		}
		if raw == "0" {
			return tunnel{}, true, nil
		}
		if idx, convErr := strconv.Atoi(raw); convErr == nil {
			if idx >= 1 && idx <= len(tunnels) {
				return tunnels[idx-1], false, nil
			}
			fmt.Println("Invalid tunnel number.")
			continue
		}
		for _, t := range tunnels {
			if strings.EqualFold(t.TunnelID, raw) {
				return t, false, nil
			}
		}
		fmt.Println("Tunnel not found. Enter a valid number or tunnel id.")
	}
}

func valueOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
