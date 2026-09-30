package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

func runVMGroup(args []string) error {
	if len(args) == 0 {
		return vmListFlow(nil)
	}

	command := args[0]
	rest := args[1:]

	switch command {
	case "list", "ls", "ps":
		return vmListFlow(rest)
	case "inspect", "info", "status":
		if len(rest) < 1 {
			return errors.New("usage: hubfly vm inspect <vmIdOrName>")
		}
		return vmInspectFlow(rest[0])
	case "start":
		if len(rest) < 1 {
			return errors.New("usage: hubfly vm start <vmIdOrName>")
		}
		return vmControlFlow(rest[0], "start")
	case "stop", "shutdown":
		if len(rest) < 1 {
			return errors.New("usage: hubfly vm stop <vmIdOrName>")
		}
		return vmControlFlow(rest[0], "shutdown")
	case "restart", "reboot":
		if len(rest) < 1 {
			return errors.New("usage: hubfly vm restart <vmIdOrName>")
		}
		return vmControlFlow(rest[0], "reboot")
	case "force-stop":
		if len(rest) < 1 {
			return errors.New("usage: hubfly vm force-stop <vmIdOrName>")
		}
		return vmControlFlow(rest[0], "force-stop")
	case "resize":
		if len(rest) < 1 {
			return errors.New("usage: hubfly vm resize <vmIdOrName> [--vcpu <n>] [--ram <mb>] [--disk <gb>] [--mode <try_live_then_restart|restart_now|next_boot>]")
		}
		return vmResizeFlow(rest[0], rest[1:])
	case "delete", "rm":
		if len(rest) < 1 {
			return errors.New("usage: hubfly vm delete <vmIdOrName> [--yes|-y]")
		}
		autoConfirm := len(rest) > 1 && (rest[1] == "--yes" || rest[1] == "-y" || rest[1] == "-f" || rest[1] == "--force")
		return vmDeleteFlow(rest[0], autoConfirm)
	case "images":
		return vmImagesFlow(rest)
	case "create":
		return vmCreateFlow(rest)
	default:
		return fmt.Errorf("unknown vm command %q (available: list, inspect, start, stop, restart, force-stop, resize, delete, images, create)", command)
	}
}

func findBox(token, boxIDOrName string) (*Box, string, error) {
	allProjects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return nil, "", err
	}

	// Prioritize box projects
	for _, p := range allProjects {
		if p.Type != "" && p.Type != "box" {
			continue
		}
		boxes, err := fetchProjectBoxes(token, p.ID)
		if err != nil {
			continue
		}
		for _, b := range boxes {
			if b.ID == boxIDOrName || b.Name == boxIDOrName || strings.EqualFold(b.Name, boxIDOrName) {
				b.ProjectName = p.Name
				return &b, p.ID, nil
			}
		}
	}

	// Secondary check across any project type
	for _, p := range allProjects {
		if p.Type == "box" {
			continue
		}
		boxes, err := fetchProjectBoxes(token, p.ID)
		if err != nil {
			continue
		}
		for _, b := range boxes {
			if b.ID == boxIDOrName || b.Name == boxIDOrName || strings.EqualFold(b.Name, boxIDOrName) {
				b.ProjectName = p.Name
				return &b, p.ID, nil
			}
		}
	}

	return nil, "", fmt.Errorf("virtual machine '%s' not found in any project", boxIDOrName)
}

type vmListOptions struct {
	projectFilter string
	orgFilter     string
}

func parseVMListOptions(args []string) (vmListOptions, error) {
	var opts vmListOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return opts, errors.New("--project requires an argument")
			}
			opts.projectFilter = args[i+1]
			i++
		case "--org", "-o":
			if i+1 >= len(args) {
				return opts, errors.New("--org requires an argument")
			}
			opts.orgFilter = args[i+1]
			i++
		case "--all", "-a":
			// default
		default:
			if !strings.HasPrefix(args[i], "-") && opts.projectFilter == "" {
				opts.projectFilter = args[i]
			} else {
				return opts, fmt.Errorf("unexpected argument: %s", args[i])
			}
		}
	}
	return opts, nil
}

func vmListFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	opts, err := parseVMListOptions(args)
	if err != nil {
		return err
	}

	orgID := ""
	if opts.orgFilter != "" {
		orgs, err := fetchOrganizations(token)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			if o.ID == opts.orgFilter || o.Slug == opts.orgFilter {
				orgID = o.ID
				break
			}
		}
		if orgID == "" {
			return fmt.Errorf("organization '%s' not found", opts.orgFilter)
		}
	}

	allProjects, err := fetchAllProjectsAnyType(token, orgID)
	if err != nil {
		return err
	}

	var boxProjects []project
	for _, p := range allProjects {
		if opts.projectFilter != "" {
			if p.ID == opts.projectFilter || strings.EqualFold(p.Name, opts.projectFilter) {
				boxProjects = append(boxProjects, p)
			}
		} else if p.Type == "box" || p.Type == "" {
			boxProjects = append(boxProjects, p)
		}
	}

	if opts.projectFilter != "" && len(boxProjects) == 0 {
		return fmt.Errorf("project '%s' not found", opts.projectFilter)
	}

	type boxRow struct {
		Box
		projectName string
	}

	var allBoxes []boxRow
	for _, p := range boxProjects {
		boxes, err := fetchProjectBoxes(token, p.ID)
		if err != nil {
			continue
		}
		for _, b := range boxes {
			allBoxes = append(allBoxes, boxRow{
				Box:         b,
				projectName: p.Name,
			})
		}
	}

	if len(allBoxes) == 0 {
		if opts.projectFilter != "" {
			fmt.Printf("No virtual machines found in project '%s'.\n", opts.projectFilter)
		} else {
			fmt.Println("No virtual machines found.")
		}
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tNAME\tSTATUS\tIMAGE\tVCPU\tRAM(MB)\tDISK(GB)\tPRIVATE IP\tPROJECT\tID")
	for i, row := range allBoxes {
		ip := "-"
		if row.PrivateIPv4 != nil && *row.PrivateIPv4 != "" {
			ip = *row.PrivateIPv4
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n",
			i+1,
			row.Name,
			row.Status,
			row.ImageID,
			row.VCPUs,
			row.MemoryMiB,
			row.RootDiskGiB,
			ip,
			row.projectName,
			row.ID,
		)
	}
	return tw.Flush()
}

func vmInspectFlow(boxIDOrName string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Printf("Resolving virtual machine '%s'...\n", boxIDOrName)
	box, projectID, err := findBox(token, boxIDOrName)
	if err != nil {
		return err
	}

	// Attempt to sync live runtime from Hubbox
	runtimeBox, err := fetchBoxRuntime(token, projectID, box.ID)
	if err == nil && runtimeBox != nil {
		box = runtimeBox
	}

	fmt.Printf("\nVirtual Machine: %s\n", box.Name)
	fmt.Printf("ID:              %s\n", box.ID)
	fmt.Printf("Project:         %s\n", projectID)
	fmt.Printf("Status:          %s\n", box.Status)
	fmt.Printf("Base Image:      %s\n", box.ImageID)
	fmt.Printf("Compute (vCPU):  %d\n", box.VCPUs)
	fmt.Printf("Memory (RAM):    %d MiB (%.1f GiB)\n", box.MemoryMiB, float64(box.MemoryMiB)/1024.0)
	fmt.Printf("Root Disk:       %d GiB\n", box.RootDiskGiB)

	if box.PrivateIPv4 != nil && *box.PrivateIPv4 != "" {
		fmt.Printf("Private IPv4:    %s\n", *box.PrivateIPv4)
	}
	if box.InitializationStatus != "" {
		fmt.Printf("Init Status:     %s\n", box.InitializationStatus)
	}
	if box.ActiveOperationID != nil && *box.ActiveOperationID != "" {
		fmt.Printf("Active Task:     %s\n", *box.ActiveOperationID)
	}
	if box.CreatedAt != "" {
		fmt.Printf("Created:         %s\n", box.CreatedAt)
	}

	return nil
}

