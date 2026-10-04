package cli

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
)

var envKeyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func runEnvGroup(args []string) error {
	if len(args) == 0 {
		return envListFlow(nil)
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "list", "ls":
		return envListFlow(rest)
	case "set":
		return envSetFlow(rest)
	case "unset", "rm", "remove":
		return envUnsetFlow(rest)
	default:
		// If first arg looks like KEY=VALUE, treat as "set"
		if strings.Contains(cmd, "=") {
			return envSetFlow(args)
		}
		return fmt.Errorf("unknown env command %q (available: list, set, unset)", cmd)
	}
}

func resolveProjectForEnv(token, explicit string) (*project, error) {
	allProjects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return nil, err
	}
	if len(allProjects) == 0 {
		return nil, errors.New("no projects found in your account")
	}

	if explicit != "" {
		for _, p := range allProjects {
			if p.ID == explicit || strings.EqualFold(p.Name, explicit) {
				return &p, nil
			}
		}
		return nil, fmt.Errorf("project '%s' not found", explicit)
	}

	// Default to first project (prioritizing cell or active projects)
	return &allProjects[0], nil
}

func envListFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	var projectFilter string
	var reveal bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return errors.New("--project requires an argument")
			}
			projectFilter = args[i+1]
			i++
		case "--reveal", "-r":
			reveal = true
		default:
			if !strings.HasPrefix(args[i], "-") && projectFilter == "" {
				projectFilter = args[i]
			} else {
				return fmt.Errorf("unexpected argument: %s", args[i])
			}
		}
	}

	proj, err := resolveProjectForEnv(token, projectFilter)
	if err != nil {
		return err
	}

	envVars, err := fetchProjectEnv(token, proj.ID)
	if err != nil {
		return fmt.Errorf("failed to fetch environment variables: %w", err)
	}

	if len(envVars) == 0 {
		fmt.Printf("No environment variables configured for project '%s'.\n", proj.Name)
		fmt.Println("Use 'hubfly env set KEY=VALUE' to add variables.")
		return nil
	}

	fmt.Printf("Environment variables for project '%s' (%s):\n\n", proj.Name, proj.ID)
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KEY\tVALUE\tSECRET")
	for _, v := range envVars {
		displayVal := v.Value
		if v.IsSecret && !reveal {
			displayVal = "••••••••"
		}
		secretStr := "no"
		if v.IsSecret {
			secretStr = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", v.Key, displayVal, secretStr)
	}
	_ = tw.Flush()

	if !reveal {
		fmt.Println("\nTip: Pass --reveal to unmask secret values.")
	}
	return nil
}

func envSetFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	var projectFilter string
	var asSecret bool
	var pairs []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return errors.New("--project requires an argument")
			}
			projectFilter = args[i+1]
			i++
		case "--secret", "-s":
			asSecret = true
		default:
			if strings.Contains(args[i], "=") {
				pairs = append(pairs, args[i])
			} else {
				return fmt.Errorf("invalid environment variable definition: '%s' (expected KEY=VALUE)", args[i])
			}
		}
	}

	if len(pairs) == 0 {
		return errors.New("usage: hubfly env set KEY=VALUE [KEY2=VALUE2...] [--secret] [--project <id|name>]")
	}

	proj, err := resolveProjectForEnv(token, projectFilter)
	if err != nil {
		return err
	}

	existing, err := fetchProjectEnv(token, proj.ID)
	if err != nil {
		// Non-fatal if empty
		existing = []ProjectEnvVar{}
	}

	envMap := make(map[string]ProjectEnvVar)
	for _, v := range existing {
		envMap[v.Key] = v
	}

	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		key := strings.TrimSpace(parts[0])
		val := parts[1]

		if !envKeyRegex.MatchString(key) {
			return fmt.Errorf("invalid environment key '%s': must begin with letter/underscore and contain alphanumeric or underscores", key)
		}

		current, exists := envMap[key]
		isSec := asSecret
		if exists && !asSecret && current.IsSecret {
			// preserve secret status if updating without explicitly changing it
			isSec = true
		}

		envMap[key] = ProjectEnvVar{
			ID:       current.ID,
			Key:      key,
			Value:    val,
			IsSecret: isSec,
		}
	}

	var updatedList []ProjectEnvVar
	for _, v := range envMap {
		updatedList = append(updatedList, v)
	}

	fmt.Printf("Updating environment variables in project '%s'...\n", proj.Name)
	res, err := updateProjectEnv(token, proj.ID, updatedList)
	if err != nil {
		return fmt.Errorf("failed to update environment variables: %w", err)
	}

	fmt.Printf("Successfully updated %d environment variable(s) (%d container(s) reloaded).\n", res.Count, res.Redeployed)
	return nil
}

func envUnsetFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	var projectFilter string
	var keysToRemove []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return errors.New("--project requires an argument")
			}
			projectFilter = args[i+1]
			i++
		default:
			if !strings.HasPrefix(args[i], "-") {
				keysToRemove = append(keysToRemove, args[i])
			} else {
				return fmt.Errorf("unexpected argument: %s", args[i])
			}
		}
	}

	if len(keysToRemove) == 0 {
		return errors.New("usage: hubfly env unset KEY [KEY2...] [--project <id|name>]")
	}

	proj, err := resolveProjectForEnv(token, projectFilter)
	if err != nil {
		return err
	}

	existing, err := fetchProjectEnv(token, proj.ID)
	if err != nil {
		return fmt.Errorf("failed to fetch environment variables: %w", err)
	}

	removeSet := make(map[string]bool)
	for _, k := range keysToRemove {
		removeSet[k] = true
	}

	var remaining []ProjectEnvVar
	removedCount := 0
	for _, v := range existing {
		if removeSet[v.Key] {
			removedCount++
		} else {
			remaining = append(remaining, v)
		}
	}

	if removedCount == 0 {
		fmt.Println("None of the specified keys existed in the project.")
		return nil
	}

	fmt.Printf("Removing %d variable(s) from project '%s'...\n", removedCount, proj.Name)
	res, err := updateProjectEnv(token, proj.ID, remaining)
	if err != nil {
		return fmt.Errorf("failed to update environment variables: %w", err)
	}

	fmt.Printf("Successfully removed variable(s) (%d container(s) reloaded).\n", res.Redeployed)
	return nil
}
