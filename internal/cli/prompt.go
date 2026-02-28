package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func prompt(label string) (string, error) {
	fmt.Print(label)
	line, err := stdin.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptNumber(label string, max int) (int, error) {
	for {
		text, err := prompt(label)
		if err != nil {
			return 0, err
		}
		value, convErr := strconv.Atoi(text)
		if convErr != nil {
			fmt.Println("Please enter a valid number.")
			continue
		}
		if value < 0 || value > max {
			fmt.Println("Number out of range.")
			continue
		}
		return value, nil
	}
}

func promptNumberWithDefault(label string, defaultValue int) (int, error) {
	for {
		text, err := prompt(fmt.Sprintf("%s (default %d): ", label, defaultValue))
		if err != nil {
			return 0, err
		}
		if text == "" {
			return defaultValue, nil
		}
		value, convErr := strconv.Atoi(text)
		if convErr != nil {
			fmt.Println("Please enter a valid number.")
			continue
		}
		return value, nil
	}
}
