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
		return containerLogsFlow(rest[0], len(rest) > 1 && (rest[1] == "--follow" || rest[1] == "-f"))
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
		return fmt.Errorf("unknown container command %q (available: list, inspect, start, stop, restart, delete, logs, ssh, exec, tunnel)%s", command, didYouMean(command, []string{"list", "inspect", "start", "stop", "restart", "delete", "logs", "ssh", "exec", "tunnel"}))
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

	// One project listing serves both the Cell list and the name lookup.
	allProjects, err := fetchAllProjectsAnyType(token, orgID)
	if err != nil {
		return err
	}
	projects := make([]project, 0, len(allProjects))
	for _, p := range allProjects {
		if p.Type == "" || p.Type == "cell" {
			projects = append(projects, p)
		}
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
			for _, p := range allProjects {
				if p.ID == opts.projectFilter || strings.EqualFold(p.Name, opts.projectFilter) {
					filtered = append(filtered, p)
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
	perProject, fetchErrs := fanOutProjects(projects, func(p project) ([]container, error) {
		return fetchProjectContainers(token, p.ID)
	})
	for index, p := range projects {
		if fetchErrs[index] != nil {
			debugf("skipping project %s: %v", p.ID, fetchErrs[index])
			continue
		}
		for _, c := range perProject[index] {
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

// Exit codes for container runtime refusals (logs, stats, exec). 2-7 are the
// shared codes in exitCodeForError; an UPSTREAM_TIMEOUT keeps 6 (timeout).
const (
	exitContainerNotRunning  = 8 // stopped, sleeping, or no runtime in its region
	exitContainerRegionDown  = 9 // the container's region is temporarily unreachable
	defaultRegionRetryAfter  = 15 * time.Second
	maxRegionRetryAfter      = 5 * time.Minute
	containerRuntimeTimeout  = "UPSTREAM_TIMEOUT"
	containerRegionDown      = "REGION_UNAVAILABLE"
	containerNotRunning      = "CONTAINER_NOT_RUNNING"
	containerSleeping        = "CONTAINER_SLEEPING"
	containerRuntimeMissing  = "runtime_missing"
	containerStoppedFollowUp = "start it with 'hubfly container start %s'"
)

// containerRuntimeError is the friendly form of the engine's precise container
// runtime errors. Unwrap keeps the *apiError reachable for callers.
type containerRuntimeError struct {
	code       string
	message    string
	exitCode   int
	retryAfter time.Duration
	err        error
}

func (e *containerRuntimeError) Error() string { return e.message }
func (e *containerRuntimeError) Unwrap() error { return e.err }
func (e *containerRuntimeError) ExitCode() int { return e.exitCode }

func apiErrorDetail(apiErr *apiError, key string) any {
	details, ok := apiErr.Details.(map[string]any)
	if !ok {
		return nil
	}
	return details[key]
}

// regionRetryAfter reads details.retryAfterSeconds, falling back to 15s.
func regionRetryAfter(apiErr *apiError) time.Duration {
	if seconds, ok := apiErrorDetail(apiErr, "retryAfterSeconds").(float64); ok && seconds > 0 {
		wait := time.Duration(seconds * float64(time.Second))
		if wait > maxRegionRetryAfter {
			return maxRegionRetryAfter
		}
		return wait
	}
	return defaultRegionRetryAfter
}

// friendlyContainerRuntimeError maps CONTAINER_NOT_RUNNING, CONTAINER_SLEEPING,
// REGION_UNAVAILABLE and UPSTREAM_TIMEOUT to a clear message and exit code.
// Any other error is returned unchanged. operation is e.g. "logs", "stats".
func friendlyContainerRuntimeError(err error, container, operation string) error {
	var apiErr *apiError
	if err == nil || !errors.As(err, &apiErr) {
		return err
	}
	out := &containerRuntimeError{code: apiErr.Code, err: err}
	switch apiErr.Code {
	case containerNotRunning:
		out.exitCode = exitContainerNotRunning
		if apiErrorDetail(apiErr, "reason") == containerRuntimeMissing {
			out.message = fmt.Sprintf("container %s has no runtime in its region, so %s are unavailable; start or redeploy it ('hubfly container start %s')", container, operation, container)
		} else {
			out.message = fmt.Sprintf("container %s is stopped, so %s are unavailable; "+containerStoppedFollowUp, container, operation, container)
		}
	case containerSleeping:
		out.exitCode = exitContainerNotRunning
		out.message = fmt.Sprintf("container %s is sleeping; it wakes on the next request (or "+containerStoppedFollowUp+")", container, container)
	case containerRegionDown:
		out.exitCode = exitContainerRegionDown
		out.retryAfter = regionRetryAfter(apiErr)
		out.message = fmt.Sprintf("the region hosting container %s is temporarily unavailable; try again in %s", container, out.retryAfter.Round(time.Second))
	case containerRuntimeTimeout:
		out.exitCode = 6
		out.message = fmt.Sprintf("the region hosting container %s did not answer the %s request in time; try again", container, operation)
	default:
		return err
	}
	return out
}

// containerRuntimeRetryWait is how long a --follow loop waits before trying
// again after err, or false when err should end the command.
func containerRuntimeRetryWait(err error) (time.Duration, bool) {
	var runtimeErr *containerRuntimeError
	if !errors.As(err, &runtimeErr) {
		return 0, false
	}
	switch runtimeErr.code {
	case containerRegionDown:
		return runtimeErr.retryAfter, true
	case containerRuntimeTimeout:
		return 5 * time.Second, true
	}
	return 0, false
}

// sleepUnlessInterrupted waits d, returning false if Ctrl-C came first.
func sleepUnlessInterrupted(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-RootContext().Done():
		return false
	case <-timer.C:
		return true
	}
}

// containerLogsFlow prints a container's logs; with follow it polls and, when
// the region is unavailable or slow, waits (honoring retryAfterSeconds) and
// resumes instead of exiting.
func containerLogsFlow(containerIDOrName string, follow bool) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Searching for container '%s'...\n", containerIDOrName)
	c, projectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}

	if !follow {
		logs, err := fetchContainerLogs(token, projectID, c.ID)
		if err != nil {
			return friendlyContainerRuntimeError(err, c.Name, "logs")
		}
		if logs.Stdout != "" {
			fmt.Println("--- STDOUT ---")
			fmt.Print(logs.Stdout)
		}
		if logs.Stderr != "" {
			fmt.Println("--- STDERR ---")
			fmt.Print(logs.Stderr)
		}
		return nil
	}

	fmt.Fprintf(os.Stderr, "Streaming logs for container %s (%s) [Ctrl+C to stop]...\n", c.Name, c.ID)
	var lastStdout, lastStderr string
	for {
		logs, err := fetchContainerLogs(token, projectID, c.ID)
		if err != nil {
			if RootContext().Err() != nil {
				return nil
			}
			friendly := friendlyContainerRuntimeError(err, c.Name, "logs")
			wait, retry := containerRuntimeRetryWait(friendly)
			if !retry {
				return friendly
			}
			fmt.Fprintf(os.Stderr, "%s — retrying in %s...\n", friendly, wait.Round(time.Second))
			if !sleepUnlessInterrupted(wait) {
				return nil
			}
			continue
		}
		if len(logs.Stdout) > len(lastStdout) {
			fmt.Print(logs.Stdout[len(lastStdout):])
			lastStdout = logs.Stdout
		}
		if len(logs.Stderr) > len(lastStderr) {
			fmt.Fprint(os.Stderr, logs.Stderr[len(lastStderr):])
			lastStderr = logs.Stderr
		}
		if !sleepUnlessInterrupted(2 * time.Second) {
			return nil
		}
	}
}
