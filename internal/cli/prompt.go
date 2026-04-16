package cli

import (
	"errors"
	"fmt"
	"io"
	"math"
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

func promptStringWithDefault(label, defaultValue string) (string, error) {
	text, err := prompt(fmt.Sprintf("%s (default %s): ", label, defaultValue))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return defaultValue, nil
	}
	return strings.TrimSpace(text), nil
}

func promptFloatWithDefault(label string, defaultValue float64) (float64, error) {
	for {
		text, err := prompt(fmt.Sprintf("%s (default %.2f): ", label, defaultValue))
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(text) == "" {
			return defaultValue, nil
		}
		value, convErr := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if convErr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			fmt.Println("Please enter a valid number.")
			continue
		}
		return value, nil
	}
}

func promptYesNo(label string, defaultYes bool) (bool, error) {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	for {
		text, err := prompt(fmt.Sprintf("%s (%s): ", label, hint))
		if err != nil {
			return false, err
		}
		text = strings.ToLower(strings.TrimSpace(text))
		if text == "" {
			return defaultYes, nil
		}
		if text == "y" || text == "yes" {
			return true, nil
		}
		if text == "n" || text == "no" {
			return false, nil
		}
		fmt.Println("Please answer yes or no.")
	}
}
