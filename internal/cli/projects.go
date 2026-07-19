package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

func projectsFlow(orgFilter string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	orgID := ""
	if orgFilter != "" {
		orgs, err := fetchOrganizations(token)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			if o.ID == orgFilter || o.Slug == orgFilter {
				orgID = o.ID
				break
			}
		}
		if orgID == "" {
			return fmt.Errorf("organization '%s' not found", orgFilter)
		}
	}

	return runProjectsTUI(token, orgID)
}

func manageProject(token string, p project) error {
	for {
		details, err := fetchProject(token, p.ID)
		if err != nil {
			return err
		}

		renderScreen("Project", fmt.Sprintf("%s (%s)", p.Name, p.ID))
		fmt.Printf("Region: %s | Status: %s | Role: %s | Spent: %s\n\n", p.Region.Name, p.Status, p.Role, valueOrDash(p.Spent))

		if len(details.Containers) == 0 {
			fmt.Println("No containers found in this project.")
		} else {
			printContainersTable(details.Containers)
		}

		action, cancelled, err := pickAction("Project Actions", "Use arrows and Enter", []listOption{
			{Title: "Manage Container (Tunnels)", Desc: "Select a container and manage its tunnels"},
			{Title: "Refresh", Desc: "Reload project details"},
			{Title: "Back", Desc: "Return to projects list"},
		})
		if err != nil {
			return err
		}
		if cancelled {
			return nil
		}

		switch action {
		case 0:
			if len(details.Containers) == 0 {
				waitForEnter("No containers available. Press Enter to continue...")
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
		case 1:
			continue
		default:
			return nil
		}
	}
}

func manageContainer(token, projectID string, c container) error {
	for {
		renderScreen("Container", fmt.Sprintf("%s (%s)", c.Name, c.ID))
		fmt.Printf("Status: %s | Type: %s | Tier: %s\n", c.Status, c.Source.Type, c.Tier)
		fmt.Printf("CPU: %.2f | RAM: %.0fMB | Storage: %.0fGB | Ports: %d\n", c.Resources.CPU, c.Resources.RAM, c.Resources.Storage, len(c.Networking.Ports))
		if c.PrimaryNetworkAlias != "" {
			fmt.Printf("Network Alias: %s\n", c.PrimaryNetworkAlias)
		}
		fmt.Println()

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

		action, cancelled, err := pickAction("Container Actions", "Use arrows and Enter", []listOption{
			{Title: "Create New Tunnel", Desc: "Create a direct tunnel session for this container"},
			{Title: "Connect One Tunnel", Desc: "Open a direct tunnel session"},
			{Title: "Connect Multiple Tunnels", Desc: "Run several direct tunnels at the same time"},
			{Title: "Refresh", Desc: "Reload container and tunnel state"},
			{Title: "Back", Desc: "Return to project view"},
		})
		if err != nil {
			return err
		}
		if cancelled {
			return nil
		}

		switch action {
		case 0:
			port, pErr := promptNumberWithDefault("Enter internal container port", 80)
			if pErr != nil {
				return pErr
			}
			if port <= 0 {
				continue
			}
			if err := createAndStoreTunnel(token, projectID, c, port); err != nil {
				waitForEnter(fmt.Sprintf("Failed to create tunnel: %v\nPress Enter to continue...", err))
				continue
			}
			waitForEnter("Tunnel created successfully. Press Enter to continue...")
		case 1:
			if len(myTunnels) == 0 {
				waitForEnter("No tunnels available. Press Enter to continue...")
				continue
			}
			selected, cancelled, selErr := selectTunnel(myTunnels)
			if selErr != nil {
				return selErr
			}
			if cancelled {
				continue
			}
			local, lErr := promptNumberWithDefault("Enter local port to forward to", selectedPrimaryPort(selected))
			if lErr != nil {
				return lErr
			}
			if local <= 0 {
				continue
			}
			renderScreen("Tunnel Session", fmt.Sprintf("Tunnel %s", selected.TunnelID))
			if err := runTunnelConnection(selected, "", local, selectedPrimaryPort(selected)); err != nil {
				waitForEnter(fmt.Sprintf("Tunnel connection failed: %v\nPress Enter to continue...", err))
			}
		case 2:
			if len(myTunnels) == 0 {
				waitForEnter("No tunnels available. Press Enter to continue...")
				continue
			}
			if err := connectMultipleTunnels(myTunnels); err != nil {
				waitForEnter(fmt.Sprintf("Multi tunnel connect failed: %v\nPress Enter to continue...", err))
			}
		case 3:
			continue
		default:
			return nil
		}
	}
}

func connectMultipleTunnels(tunnels []tunnel) error {
	selected, cancelled, err := selectMultipleTunnels(tunnels)
	if err != nil {
		return err
	}
	if cancelled || len(selected) == 0 {
		return nil
	}

	useDefaults, err := promptYesNo("Use each tunnel target port as local port", true)
	if err != nil {
		return err
	}

	type plannedTunnel struct {
		tunnel    tunnel
		localPort int
	}

	plans := make([]plannedTunnel, 0, len(selected))
	for _, t := range selected {
		localPort := selectedPrimaryPort(t)
		if !useDefaults {
			localPort, err = promptNumberWithDefault(fmt.Sprintf("Local port for %s", t.TunnelID), selectedPrimaryPort(t))
			if err != nil {
				return err
			}
			if localPort <= 0 {
				return fmt.Errorf("invalid local port for %s", t.TunnelID)
			}
		}
		plans = append(plans, plannedTunnel{
			tunnel:    t,
			localPort: localPort,
		})
	}

	renderScreen("Multi Tunnel Connect", "Starting selected tunnels")
	cmds := make([]*exec.Cmd, 0, len(plans))
	for _, p := range plans {
		fmt.Printf("Starting %s on localhost:%d -> %s:%d\n", p.tunnel.TunnelID, p.localPort, resolveTunnelForwardHost(p.tunnel), selectedPrimaryPort(p.tunnel))
		cmd, startErr := startTunnelConnectionBackground(p.tunnel, "", p.localPort, selectedPrimaryPort(p.tunnel))
		if startErr != nil {
			for _, running := range cmds {
				_ = stopSSHProcess(running)
			}
			return fmt.Errorf("failed to start %s: %w", p.tunnel.TunnelID, startErr)
		}
		cmds = append(cmds, cmd)
	}

	fmt.Println()
	fmt.Printf("%d tunnel processes are running.\n", len(cmds))
	fmt.Println("Press Enter to stop all tunnels, or use Ctrl+C.")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	stopCh := make(chan struct{})
	go func() {
		_, _ = prompt("")
		close(stopCh)
	}()

	exitCh := make(chan string, len(cmds))
	for i, cmd := range cmds {
		idx := i
		go func() {
			err := cmd.Wait()
			if err != nil {
				exitCh <- fmt.Sprintf("Tunnel #%d exited with error: %v", idx+1, err)
				return
			}
			exitCh <- fmt.Sprintf("Tunnel #%d exited", idx+1)
		}()
	}

	running := len(cmds)
	for running > 0 {
		select {
		case <-stopCh:
			for _, cmd := range cmds {
				_ = stopSSHProcess(cmd)
			}
			waitForEnter("All tunnels stopped. Press Enter to continue...")
			return nil
		case sig := <-sigCh:
			for _, cmd := range cmds {
				_ = stopSSHProcess(cmd)
			}
			waitForEnter(fmt.Sprintf("Received %s. All tunnels stopped. Press Enter to continue...", sig.String()))
			return nil
		case msg := <-exitCh:
			running--
			fmt.Println(msg)
			if running == 0 {
				waitForEnter("All tunnel processes exited. Press Enter to continue...")
				return nil
			}
		}
	}
	return nil
}

func stopSSHProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(os.Interrupt)
	time.Sleep(250 * time.Millisecond)
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	return nil
}

