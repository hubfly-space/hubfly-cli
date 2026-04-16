package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

type deployPreparedBuild struct {
	DockerfilePath  string
	BuilderVersion  string
	BuildSource     string
	BuildSourcePath string
	Warnings        []string
}

func canUseTUI() bool {
	termName := strings.TrimSpace(strings.ToLower(os.Getenv("TERM")))
	if termName == "" || termName == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func terminalWidth(defaultWidth int) int {
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return defaultWidth
}

func printDeployHeader(projectDir string) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	subtleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	name := filepath.Base(projectDir)
	if strings.TrimSpace(name) == "" {
		name = projectDir
	}

	fmt.Println(titleStyle.Render("Hubfly Deploy"))
	fmt.Println(subtleStyle.Render(name + "  " + projectDir))
	fmt.Println()
}

func printDeployStep(title, detail string) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	subtleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	fmt.Println(titleStyle.Render(title))
	if strings.TrimSpace(detail) != "" {
		fmt.Println(subtleStyle.Render(detail))
	}
}

func printDeploySummary(projectDir string, cfg deployConfigFile, prepared deployPreparedBuild) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	rows := [][2]string{
		{"Project", displayDeployValue(cfg.Project.Name, "Create new project")},
		{"Region", displayDeployValue(cfg.Project.Region, "Choose during deploy")},
		{"Container", displayDeployValue(cfg.Container.Name, "app")},
		{"Build", displayDeployBuildSource(projectDir, prepared)},
		{"Resources", formatDeployResources(cfg)},
		{"Ports", formatDeployPorts(cfg.Deploy.Ports)},
		{"Volumes", formatDeployVolumes(cfg.Deploy.Volumes)},
		{"Config", deployConfigPath(projectDir)},
	}

	if strings.TrimSpace(prepared.BuilderVersion) != "" {
		rows = append(rows, [2]string{"Builder", prepared.BuilderVersion})
	}

	maxLabelWidth := 0
	for _, row := range rows {
		if len(row[0]) > maxLabelWidth {
			maxLabelWidth = len(row[0])
		}
	}

	var body strings.Builder
	body.WriteString(titleStyle.Render("Deployment Plan"))
	body.WriteString("\n\n")

	for _, row := range rows {
		body.WriteString(labelStyle.Width(maxLabelWidth).Render(row[0]))
		body.WriteString("  ")
		body.WriteString(valueStyle.Render(row[1]))
		body.WriteString("\n")
	}

	width := terminalWidth(88) - 4
	if width < 52 {
		width = 52
	}
	if width > 96 {
		width = 96
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(1, 2).
		Width(width)

	fmt.Println(boxStyle.Render(strings.TrimRight(body.String(), "\n")))
	fmt.Println()
}

func printDeployWarnings(title string, warnings []string) {
	if len(warnings) == 0 {
		return
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	unique := make([]string, 0, len(warnings))
	seen := make(map[string]struct{}, len(warnings))
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		if _, ok := seen[warning]; ok {
			continue
		}
		seen[warning] = struct{}{}
		unique = append(unique, warning)
	}
	if len(unique) == 0 {
		return
	}
	sort.Strings(unique)

	var body strings.Builder
	body.WriteString(titleStyle.Render(title))
	body.WriteString("\n\n")
	for _, warning := range unique {
		body.WriteString(textStyle.Render("- " + warning))
		body.WriteString("\n")
	}

	width := terminalWidth(88) - 4
	if width < 52 {
		width = 52
	}
	if width > 96 {
		width = 96
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("11")).
		Padding(1, 2).
		Width(width)

	fmt.Println(boxStyle.Render(strings.TrimRight(body.String(), "\n")))
	fmt.Println()
}

func promptDeployReviewChoice(created bool) (bool, error) {
	if canUseTUI() {
		subtitle := "Launch now or open hubfly.build.json before the local build starts."
		if created {
			subtitle = "hubfly.build.json was created with defaults. Launch now or review it first."
		}
		options := []listOption{
			{
				Title: "Deploy now",
				Desc:  "Use the current plan and start the local image build",
			},
			{
				Title: "Review hubfly.build.json",
				Desc:  "Open the config file to tweak resources, env, ports, or the Dockerfile path",
			},
		}
		idx, cancelled, err := tuiPickOne("Deploy Options", subtitle, options)
		if err != nil {
			return false, err
		}
		if cancelled {
			return false, errors.New("deployment cancelled")
		}
		return idx == 1, nil
	}

	return promptYesNo("Review hubfly.build.json before build", created)
}

