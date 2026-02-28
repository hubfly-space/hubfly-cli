package cli

import (
	"fmt"
	"strings"
)

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

func renderScreen(title string, subtitle string) {
	clearScreen()
	line := strings.Repeat("=", 80)
	fmt.Println(line)
	fmt.Printf("Hubfly CLI | %s\n", title)
	if strings.TrimSpace(subtitle) != "" {
		fmt.Println(subtitle)
	}
	fmt.Println(line)
	fmt.Println()
}

func waitForEnter(message string) {
	if message == "" {
		message = "Press Enter to continue..."
	}
	_, _ = prompt(message)
}