func createAndStoreTunnel(token, projectID string, c container, targetPort int) error {
	fmt.Println("Creating tunnel on server...")
	t, err := createTunnel(token, projectID, createTunnelRequest{
		ContainerID: c.ID,
		TargetPort:  targetPort,
		LocalPort:   targetPort,
	})
	if err != nil {
		return err
	}
	if err := saveTunnelTicket(t); err != nil {
		return err
	}
	fmt.Println("Tunnel created successfully. Local session ticket saved.")
	return nil
}

func tunnelFlow(containerIDOrName string, localPort, targetPort int) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Printf("Searching for container '%s'...\n", containerIDOrName)
	targetContainer, targetProjectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}
	fmt.Printf("Found container: %s (%s)\n", targetContainer.Name, targetContainer.ID)

	fmt.Println("Creating tunnel session...")
	tunnelToUse, err := createTunnel(token, targetProjectID, createTunnelRequest{
		ContainerID: targetContainer.ID,
		TargetPort:  targetPort,
		LocalPort:   localPort,
	})
	if err != nil {
		return err
	}
	if err := saveTunnelTicket(tunnelToUse); err != nil {
		return err
	}
	return runTunnelConnection(tunnelToUse, "", localPort, targetPort)
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
	_, _ = fmt.Fprintln(tw, "#\tTunnel ID\tMode\tTarget\tExpires\tState")
	for i, t := range tunnels {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s:%d\t%s\t%s\n",
			i+1, t.TunnelID, valueOrDash(t.Mode), resolveTunnelForwardHost(t), selectedPrimaryPort(t), t.ExpiresAt, tunnelState(t.ExpiresAt))
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

