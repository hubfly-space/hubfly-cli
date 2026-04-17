package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type deployDiffSection struct {
	Title   string
	Current string
	Desired string
}

type deployDiffPlan struct {
	Sections       []deployDiffSection
	Warnings       []string
	HasCurrent     bool
	HasDestructive bool
}

func buildDeployDiffPlan(
	projectDir string,
	cfg deployConfigFile,
	prepared deployPreparedBuild,
	current *deployContainerSnapshotResponse,
) deployDiffPlan {
	sections := []deployDiffSection{
		{
			Title:   "Image source",
			Current: deployCurrentImageSource(current),
			Desired: displayDeployBuildSource(projectDir, prepared),
		},
		{
			Title:   "Resources",
			Current: deployCurrentResources(current),
			Desired: formatDeployResources(cfg),
		},
		{
			Title:   "Ports",
			Current: deployCurrentPorts(current),
			Desired: formatDeployPorts(cfg.Deploy.Ports),
		},
		{
			Title:   "Volumes",
			Current: deployCurrentVolumes(current),
			Desired: formatDeployVolumes(cfg.Deploy.Volumes),
		},
		{
			Title:   "Environment",
			Current: deployCurrentEnvironment(current),
			Desired: deployDesiredEnvironment(cfg),
		},
		{
			Title:   "Healthcheck",
			Current: deployCurrentHealthcheck(current),
			Desired: deployDesiredHealthcheck(cfg),
		},
		{
			Title:   "Replacement",
			Current: deployCurrentReplacement(current),
			Desired: deployDesiredReplacement(cfg),
		},
	}

	warnings := deployDiffWarnings(cfg, current)
	return deployDiffPlan{
		Sections:       sections,
		Warnings:       warnings,
		HasCurrent:     current != nil,
		HasDestructive: len(warnings) > 0,
	}
}

func printDeployDiffPlan(plan deployDiffPlan) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	currentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	desiredStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	maxLabelWidth := 0
	for _, section := range plan.Sections {
		if len(section.Title) > maxLabelWidth {
			maxLabelWidth = len(section.Title)
		}
	}

	var body strings.Builder
	body.WriteString(titleStyle.Render("Deploy Diff"))
	body.WriteString("\n\n")
	for _, section := range plan.Sections {
		body.WriteString(labelStyle.Width(maxLabelWidth).Render(section.Title))
		body.WriteString("  ")
		body.WriteString(currentStyle.Render("current"))
		body.WriteString(": ")
		body.WriteString(section.Current)
		body.WriteString("\n")
		body.WriteString(strings.Repeat(" ", maxLabelWidth+2))
		body.WriteString(desiredStyle.Render("desired"))
		body.WriteString(": ")
		body.WriteString(section.Desired)
		body.WriteString("\n\n")
	}

	width := terminalWidth(92) - 4
	if width < 56 {
		width = 56
	}
	if width > 104 {
		width = 104
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(1, 2).
		Width(width)

	fmt.Println(boxStyle.Render(strings.TrimRight(body.String(), "\n")))
	fmt.Println()

	if len(plan.Warnings) > 0 {
		printDeployWarnings("Safety checks", plan.Warnings)
	}
}

func confirmDeployPlan(opts deployOptions, plan deployDiffPlan) error {
	if opts.AutoApprove {
		return nil
	}
	if !isInteractiveShell() {
		return fmt.Errorf("non-interactive deploy requires --yes after reviewing the deploy diff")
	}

	confirmed, err := promptYesNo("Continue with this deployment", !plan.HasDestructive)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("deployment cancelled")
	}
	return nil
}

func deployCurrentImageSource(current *deployContainerSnapshotResponse) string {
	if current == nil {
		return "No existing container"
	}
	return displayDeployValue(current.Container.SourceImageDisplay, "Managed by Hubfly")
}

func deployCurrentResources(current *deployContainerSnapshotResponse) string {
	if current == nil {
		return "No existing container"
	}
	return fmt.Sprintf(
		"%.2f vCPU / %d MB RAM / %d GB disk",
		current.Container.Resources.CPU,
		current.Container.Resources.RAM,
		current.Container.Resources.Storage,
	)
}

