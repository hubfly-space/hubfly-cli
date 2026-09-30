package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func Run(args []string) int {
	args = configureDebug(args)
	debugf("debug mode enabled")
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitCodeForError(err)
	}
	return 0
}

func exitCodeForError(err error) int {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		if apiErr.Status == 401 || apiErr.Status == 403 {
			return 3
		}
		if apiErr.Status == 409 {
			return 4
		}
		if apiErr.Status >= 400 {
			return 5
		}
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return 6
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "cancelled") || strings.Contains(message, "declined") {
		return 7
	}
	if strings.Contains(message, "usage:") || strings.Contains(message, "invalid ") || strings.Contains(message, "unknown ") || strings.Contains(message, "unexpected ") || strings.Contains(message, "requires explicit migration") {
		return 2
	}
	return 5
}

func run(args []string) error {
	if len(args) == 0 {
		return projectsFlow("")
	}
	if args[0] == "__connect-tunnel" {
		if len(args) != 4 {
			return errors.New("invalid internal tunnel invocation")
		}
		localPort, localErr := strconv.Atoi(args[2])
		targetPort, targetErr := strconv.Atoi(args[3])
		if localErr != nil || targetErr != nil || localPort <= 0 || targetPort <= 0 {
			return errors.New("invalid tunnel port")
		}
		return connectStoredTunnelFlow(args[1], localPort, targetPort)
	}
	switch args[0] {
	case "auth":
		return runAuthGroup(args[1:])
	case "login":
		return runAuthGroup(append([]string{"login"}, args[1:]...))
	case "logout":
		return runAuthGroup([]string{"logout"})
	case "whoami":
		return runAuthGroup([]string{"status"})
	case "projects":
		orgFilter, err := parseOrgFilter(args[1:])
		if err != nil {
			return err
		}
		return projectsFlow(orgFilter)
	case "orgs", "org", "organizations":
		return organizationsFlow()
	case "project":
		if len(args) >= 2 && args[1] == "list" {
			orgFilter, err := parseOrgFilter(args[2:])
			if err != nil {
				return err
			}
			return projectsFlow(orgFilter)
		}
		return errors.New("usage: hubfly project list [--org <id|slug>]")
	case "container", "containers", "c":
		return runContainerGroup(args[1:])
	case "vm", "vms", "box", "boxes":
		return runVMGroup(args[1:])
	case "start":
		return runGlobalControlCommand("start", args[1:])
	case "stop":
		return runGlobalControlCommand("stop", args[1:])
	case "restart":
		return runGlobalControlCommand("restart", args[1:])
	case "logs":
		if len(args) < 2 {
			return errors.New("usage: hubfly logs <containerIdOrName> [--follow|-f]")
		}
		return logsFlow(args[1], len(args) > 2 && (args[2] == "--follow" || args[2] == "-f"))
	case "ssh":
		return runLegacyExecCommand("ssh", args[1:])
	case "exec":
		return runLegacyExecCommand("exec", args[1:])
	case "tunnel":
		if len(args) != 4 {
			return errors.New("usage: hubfly tunnel <containerIdOrName> <localPort> <targetPort>")
		}
		localPort, localErr := strconv.Atoi(args[2])
		targetPort, targetErr := strconv.Atoi(args[3])
		if localErr != nil || targetErr != nil || localPort <= 0 || targetPort <= 0 {
			return errors.New("invalid tunnel port")
		}
		return tunnelFlow(args[1], localPort, targetPort)
	case "stack":
		return stackFlow(args[1:])
	case "build":
		return runBuildCommand(args[1:])
	case "deploy":
		return runDeployGroup(args[1:])
	case "compose":
		return stackFlow(args[1:])
	case "config":
		return configFlow(args[1:])
	case "update":
		return updateFlow(len(args) > 1 && args[1] == "--check")
	case "version", "--version", "-v":
		showVersion()
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func parseOrgFilter(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) == 2 && args[0] == "--org" && strings.TrimSpace(args[1]) != "" {
		return args[1], nil
	}
	return "", errors.New("usage: hubfly projects [--org <id|slug>]")
}

func runLegacyExecCommand(command string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: hubfly %s <containerIdOrName> [-- <cmd> [args...]]", command)
	}
	if len(args) == 1 {
		if command == "ssh" {
			return sshFlow(args[0])
		}
		return fmt.Errorf("usage: hubfly exec <containerIdOrName> -- <cmd> [args...]")
	}
	if len(args) >= 3 && args[1] == "--" {
		return execFlow(args[0], args[2:], 55*time.Second)
	}
	return fmt.Errorf("usage: hubfly %s <containerIdOrName> -- <cmd> [args...]", command)
}

func runDeployGroup(args []string) error {
	if len(args) > 0 && (args[0] == "status" || args[0] == "events" || args[0] == "cancel" || args[0] == "retry") {
		if len(args) != 2 {
			return fmt.Errorf("usage: hubfly deploy %s <buildId>", args[0])
		}
		token, err := ensureAuth(true)
		if err != nil {
			return err
		}
		switch args[0] {
		case "status":
			status, fetchErr := fetchDeploySession(token, args[1])
			if fetchErr != nil {
				return fetchErr
			}
			fmt.Printf("%s\t%s\t%s\t%s\n", status.Build.ID, status.Build.Status, status.Build.Phase, status.Build.Error)
			return nil
		case "events":
			events, fetchErr := fetchDeploySessionEvents(token, args[1])
			if fetchErr != nil {
				return fetchErr
			}
			for _, event := range events.Events {
				fmt.Printf("%s\t%-14s\t%-8s\t%s\n", event.CreatedAt, event.Phase, event.Status, event.Message)
			}
			return nil
		case "cancel":
			return cancelDeploySession(token, args[1])
		case "retry":
			return retryDeploySession(token, args[1])
		}
	}
	opts, err := parseDeployOptions(args)
	if err != nil {
		return err
	}
	return deployFlowWithOptions(opts)
}

func runAuthGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly auth <login|logout|status>")
	}
	switch args[0] {
	case "login":
		provided := ""
		if len(args) == 3 && args[1] == "--token" {
			provided = args[2]
		} else if len(args) != 1 {
			return errors.New("usage: hubfly auth login [--token <token>]")
		}
		return login(provided)
	case "logout":
		token, err := getToken()
		if err != nil {
			return err
		}
		if strings.TrimSpace(token) != "" {
			if err := revokeCurrentToken(token); err != nil {
				return fmt.Errorf("revoke token remotely: %w", err)
			}
		}
		if err := deleteToken(); err != nil {
			return err
		}
		fmt.Println("Logged out successfully.")
		return nil
	case "status":
		_, err := ensureAuth(false)
		return err
	default:
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

func runGlobalControlCommand(action string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: hubfly %s <containerOrVmIdOrName>", action)
	}
	target := args[0]
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	// Try finding as container first
	c, _, cErr := findContainer(token, target)
	if cErr == nil && c != nil {
		return containerControlFlow(target, action)
	}

	// Try finding as box/vm
	boxAction := action
	if action == "stop" {
		boxAction = "shutdown"
	} else if action == "restart" {
		boxAction = "reboot"
	}
	box, _, bErr := findBox(token, target)
	if bErr == nil && box != nil {
		return vmControlFlow(target, boxAction)
	}

	return fmt.Errorf("target '%s' not found as container or virtual machine", target)
}

func printUsage() {
	fmt.Println("Hubfly CLI - Cloud Containers, Virtual Machines & Stacks")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  hubfly login [--token <TOKEN>]")
	fmt.Println("  hubfly logout")
	fmt.Println("  hubfly whoami")
	fmt.Println("  hubfly projects [--org <id|slug>]")
	fmt.Println("  hubfly orgs")
	fmt.Println("")
	fmt.Println("Containers (Cells):")
	fmt.Println("  hubfly container list [--project <id|name>] [--org <id|slug>]")
	fmt.Println("  hubfly container inspect <containerIdOrName>")
	fmt.Println("  hubfly container start <containerIdOrName>")
	fmt.Println("  hubfly container stop <containerIdOrName>")
	fmt.Println("  hubfly container restart <containerIdOrName>")
	fmt.Println("  hubfly container delete <containerIdOrName> [--yes|-y]")
	fmt.Println("  hubfly container logs <containerIdOrName> [--follow|-f]")
	fmt.Println("  hubfly container ssh <containerIdOrName> [-- <cmd> [args...]]")
	fmt.Println("  hubfly container exec <containerIdOrName> -- <cmd> [args...]")
	fmt.Println("  hubfly container tunnel <containerIdOrName> <localPort> <targetPort>")
	fmt.Println("")
	fmt.Println("Virtual Machines (KVM Boxes):")
	fmt.Println("  hubfly vm list [--project <id|name>] [--org <id|slug>]")
	fmt.Println("  hubfly vm inspect <vmIdOrName>")
	fmt.Println("  hubfly vm start <vmIdOrName>")
	fmt.Println("  hubfly vm stop <vmIdOrName>")
	fmt.Println("  hubfly vm restart <vmIdOrName>")
	fmt.Println("  hubfly vm force-stop <vmIdOrName>")
	fmt.Println("  hubfly vm resize <vmIdOrName> [--vcpu <n>] [--ram <mb>] [--disk <gb>] [--mode <mode>]")
	fmt.Println("  hubfly vm delete <vmIdOrName> [--yes|-y]")
	fmt.Println("  hubfly vm images")
	fmt.Println("  hubfly vm create --name <name> --image <imageId> [--vcpu <n>] [--ram <mb>] [--disk <gb>]")
	fmt.Println("")
	fmt.Println("Shortcuts:")
	fmt.Println("  hubfly start <containerOrVmIdOrName>")
	fmt.Println("  hubfly stop <containerOrVmIdOrName>")
	fmt.Println("  hubfly restart <containerOrVmIdOrName>")
	fmt.Println("  hubfly tunnel <containerIdOrName> <localPort> <targetPort>")
	fmt.Println("  hubfly ssh <containerIdOrName> [-- <cmd> [args...]]")
	fmt.Println("  hubfly exec <containerIdOrName> -- <cmd> [args...]")
	fmt.Println("  hubfly logs <containerIdOrName> [--follow|-f]")
	fmt.Println("")
	fmt.Println("Deployment & Stacks:")
	fmt.Println("  hubfly deploy [advanced|--advanced] [--project <id|name|new>] [--region <region>] [--yes]")
	fmt.Println("              [--config <path>] [--detach] [--dockerfile <path>] [--builder-version <tag>]")
	fmt.Println("  hubfly stack <plan|up|status|logs|exec|ssh|down> [options]")
	fmt.Println("  hubfly build <init|validate|edit|explain> [options]")
	fmt.Println("  hubfly compose <plan|up|status|logs|exec|ssh|down> [options]")
	fmt.Println("  hubfly config <validate|migrate|explain>")
	fmt.Println("  hubfly update [--check]")
	fmt.Println("  hubfly version")
}
