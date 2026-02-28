package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	fmt.Println("Projects:")
	for i, p := range projects {
		fmt.Printf("%d. %s (%s) - %s\n", i+1, p.Name, p.Region.Name, p.Status)
	}
	choice, err := promptNumber("Select project number (0 to cancel): ", len(projects))
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