func pickAction(title, subtitle string, options []listOption) (int, bool, error) {
	return tuiPickOne(title, subtitle, options)
}

func selectProject(projects []project) (project, bool, error) {
	options := make([]listOption, 0, len(projects))
	for _, p := range projects {
		options = append(options, listOption{
			Title: fmt.Sprintf("%s (%s)", p.Name, p.ID),
			Desc:  fmt.Sprintf("Region: %s | Status: %s | Role: %s | Spent: %s", p.Region.Name, p.Status, p.Role, valueOrDash(p.Spent)),
		})
	}
	idx, cancelled, err := tuiPickOne("Projects", "Type to filter, Enter to select, q to cancel", options)
	if err != nil {
		return project{}, false, err
	}
	if cancelled {
		return project{}, true, nil
	}
	return projects[idx], false, nil
}

func selectContainer(containers []container) (container, bool, error) {
	options := make([]listOption, 0, len(containers))
	for _, c := range containers {
		options = append(options, listOption{
			Title: fmt.Sprintf("%s (%s)", c.Name, c.ID),
			Desc:  fmt.Sprintf("Status: %s | CPU: %.2f | RAM: %.0fMB | Ports: %d", c.Status, c.Resources.CPU, c.Resources.RAM, len(c.Networking.Ports)),
		})
	}
	idx, cancelled, err := tuiPickOne("Containers", "Type to filter, Enter to select, q to cancel", options)
	if err != nil {
		return container{}, false, err
	}
	if cancelled {
		return container{}, true, nil
	}
	return containers[idx], false, nil
}

func selectTunnel(tunnels []tunnel) (tunnel, bool, error) {
	options := make([]listOption, 0, len(tunnels))
	for _, t := range tunnels {
		options = append(options, listOption{
			Title: fmt.Sprintf("%s", t.TunnelID),
			Desc:  fmt.Sprintf("Mode: %s | Target: %s:%d | State: %s", valueOrDash(t.Mode), resolveTunnelForwardHost(t), selectedPrimaryPort(t), tunnelState(t.ExpiresAt)),
		})
	}
	idx, cancelled, err := tuiPickOne("Tunnels", "Type to filter, Enter to select, q to cancel", options)
	if err != nil {
		return tunnel{}, false, err
	}
	if cancelled {
		return tunnel{}, true, nil
	}
	return tunnels[idx], false, nil
}