func deployCurrentPorts(current *deployContainerSnapshotResponse) string {
	if current == nil {
		return "No existing container"
	}
	return formatCurrentPorts(current.Container.Ports)
}

func deployCurrentVolumes(current *deployContainerSnapshotResponse) string {
	if current == nil {
		return "No existing container"
	}
	if len(current.Container.Volumes) == 0 {
		return "No persistent volumes"
	}
	values := make([]string, 0, len(current.Container.Volumes))
	for _, volume := range current.Container.Volumes {
		label := displayDeployValue(volume.Name, volume.DockerVolumeName)
		values = append(values, fmt.Sprintf("%s -> %s", label, volume.MountPoint))
	}
	return strings.Join(values, ", ")
}

func deployCurrentEnvironment(current *deployContainerSnapshotResponse) string {
	if current == nil {
		return "No existing container"
	}
	if len(current.Container.Environment) == 0 {
		return "No runtime variables"
	}
	values := make([]string, 0, len(current.Container.Environment))
	for _, entry := range current.Container.Environment {
		label := entry.Key
		if entry.IsSecret {
			label += " (secret)"
		}
		values = append(values, label)
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}

func deployDesiredEnvironment(cfg deployConfigFile) string {
	values := make([]string, 0)
	for _, entry := range cfg.Env {
		scope := normalizeEnvScope(entry.Scope)
		if scope != "runtime" && scope != "both" {
			continue
		}
		key := strings.TrimSpace(entry.Name)
		if key == "" {
			continue
		}
		if entry.Secret {
			key += " (secret)"
		}
		values = append(values, key)
	}
	if len(values) == 0 {
		return "No runtime variables"
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}

func deployCurrentHealthcheck(current *deployContainerSnapshotResponse) string {
	if current == nil {
		return "No existing container"
	}
	return formatHealthcheck(current.Container.Healthcheck)
}

func deployDesiredHealthcheck(cfg deployConfigFile) string {
	return formatConfigHealthcheck(cfg.Deploy.Healthcheck)
}

func deployCurrentReplacement(current *deployContainerSnapshotResponse) string {
	if current == nil {
		return "No bound container"
	}
	return fmt.Sprintf("%s (%s, %s)", current.Container.Name, current.Container.ID, current.Container.Status)
}

func deployDesiredReplacement(cfg deployConfigFile) string {
	if strings.TrimSpace(cfg.Container.ID) == "" {
		return "Create a new container"
	}
	return fmt.Sprintf("Replace bound container %s", cfg.Container.ID)
}

func deployDiffWarnings(cfg deployConfigFile, current *deployContainerSnapshotResponse) []string {
	if current == nil {
		return nil
	}

	warnings := make([]string, 0)
	currentPorts := currentPortSet(current.Container.Ports)
	desiredPorts := desiredPortSet(cfg.Deploy.Ports)
	for port := range currentPorts {
		if _, ok := desiredPorts[port]; !ok {
			warnings = append(warnings, "A published port will be removed: "+port)
		}
	}

	currentVolumes := currentVolumeSet(current.Container.Volumes)
	desiredVolumes := desiredVolumeSet(cfg.Deploy.Volumes)
	for volume := range currentVolumes {
		if _, ok := desiredVolumes[volume]; !ok {
			warnings = append(warnings, "A volume mount will be removed: "+volume)
		}
	}

	currentEnv := currentEnvSet(current.Container.Environment)
	desiredEnv := desiredEnvSet(cfg.Env)
	for key := range currentEnv {
		if _, ok := desiredEnv[key]; !ok {
			warnings = append(warnings, "A runtime environment variable will be removed: "+key)
		}
	}

	if current.Container.Resources.CPU > cfg.Deploy.Resources.CPU ||
		current.Container.Resources.RAM > cfg.Deploy.Resources.RAM ||
		current.Container.Resources.Storage > cfg.Deploy.Resources.Storage {
		warnings = append(warnings, "Requested resources are smaller than the current container allocation")
	}

	if current.Container.Healthcheck != nil && cfg.Deploy.Healthcheck == nil {
		warnings = append(warnings, "The current healthcheck will be removed")
	}

	sort.Strings(warnings)
	return warnings
}

func formatCurrentPorts(ports []cliDeploymentPort) string {
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
	sort.Strings(values)
	return strings.Join(values, ", ")
}

func formatHealthcheck(healthcheck *cliDeploymentHealthcheck) string {
	if healthcheck == nil || len(healthcheck.Test) == 0 {
		return "No healthcheck"
	}

	parts := []string{strings.Join(healthcheck.Test, " ")}
	if value := strings.TrimSpace(healthcheck.Interval); value != "" {
		parts = append(parts, "interval="+value)
	}
	if value := strings.TrimSpace(healthcheck.Timeout); value != "" {
		parts = append(parts, "timeout="+value)
	}
	if value := strings.TrimSpace(healthcheck.StartPeriod); value != "" {
		parts = append(parts, "start="+value)
	}
	if healthcheck.Retries > 0 {
		parts = append(parts, fmt.Sprintf("retries=%d", healthcheck.Retries))
	}
	return strings.Join(parts, "  ")
}

func formatConfigHealthcheck(healthcheck *deployHealthcheck) string {
	if healthcheck == nil || len(healthcheck.Test) == 0 {
		return "No healthcheck"
	}
	return formatHealthcheck(&cliDeploymentHealthcheck{
		Test:        cloneStrings(healthcheck.Test),
		Interval:    healthcheck.Interval,
		Timeout:     healthcheck.Timeout,
		StartPeriod: healthcheck.StartPeriod,
		Retries:     healthcheck.Retries,
	})
}

func currentPortSet(ports []cliDeploymentPort) map[string]struct{} {
	set := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		if port.Container <= 0 {
			continue
		}
		set[fmt.Sprintf("%d/%s", port.Container, normalizePortProtocol(port.Protocol))] = struct{}{}
	}
	return set
}

func desiredPortSet(ports []deployPort) map[string]struct{} {
	set := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		if port.Container <= 0 {
			continue
		}
		set[fmt.Sprintf("%d/%s", port.Container, normalizePortProtocol(port.Protocol))] = struct{}{}
	}
	return set
}