func vmControlFlow(boxIDOrName, action string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	box, projectID, err := findBox(token, boxIDOrName)
	if err != nil {
		return err
	}

	apiAction := action
	switch action {
	case "stop":
		apiAction = "shutdown"
	case "restart":
		apiAction = "reboot"
	}

	fmt.Printf("Executing '%s' on virtual machine %s (%s)...\n", action, box.Name, box.ID)
	if err := controlProjectBox(token, projectID, box.ID, apiAction); err != nil {
		return fmt.Errorf("failed to %s VM: %w", action, err)
	}

	fmt.Printf("Virtual machine '%s' (%s): %s signal dispatched successfully.\n", box.Name, box.ID, action)
	return nil
}

func vmResizeFlow(boxIDOrName string, args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	box, projectID, err := findBox(token, boxIDOrName)
	if err != nil {
		return err
	}

	var input BoxResizeInput
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--vcpu", "-c":
			if i+1 >= len(args) {
				return errors.New("--vcpu requires an integer value")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v <= 0 {
				return errors.New("invalid --vcpu value")
			}
			input.VCPUs = v
			i++
		case "--ram", "-m":
			if i+1 >= len(args) {
				return errors.New("--ram requires an integer value in MiB")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v <= 0 {
				return errors.New("invalid --ram value")
			}
			input.MemoryMiB = v
			i++
		case "--disk", "-d":
			if i+1 >= len(args) {
				return errors.New("--disk requires an integer value in GiB")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v <= 0 {
				return errors.New("invalid --disk value")
			}
			input.RootDiskGiB = v
			i++
		case "--mode":
			if i+1 >= len(args) {
				return errors.New("--mode requires an argument (try_live_then_restart, restart_now, next_boot)")
			}
			input.Mode = args[i+1]
			i++
		case "--restart":
			input.Mode = "restart_now"
		}
	}

	if input.VCPUs == 0 && input.MemoryMiB == 0 && input.RootDiskGiB == 0 {
		return errors.New("specify at least one of --vcpu, --ram, or --disk to resize")
	}

	fmt.Printf("Resizing virtual machine %s (%s)...\n", box.Name, box.ID)
	if err := resizeProjectBox(token, projectID, box.ID, input); err != nil {
		return fmt.Errorf("failed to resize VM: %w", err)
	}

	var changes []string
	if input.VCPUs > 0 {
		changes = append(changes, fmt.Sprintf("vCPUs: %d", input.VCPUs))
	}
	if input.MemoryMiB > 0 {
		changes = append(changes, fmt.Sprintf("RAM: %d MiB", input.MemoryMiB))
	}
	if input.RootDiskGiB > 0 {
		changes = append(changes, fmt.Sprintf("Disk: %d GiB", input.RootDiskGiB))
	}

	fmt.Printf("Virtual machine '%s' resized successfully (%s).\n", box.Name, strings.Join(changes, ", "))
	return nil
}

func vmDeleteFlow(boxIDOrName string, autoConfirm bool) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	box, projectID, err := findBox(token, boxIDOrName)
	if err != nil {
		return err
	}

	if !autoConfirm {
		confirmed, promptErr := promptYesNo(fmt.Sprintf("Permanently delete virtual machine '%s' (%s) in project '%s'?", box.Name, box.ID, projectID), false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Printf("Deleting virtual machine %s (%s)...\n", box.Name, box.ID)
	if err := removeProjectBox(token, projectID, box.ID); err != nil {
		return fmt.Errorf("failed to delete VM: %w", err)
	}

	fmt.Printf("Virtual machine '%s' (%s) deleted successfully.\n", box.Name, box.ID)
	return nil
}

func vmImagesFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	projectID := ""
	for i := 0; i < len(args); i++ {
		if (args[i] == "--project" || args[i] == "-p") && i+1 < len(args) {
			projectID = args[i+1]
			break
		}
	}

	if projectID == "" {
		allProjects, err := fetchAllProjectsAnyType(token, "")
		if err != nil {
			return err
		}
		for _, p := range allProjects {
			if p.Type == "box" {
				projectID = p.ID
				break
			}
		}
		if projectID == "" && len(allProjects) > 0 {
			projectID = allProjects[0].ID
		}
		if projectID == "" {
			return errors.New("no projects found; please create a project first")
		}
	}

	images, err := fetchProjectBoxImages(token, projectID)
	if err != nil {
		return fmt.Errorf("failed to fetch VM images: %w", err)
	}

	if len(images) == 0 {
		fmt.Println("No VM images available in this region.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tID\tNAME\tFAMILY\tVERSION\tDEFAULT USER")
	for i, img := range images {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			i+1, img.ID, img.Name, img.Family, img.Version, img.DefaultUser)
	}
	return tw.Flush()
}

func vmCreateFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	if len(args) < 1 {
		return errors.New("usage: hubfly vm create <name> --image <imageId> [--vcpu <n>] [--ram <mb>] [--disk <gb>] [--project <id>]")
	}

	name := args[0]
	if strings.HasPrefix(name, "-") {
		return errors.New("usage: hubfly vm create <name> --image <imageId> [options]")
	}

	input := BoxCreateInput{
		Name:        name,
		VCPUs:       1,
		MemoryMiB:   1024,
		RootDiskGiB: 10,
	}

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--image", "-i":
			if i+1 >= len(args) {
				return errors.New("--image requires an argument")
			}
			input.ImageID = args[i+1]
			i++
		case "--vcpu", "-c":
			if i+1 >= len(args) {
				return errors.New("--vcpu requires an integer value")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v <= 0 {
				return errors.New("invalid --vcpu value")
			}
			input.VCPUs = v
			i++
		case "--ram", "-m":
			if i+1 >= len(args) {
				return errors.New("--ram requires an integer value in MiB")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v <= 0 {
				return errors.New("invalid --ram value")
			}
			input.MemoryMiB = v
			i++
		case "--disk", "-d":
			if i+1 >= len(args) {
				return errors.New("--disk requires an integer value in GiB")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v <= 0 {
				return errors.New("invalid --disk value")
			}
			input.RootDiskGiB = v
			i++
		case "--project", "-p":
			if i+1 >= len(args) {
				return errors.New("--project requires an argument")
			}
			input.ProjectID = args[i+1]
			i++
		case "--user":
			if i+1 >= len(args) {
				return errors.New("--user requires an argument")
			}
			input.Username = args[i+1]
			i++
		case "--ssh-key":
			if i+1 >= len(args) {
				return errors.New("--ssh-key requires an argument")
			}
			input.SSHKeys = append(input.SSHKeys, args[i+1])
			i++
		}
	}

	if input.ImageID == "" {
		return errors.New("missing required flag --image <imageId>. Run 'hubfly vm images' to view available images")
	}

	if input.ProjectID == "" {
		allProjects, err := fetchAllProjectsAnyType(token, "")
		if err != nil {
			return err
		}
		for _, p := range allProjects {
			if p.Type == "box" {
				input.ProjectID = p.ID
				break
			}
		}
		if input.ProjectID == "" && len(allProjects) > 0 {
			input.ProjectID = allProjects[0].ID
		}
		if input.ProjectID == "" {
			return errors.New("no projects found; specify --project <id>")
		}
	}

	fmt.Printf("Provisioning virtual machine '%s' (%s, %d vCPU, %d MiB RAM, %d GiB Disk) in project '%s'...\n",
		input.Name, input.ImageID, input.VCPUs, input.MemoryMiB, input.RootDiskGiB, input.ProjectID)

	created, err := createProjectBox(token, input.ProjectID, input)
	if err != nil {
		return fmt.Errorf("failed to create virtual machine: %w", err)
	}

	fmt.Printf("Virtual machine created successfully!\n")
	fmt.Printf("ID:     %s\n", created.ID)
	fmt.Printf("Name:   %s\n", created.Name)
	fmt.Printf("Status: %s\n", created.Status)
	return nil
}