func selectProjectIndex(projects []project) (int, error) {
	if canUseTUI() {
		options := []listOption{{
			Title: "Create new project",
			Desc:  "Provision a new Hubfly project and choose its region",
		}}
		for _, project := range projects {
			desc := strings.TrimSpace(project.Region.Name)
			if strings.TrimSpace(project.Region.Location) != "" {
				desc = strings.TrimSpace(project.Region.Name + "  " + project.Region.Location)
			}
			options = append(options, listOption{
				Title: project.Name,
				Desc:  desc,
			})
		}
		idx, cancelled, err := tuiPickOne("Deploy Project", "Select where this deploy should land.", options)
		if err != nil {
			return 0, err
		}
		if cancelled {
			return 0, errors.New("deployment cancelled")
		}
		return idx + 1, nil
	}

	fmt.Println("Select a project:")
	fmt.Println("  1. Create new project")
	for idx, p := range projects {
		fmt.Printf("  %d. %s [%s]\n", idx+2, p.Name, p.Region.Name)
	}
	return promptMenuSelection("Project", len(projects)+1, 1)
}

func selectRegionIndex(availableRegions []region) (int, error) {
	if canUseTUI() {
		options := make([]listOption, 0, len(availableRegions))
		for _, entry := range availableRegions {
			desc := entry.Location
			if strings.TrimSpace(desc) == "" {
				desc = entry.PrimaryIP
			}
			options = append(options, listOption{
				Title: entry.Name,
				Desc:  desc,
			})
		}
		idx, cancelled, err := tuiPickOne("Deploy Region", "Pick the region that will host this project.", options)
		if err != nil {
			return 0, err
		}
		if cancelled {
			return 0, errors.New("deployment cancelled")
		}
		return idx + 1, nil
	}

	fmt.Println("Select a region:")
	for idx, entry := range availableRegions {
		fmt.Printf("  %d. %s [%s]\n", idx+1, entry.Name, entry.Location)
	}
	return promptMenuSelection("Region", len(availableRegions), 1)
}

func displayDeployBuildSource(projectDir string, prepared deployPreparedBuild) string {
	source := strings.TrimSpace(prepared.BuildSource)
	switch source {
	case "dockerfile":
		return "Dockerfile  " + displayRelativePath(projectDir, prepared.BuildSourcePath)
	case "generated":
		if strings.TrimSpace(prepared.BuilderVersion) == "" {
			return "Generated Dockerfile"
		}
		return "Generated by hubfly-builder " + prepared.BuilderVersion
	default:
		if strings.TrimSpace(prepared.BuildSourcePath) != "" {
			return displayRelativePath(projectDir, prepared.BuildSourcePath)
		}
		return "Unknown"
	}
}

func displayRelativePath(projectDir, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if rel, err := filepath.Rel(projectDir, path); err == nil && rel != "" && rel != "." {
		return rel
	}
	return path
}

func displayDeployValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func formatDeployResources(cfg deployConfigFile) string {
	return fmt.Sprintf("%.2f vCPU / %d MB RAM / %d GB disk", cfg.Deploy.Resources.CPU, cfg.Deploy.Resources.RAM, cfg.Deploy.Resources.Storage)
}

func formatDeployPorts(ports []deployPort) string {
	if len(ports) == 0 {
		return "No published ports"
	}
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		if port.Container <= 0 {
			continue
		}
		values = append(values, fmt.Sprintf("%d/%s", port.Container, normalizePortProtocol(port.Protocol)))
	}
	if len(values) == 0 {
		return "No published ports"
	}
	return strings.Join(values, ", ")
}

func formatDeployVolumes(volumes []deployVolume) string {
	if len(volumes) == 0 {
		return "No persistent volumes"
	}
	values := make([]string, 0, len(volumes))
	for _, volume := range volumes {
		name := strings.TrimSpace(volume.Name)
		mount := strings.TrimSpace(volume.MountPath)
		if name == "" || mount == "" {
			continue
		}
		values = append(values, fmt.Sprintf("%s -> %s", name, mount))
	}
	if len(values) == 0 {
		return "No persistent volumes"
	}
	return strings.Join(values, ", ")
}
