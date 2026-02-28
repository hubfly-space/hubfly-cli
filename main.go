package main

import (
	"fmt"
	"os"
	"strconv"

	"hubfly-cli/internal/cli"
	"hubfly-cli/internal/service"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "service" {
		port := 5600
		if len(args) >= 3 && args[1] == "--port" {
			parsed, err := strconv.Atoi(args[2])
			if err != nil || parsed <= 0 {
				fmt.Fprintln(os.Stderr, "invalid service port")
				os.Exit(1)
			}
			port = parsed
		}
		if err := service.Run(port); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	os.Exit(cli.Run(args))
}
