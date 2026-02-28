package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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
	fmt.Println("  hubfly [--debug] login [--token <token>]")
	fmt.Println("  hubfly [--debug] logout")
	fmt.Println("  hubfly [--debug] whoami")
	fmt.Println("  hubfly [--debug] projects")
	fmt.Println("  hubfly [--debug] tunnel <containerIdOrName> <localPort> <targetPort>")
	fmt.Println("  hubfly service [--port <port>]")
	fmt.Println("")
	fmt.Println("Debug mode:")
	fmt.Println("  --debug")
	fmt.Println("  HUBFLY_DEBUG=1")
}