func selectMultipleTunnels(tunnels []tunnel) ([]tunnel, bool, error) {
	options := make([]listOption, 0, len(tunnels))
	for _, t := range tunnels {
		options = append(options, listOption{
			Title: fmt.Sprintf("%s", t.TunnelID),
			Desc:  fmt.Sprintf("gateway -> %s:%d", resolveTunnelForwardHost(t), selectedPrimaryPort(t)),
		})
	}
	indices, cancelled, err := tuiPickMany("Multi Tunnel Selection", "Space to toggle, Enter to confirm, q to cancel", options)
	if err != nil {
		return nil, false, err
	}
	if cancelled {
		return nil, true, nil
	}

	selected := make([]tunnel, 0, len(indices))
	for _, idx := range indices {
		if idx >= 0 && idx < len(tunnels) {
			selected = append(selected, tunnels[idx])
		}
	}
	if len(selected) == 0 {
		return nil, true, nil
	}
	return selected, false, nil
}

func valueOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func selectedPrimaryPort(t tunnel) int {
	target, err := primaryTunnelTarget(t, 0)
	if err != nil {
		return 0
	}
	return target.TargetPort
}

func findContainer(token string, containerIDOrName string) (*container, string, error) {
	projects, err := fetchProjects(token)
	if err != nil {
		return nil, "", err
	}

	for _, p := range projects {
		details, fetchErr := fetchProject(token, p.ID)
		if fetchErr != nil {
			continue
		}
		for _, c := range details.Containers {
			if c.ID == containerIDOrName || c.Name == containerIDOrName {
				return &c, p.ID, nil
			}
		}
	}
	return nil, "", fmt.Errorf("container '%s' not found in any project", containerIDOrName)
}

func logsFlow(containerIDOrName string, follow bool) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Printf("Searching for container '%s'...\n", containerIDOrName)
	c, projectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}

	if follow {
		fmt.Printf("Streaming logs for container %s (%s) [Ctrl+C to stop]...\n", c.Name, c.ID)
		var lastStdout, lastStderr string
		for {
			logs, err := fetchContainerLogs(token, projectID, c.ID)
			if err != nil {
				return err
			}
			if len(logs.Stdout) > len(lastStdout) {
				fmt.Print(logs.Stdout[len(lastStdout):])
				lastStdout = logs.Stdout
			}
			if len(logs.Stderr) > len(lastStderr) {
				fmt.Fprint(os.Stderr, logs.Stderr[len(lastStderr):])
				lastStderr = logs.Stderr
			}
			time.Sleep(2 * time.Second)
		}
	} else {
		logs, err := fetchContainerLogs(token, projectID, c.ID)
		if err != nil {
			return err
		}
		if logs.Stdout != "" {
			fmt.Println("--- STDOUT ---")
			fmt.Print(logs.Stdout)
		}
		if logs.Stderr != "" {
			fmt.Println("--- STDERR ---")
			fmt.Print(logs.Stderr)
		}
	}
	return nil
}

func organizationsFlow() error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	orgs, err := fetchOrganizations(token)
	if err != nil {
		return err
	}

	if len(orgs) == 0 {
		fmt.Println("You do not belong to any organizations.")
		return nil
	}

	fmt.Println("Organizations:")
	fmt.Printf("%-28s %-20s %-12s %-25s\n", "ID", "NAME", "SLUG", "ROLE")
	fmt.Println(strings.Repeat("-", 90))
	for _, o := range orgs {
		fmt.Printf("%-28s %-20s %-12s %-25s\n", o.ID, o.Name, o.Slug, o.Role)
	}
	return nil
}
