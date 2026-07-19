package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

func Run(args []string) int {
	args = configureDebug(args)
	debugf("debug mode enabled")
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
		orgFilter := ""
		for i, arg := range args {
			if arg == "--org" && i+1 < len(args) {
				orgFilter = args[i+1]
				break
			}
		}
		return projectsFlow(orgFilter)
	case "deploy":
		opts, err := parseDeployOptions(args[1:])
		if err != nil {
			return err
		}
		return deployFlowWithOptions(opts)
	case "stack":
		return stackFlow(args[1:])
	case "build":
		return runBuildCommand(args[1:])
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
	case "__connect-tunnel":
		if len(args) != 4 {
			return errors.New("usage: hubfly __connect-tunnel <tunnelId> <localPort> <targetPort>")
		}
		localPort, err := strconv.Atoi(args[2])
		if err != nil || localPort <= 0 {
			return errors.New("invalid local port")
		}
		targetPort, err := strconv.Atoi(args[3])
		if err != nil || targetPort <= 0 {
			return errors.New("invalid target port")
		}
		return connectStoredTunnelFlow(args[1], localPort, targetPort)
	case "ssh":
		if len(args) < 2 {
			return errors.New("usage: hubfly ssh <containerIdOrName> [-- <cmd> [args...]]")
		}
		if len(args) == 2 {
			return sshFlow(args[1])
		}
		if len(args) >= 4 && args[2] == "--" {
			return execFlow(args[1], args[3:], 55*time.Second)
		}
		return errors.New("usage: hubfly ssh <containerIdOrName> [-- <cmd> [args...]]")
	case "exec":
		if len(args) < 2 {
			return errors.New("usage: hubfly exec <containerIdOrName> -- <cmd> [args...]")
		}
		dashIdx := -1
		for i, a := range args {
			if a == "--" {
				dashIdx = i
				break
			}
		}
		if dashIdx == -1 || dashIdx == len(args)-1 {
			return errors.New("usage: hubfly exec <containerIdOrName> -- <cmd> [args...] (missing -- or command)")
		}
		return execFlow(args[1], args[dashIdx+1:], 55*time.Second)
	case "orgs", "org", "organizations":
		return organizationsFlow()
	case "logs":
		if len(args) < 2 {
			return errors.New("usage: hubfly logs <containerIdOrName> [--follow|-f]")
		}
		follow := false
		if len(args) >= 3 && (args[2] == "--follow" || args[2] == "-f") {
			follow = true
		}
		return logsFlow(args[1], follow)
	case "version", "--version", "-v":
		showVersion()
		return nil
	case "update":
		checkOnly := len(args) > 1 && args[1] == "--check"
		return updateFlow(checkOnly)
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
	fmt.Println("  hubfly [--debug] login [--token <token>]")
	fmt.Println("  hubfly [--debug] logout")
	fmt.Println("  hubfly [--debug] whoami")
	fmt.Println("  hubfly [--debug] projects")
	fmt.Println("  hubfly [--debug] orgs")
	fmt.Println("  hubfly [--debug] logs <containerIdOrName> [--follow|-f]")
	fmt.Println("  hubfly [--debug] deploy [advanced|--advanced] [--project <id|name|new>] [--region <region>] [--yes]")
	fmt.Println("       [--config <path>] [--detach] [--dockerfile <path>] [--builder-version <tag>]")
	fmt.Println("  hubfly [--debug] stack <plan|up|status|logs|exec|ssh|down> [options]")
	fmt.Println("  hubfly [--debug] build <init|validate|edit|explain>")
	fmt.Println("  hubfly [--debug] tunnel <containerIdOrName> <localPort> <targetPort>")
	fmt.Println("  hubfly [--debug] ssh <containerIdOrName> [-- <cmd> [args...]]")
	fmt.Println("  hubfly [--debug] exec <containerIdOrName> -- <cmd> [args...]")
	fmt.Println("  hubfly [--debug] version")
	fmt.Println("  hubfly [--debug] update [--check]")
	fmt.Println("  hubfly service [--port <port>]")
	fmt.Println("")
	fmt.Println("Deploy examples:")
	fmt.Println("  hubfly deploy")
	fmt.Println("  hubfly deploy --project new --region rw-kigali-1 --yes")
	fmt.Println("  hubfly deploy --project my-api --dockerfile ./deploy/Dockerfile --builder-version v1.7.1")
	fmt.Println("")
	fmt.Println("Build config helpers:")
	fmt.Println("  hubfly build init")
	fmt.Println("  hubfly build validate --config ./hubfly.build.json")
	fmt.Println("  hubfly build explain --json")
	fmt.Println("")
	fmt.Println("Debug mode:")
	fmt.Println("  --debug")
	fmt.Println("  HUBFLY_DEBUG=1")
}