func currentVolumeSet(volumes []struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	DockerVolumeName string `json:"dockerVolumeName"`
	MountPoint       string `json:"mountPoint"`
}) map[string]struct{} {
	set := make(map[string]struct{}, len(volumes))
	for _, volume := range volumes {
		label := strings.TrimSpace(volume.DockerVolumeName)
		if label == "" {
			label = strings.TrimSpace(volume.Name)
		}
		if label == "" || strings.TrimSpace(volume.MountPoint) == "" {
			continue
		}
		set[label+"->"+strings.TrimSpace(volume.MountPoint)] = struct{}{}
	}
	return set
}

func desiredVolumeSet(volumes []deployVolume) map[string]struct{} {
	set := make(map[string]struct{}, len(volumes))
	for _, volume := range volumes {
		label := strings.TrimSpace(volume.Name)
		if label == "" || strings.TrimSpace(volume.MountPath) == "" {
			continue
		}
		set[label+"->"+strings.TrimSpace(volume.MountPath)] = struct{}{}
	}
	return set
}

func currentEnvSet(env []struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	IsSecret bool   `json:"isSecret"`
}) map[string]struct{} {
	set := make(map[string]struct{}, len(env))
	for _, entry := range env {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		set[key] = struct{}{}
	}
	return set
}

func desiredEnvSet(env []deployEnvVar) map[string]struct{} {
	set := make(map[string]struct{}, len(env))
	for _, entry := range env {
		key := strings.TrimSpace(entry.Name)
		if key == "" {
			continue
		}
		scope := normalizeEnvScope(entry.Scope)
		if scope != "runtime" && scope != "both" {
			continue
		}
		set[key] = struct{}{}
	}
	return set
}
