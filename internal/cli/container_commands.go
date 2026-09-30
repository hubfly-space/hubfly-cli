package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

func runContainerGroup(args []string) error {
	if len(args) == 0 {
		return containerListFlow(nil)
	}

	command := args[0]
	rest := args[1:]

	switch command {
	case "list", "ls", "ps":
		return containerListFlow(rest)
	case "inspect", "info":
		if len(rest) < 1 {
			return errors.New("usage: hubfly container inspect <containerIdOrName>")
		}
		return containerInspectFlow(rest[0])
	case "start":
		if len(rest) < 1 {
			return errors.New("usage: hubfly container start <containerIdOrName>")
		}
		return containerControlFlow(rest[0], "start")
	case "stop":
		if len(rest) < 1 {
			return errors.New("usage: hubfly container stop <containerIdOrName>")
		}
		return containerControlFlow(rest[0], "stop")
	case "restart":
		if len(rest) < 1 {
			return errors.New("usage: hubfly container restart <containerIdOrName>")
		}
		return containerControlFlow(rest[0], "restart")
	case "delete", "rm":
		if len(rest) < 1 {
			return errors.New("usage: hubfly container delete <containerIdOrName> [--yes|-y]")
		}
		autoConfirm := len(rest) > 1 && (rest[1] == "--yes" || rest[1] == "-y" || rest[1] == "-f" || rest[1] == "--force")
		return containerDeleteFlow(rest[0], autoConfirm)
	case "logs":
		if len(rest) < 1 {
			return errors.New("usage: hubfly container logs <containerIdOrName> [--follow|-f]")
		}
		return logsFlow(rest[0], len(rest) > 1 && (rest[1] == "--follow" || rest[1] == "-f"))
	case "ssh":
		if len(rest) == 1 {
			return sshFlow(rest[0])
		}
		if len(rest) >= 3 && rest[1] == "--" {
			return execFlow(rest[0], rest[2:], 55*time.Second)
		}
		return errors.New("usage: hubfly container ssh <containerIdOrName> [-- <cmd> [args...]]")
	case "exec":
		if len(rest) < 3 || rest[1] != "--" {
			return errors.New("usage: hubfly container exec <containerIdOrName> -- <cmd> [args...]")
		}
		return execFlow(rest[0], rest[2:], 55*time.Second)
	case "tunnel":
		if len(rest) != 3 {
			return errors.New("usage: hubfly container tunnel <containerIdOrName> <localPort> <targetPort>")
		}
		return runLegacyTunnel(rest)
	default:
		// If the first argument is not a known subcommand but might be a container name,
		// and there are 2 args, check if the second is logs/ssh/exec
		return fmt.Errorf("unknown container command %q (available: list, inspect, start, stop, restart, delete, logs, ssh, exec, tunnel)", command)
	}
}

func runLegacyTunnel(args []string) error {
	var localPort, targetPort int
	_, err1 := fmt.Sscanf(args[1], "%d", &localPort)
	_, err2 := fmt.Sscanf(args[2], "%d", &targetPort)
	if err1 != nil || err2 != nil || localPort <= 0 || targetPort <= 0 {
		return errors.New("invalid tunnel port")
	}
	return tunnelFlow(args[0], localPort, targetPort)
}

type containerListOptions struct {
	projectFilter string
	orgFilter     string
}

func parseContainerListOptions(args []string) (containerListOptions, error) {
	var opts containerListOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return opts, errors.New("--project requires an argument")
			}
			opts.projectFilter = args[i+1]
			i++
		case "--org", "-o":
			if i+1 >= len(args) {
				return opts, errors.New("--org requires an argument")
			}
			opts.orgFilter = args[i+1]
			i++
		case "--all", "-a":
			// default is all
		default:
			if !strings.HasPrefix(args[i], "-") && opts.projectFilter == "" {
				opts.projectFilter = args[i]
			} else {
				return opts, fmt.Errorf("unexpected argument: %s", args[i])
			}
		}
	}
	return opts, nil
}

func containerListFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	opts, err := parseContainerListOptions(args)
	if err != nil {
		return err
	}

	orgID := ""
	if opts.orgFilter != "" {
		orgs, err := fetchOrganizations(token)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			if o.ID == opts.orgFilter || o.Slug == opts.orgFilter {
				orgID = o.ID
				break
			}
		}
		if orgID == "" {
			return fmt.Errorf("organization '%s' not found", opts.orgFilter)
		}
	}

	projects, err := fetchProjectsFiltered(token, orgID, "cell")
	if err != nil {
		return err
	}

	if opts.projectFilter != "" {
		filtered := make([]project, 0)
		for _, p := range projects {
			if p.ID == opts.projectFilter || strings.EqualFold(p.Name, opts.projectFilter) {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			// Also check projects of any type in case it's not marked cell
			allProjects, fetchErr := fetchAllProjectsAnyType(token, orgID)
			if fetchErr == nil {
				for _, p := range allProjects {
					if p.ID == opts.projectFilter || strings.EqualFold(p.Name, opts.projectFilter) {
						filtered = append(filtered, p)
					}
				}
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("project '%s' not found", opts.projectFilter)
		}
		projects = filtered
	}

	type containerRow struct {
		container
		projectName string
	}

	var allContainers []containerRow
	for _, p := range projects {
		containers, err := fetchProjectContainers(token, p.ID)
		if err != nil {
			continue
		}
		for _, c := range containers {
			allContainers = append(allContainers, containerRow{
				container:   c,
				projectName: p.Name,
			})
		}
	}

	if len(allContainers) == 0 {
		if opts.projectFilter != "" {
			fmt.Printf("No containers found in project '%s'.\n", opts.projectFilter)
		} else {
			fmt.Println("No containers found.")
		}
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tNAME\tSTATUS\tTIER\tCPU\tRAM(MB)\tPORTS\tPROJECT\tID")
	for i, row := range allContainers {
		portCount := len(row.Networking.Ports)
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%.2f\t%.0f\t%d\t%s\t%s\n",
			i+1,
			row.Name,
			row.Status,
			row.Tier,
			row.Resources.CPU,
			row.Resources.RAM,
			portCount,
			row.projectName,
			row.ID,
		)
	}
	return tw.Flush()
}

func containerInspectFlow(containerIDOrName string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Printf("Resolving container '%s'...\n", containerIDOrName)
	c, projectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}

	details, err := fetchContainerDetails(token, projectID, c.ID)
	if err != nil {
		// Fallback to basic container struct if detailed endpoint is unavailable
		fmt.Printf("\nContainer: %s (%s)\n", c.Name, c.ID)
		fmt.Printf("Project:   %s\n", projectID)
		fmt.Printf("Status:    %s\n", c.Status)
		fmt.Printf("Tier:      %s\n", c.Tier)
		fmt.Printf("Source:    %s\n", c.Source.Type)
		fmt.Printf("Resources: CPU: %.2f | RAM: %.0fMB | Storage: %.0fGB\n", c.Resources.CPU, c.Resources.RAM, c.Resources.Storage)
		if c.PrimaryNetworkAlias != "" {
			fmt.Printf("Alias:     %s\n", c.PrimaryNetworkAlias)
		}
		return nil
	}

	fmt.Printf("\nContainer: %s\n", details.Name)
	fmt.Printf("ID:        %s\n", details.ID)
	fmt.Printf("Project:   %s (%s)\n", details.ProjectName, details.ProjectID)
	if details.Region.Name != "" {
		fmt.Printf("Region:    %s (%s)\n", details.Region.Name, details.Region.Location)
	}
	fmt.Printf("Status:    %s\n", details.Status)
	fmt.Printf("Kind:      %s\n", details.Kind)
	fmt.Printf("Tier:      %s\n", details.Tier)

	sourceDesc := details.Source.Type
	if details.Source.DockerImage != "" {
		sourceDesc += fmt.Sprintf(" (image: %s)", details.Source.DockerImage)
	} else if details.Source.GitRepository != "" {
		sourceDesc += fmt.Sprintf(" (repo: %s, branch: %s)", details.Source.GitRepository, details.Source.Branch)
	} else if details.Source.Template != "" {
		sourceDesc += fmt.Sprintf(" (template: %s)", details.Source.Template)
	}
	fmt.Printf("Source:    %s\n", sourceDesc)

	fmt.Printf("Resources: CPU: %.2f vCPU | RAM: %.0f MB | Storage: %.0f GB\n",
		details.Resources.CPU, details.Resources.RAM, details.Resources.Storage)

	if details.PrimaryNetworkAlias != "" {
		fmt.Printf("Mesh IP:   %s\n", details.PrimaryNetworkAlias)
	}

	if len(details.Networking.Ports) > 0 {
		var portStrs []string
		for _, p := range details.Networking.Ports {
			str := fmt.Sprintf("%d/%s", p.Container, p.Protocol)
			if p.TunnelURL != "" {
				str += fmt.Sprintf(" (%s)", p.TunnelURL)
			}
			portStrs = append(portStrs, str)
		}
		fmt.Printf("Ports:     %s\n", strings.Join(portStrs, ", "))
	}

	if details.CreatedAt != "" {
		fmt.Printf("Created:   %s\n", details.CreatedAt)
	}
	if details.UpdatedAt != "" {
		fmt.Printf("Updated:   %s\n", details.UpdatedAt)
	}

	return nil
}

func containerControlFlow(containerIDOrName, action string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	c, projectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}

	fmt.Printf("Executing '%s' on container %s (%s)...\n", action, c.Name, c.ID)
	if err := controlProjectContainer(token, projectID, c.ID, action); err != nil {
		return fmt.Errorf("failed to %s container: %w", action, err)
	}

	fmt.Printf("Container '%s' (%s): %s signal sent successfully.\n", c.Name, c.ID, action)
	return nil
}

func containerDeleteFlow(containerIDOrName string, autoConfirm bool) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	c, projectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}

	if !autoConfirm {
		confirmed, promptErr := promptYesNo(fmt.Sprintf("Permanently delete container '%s' (%s) in project '%s'?", c.Name, c.ID, projectID), false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Printf("Deleting container %s (%s)...\n", c.Name, c.ID)
	if err := removeProjectContainer(token, projectID, c.ID); err != nil {
		return fmt.Errorf("failed to delete container: %w", err)
	}

	fmt.Printf("Container '%s' (%s) deleted successfully.\n", c.Name, c.ID)
	return nil
}
