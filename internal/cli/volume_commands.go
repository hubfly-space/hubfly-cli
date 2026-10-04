package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

func runVolumeGroup(args []string) error {
	if len(args) == 0 {
		return volumeListFlow(nil)
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "list", "ls":
		return volumeListFlow(rest)
	case "create":
		return volumeCreateFlow(rest)
	case "attach":
		if len(rest) < 2 {
			return errors.New("usage: hubfly volume attach <volumeIdOrName> <boxIdOrName>")
		}
		return volumeAttachFlow(rest[0], rest[1])
	case "detach":
		if len(rest) < 1 {
			return errors.New("usage: hubfly volume detach <volumeIdOrName>")
		}
		return volumeDetachFlow(rest[0])
	case "resize":
		if len(rest) < 1 {
			return errors.New("usage: hubfly volume resize <volumeIdOrName> --size <gb>")
		}
		return volumeResizeFlow(rest[0], rest[1:])
	case "delete", "rm":
		if len(rest) < 1 {
			return errors.New("usage: hubfly volume delete <volumeIdOrName> [--yes|-y]")
		}
		autoConfirm := len(rest) > 1 && (rest[1] == "--yes" || rest[1] == "-y" || rest[1] == "-f")
		return volumeDeleteFlow(rest[0], autoConfirm)
	default:
		return fmt.Errorf("unknown volume command %q (available: list, create, attach, detach, resize, delete)", cmd)
	}
}

type volumeListOptions struct {
	projectFilter string
}

func parseVolumeListOptions(args []string) (volumeListOptions, error) {
	var opts volumeListOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return opts, errors.New("--project requires an argument")
			}
			opts.projectFilter = args[i+1]
			i++
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

func volumeListFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	opts, err := parseVolumeListOptions(args)
	if err != nil {
		return err
	}

	allProjects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return err
	}

	var projects []project
	for _, p := range allProjects {
		if opts.projectFilter != "" && p.ID != opts.projectFilter && !strings.EqualFold(p.Name, opts.projectFilter) {
			continue
		}
		projects = append(projects, p)
	}

	var allVolumes []UnifiedVolume

	for _, p := range projects {
		// 1. Box volumes
		if p.Type == "" || p.Type == "box" {
			boxVols, err := fetchProjectBoxVolumes(token, p.ID)
			if err == nil {
				for _, bv := range boxVols {
					attached := bv.AttachedBoxID
					if bv.Target != "" {
						attached += " (" + bv.Target + ")"
					}
					allVolumes = append(allVolumes, UnifiedVolume{
						ID:          bv.ID,
						ProjectID:   p.ID,
						ProjectName: p.Name,
						Name:        bv.Name,
						SizeGiB:     bv.SizeGiB,
						Type:        "box",
						Status:      bv.Status,
						AttachedTo:  attached,
						Target:      bv.Target,
					})
				}
			}
		}

		// 2. Cell volumes
		if p.Type == "" || p.Type == "cell" {
			details, err := fetchProject(token, p.ID)
			if err == nil {
				for _, cv := range details.Volumes {
					size, _ := strconv.Atoi(cv.SizeGb)
					allVolumes = append(allVolumes, UnifiedVolume{
						ID:          cv.ID,
						ProjectID:   p.ID,
						ProjectName: p.Name,
						Name:        cv.Name,
						SizeGiB:     size,
						Type:        "cell",
						Status:      cv.Status,
						AttachedTo:  "-",
					})
				}
			}
		}
	}

	if len(allVolumes) == 0 {
		if opts.projectFilter != "" {
			fmt.Printf("No volumes found in project '%s'.\n", opts.projectFilter)
		} else {
			fmt.Println("No storage volumes found.")
		}
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tNAME\tTYPE\tSIZE\tSTATUS\tATTACHED TO\tPROJECT\tID")
	for i, v := range allVolumes {
		attached := v.AttachedTo
		if attached == "" {
			attached = "-"
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%d GiB\t%s\t%s\t%s\t%s\n",
			i+1,
			v.Name,
			v.Type,
			v.SizeGiB,
			v.Status,
			attached,
			v.ProjectName,
			v.ID,
		)
	}
	return tw.Flush()
}

func findVolume(token, volumeIDOrName string) (*UnifiedVolume, error) {
	allProjects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return nil, err
	}

	// 1. Check box volumes first
	for _, p := range allProjects {
		boxVols, err := fetchProjectBoxVolumes(token, p.ID)
		if err == nil {
			for _, bv := range boxVols {
				if bv.ID == volumeIDOrName || bv.Name == volumeIDOrName || strings.EqualFold(bv.Name, volumeIDOrName) {
					return &UnifiedVolume{
						ID:          bv.ID,
						ProjectID:   p.ID,
						ProjectName: p.Name,
						Name:        bv.Name,
						SizeGiB:     bv.SizeGiB,
						Type:        "box",
						Status:      bv.Status,
						AttachedTo:  bv.AttachedBoxID,
						Target:      bv.Target,
					}, nil
				}
			}
		}
	}

	// 2. Check cell volumes
	for _, p := range allProjects {
		details, err := fetchProject(token, p.ID)
		if err == nil {
			for _, cv := range details.Volumes {
				if cv.ID == volumeIDOrName || cv.Name == volumeIDOrName || strings.EqualFold(cv.Name, volumeIDOrName) {
					size, _ := strconv.Atoi(cv.SizeGb)
					return &UnifiedVolume{
						ID:          cv.ID,
						ProjectID:   p.ID,
						ProjectName: p.Name,
						Name:        cv.Name,
						SizeGiB:     size,
						Type:        "cell",
						Status:      cv.Status,
					}, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("volume '%s' not found in any project", volumeIDOrName)
}

func volumeCreateFlow(args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	var name string
	var sizeGiB int
	var volType string
	var projectID string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name", "-n":
			if i+1 >= len(args) {
				return errors.New("--name requires an argument")
			}
			name = args[i+1]
			i++
		case "--size", "-s":
			if i+1 >= len(args) {
				return errors.New("--size requires an integer value in GiB")
			}
			s, err := strconv.Atoi(args[i+1])
			if err != nil || s <= 0 {
				return errors.New("invalid --size value")
			}
			sizeGiB = s
			i++
		case "--type", "-t":
			if i+1 >= len(args) {
				return errors.New("--type requires 'box' or 'cell'")
			}
			volType = strings.ToLower(args[i+1])
			i++
		case "--project", "-p":
			if i+1 >= len(args) {
				return errors.New("--project requires an argument")
			}
			projectID = args[i+1]
			i++
		}
	}

	if name == "" {
		return errors.New("usage: hubfly volume create --name <name> --size <gb> [--type box|cell] [--project <id>]")
	}
	if sizeGiB <= 0 {
		sizeGiB = 10
	}

	allProjects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return err
	}
	if len(allProjects) == 0 {
		return errors.New("no projects found in your account")
	}

	var targetProject *project
	if projectID != "" {
		for _, p := range allProjects {
			if p.ID == projectID || strings.EqualFold(p.Name, projectID) {
				targetProject = &p
				break
			}
		}
		if targetProject == nil {
			return fmt.Errorf("project '%s' not found", projectID)
		}
	} else {
		// Prefer matching type
		if volType == "box" {
			for _, p := range allProjects {
				if p.Type == "box" {
					targetProject = &p
					break
				}
			}
		}
		if targetProject == nil {
			targetProject = &allProjects[0]
		}
	}

	if volType == "" {
		if targetProject.Type == "box" {
			volType = "box"
		} else {
			volType = "cell"
		}
	}

	fmt.Printf("Creating %s volume '%s' (%d GiB) in project '%s'...\n", volType, name, sizeGiB, targetProject.Name)

	if volType == "box" {
		created, err := createProjectBoxVolume(token, targetProject.ID, name, sizeGiB)
		if err != nil {
			return fmt.Errorf("failed to create box volume: %w", err)
		}
		fmt.Printf("Volume created successfully!\nID:     %s\nName:   %s\nSize:   %d GiB\nStatus: %s\n",
			created.ID, created.Name, created.SizeGiB, created.Status)
		return nil
	}

	// Cell volume
	payload := map[string]any{
		"name":      name,
		"sizeGb":    sizeGiB,
		"projectId": targetProject.ID,
	}
	res, err := createProjectVolume(token, targetProject.ID, payload)
	if err != nil {
		return fmt.Errorf("failed to create container volume: %w", err)
	}

	volID, _ := res["id"].(string)
	fmt.Printf("Container volume created successfully!\nID:     %s\nName:   %s\nSize:   %d GiB\n",
		volID, name, sizeGiB)
	return nil
}

func volumeAttachFlow(volumeIDOrName, boxIDOrName string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	vol, err := findVolume(token, volumeIDOrName)
	if err != nil {
		return err
	}

	if vol.Type != "box" {
		return fmt.Errorf("volume '%s' is a container volume. Container volumes are attached via service definition", vol.Name)
	}

	box, _, err := findBox(token, boxIDOrName)
	if err != nil {
		return err
	}

	fmt.Printf("Attaching volume '%s' to Box '%s'...\n", vol.Name, box.Name)
	err = mutateProjectBoxVolume(token, vol.ProjectID, vol.ID, "attach", map[string]any{
		"boxId": box.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to attach volume: %w", err)
	}

	fmt.Printf("Volume '%s' attached to Box '%s' successfully.\n", vol.Name, box.Name)
	return nil
}

func volumeDetachFlow(volumeIDOrName string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	vol, err := findVolume(token, volumeIDOrName)
	if err != nil {
		return err
	}

	if vol.Type != "box" {
		return fmt.Errorf("volume '%s' is a container volume. Detach by updating container volume mounts", vol.Name)
	}

	fmt.Printf("Detaching volume '%s'...\n", vol.Name)
	err = mutateProjectBoxVolume(token, vol.ProjectID, vol.ID, "detach", nil)
	if err != nil {
		return fmt.Errorf("failed to detach volume: %w", err)
	}

	fmt.Printf("Volume '%s' detached successfully.\n", vol.Name)
	return nil
}

func volumeResizeFlow(volumeIDOrName string, args []string) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	var sizeGiB int
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--size", "-s":
			if i+1 >= len(args) {
				return errors.New("--size requires an integer value in GiB")
			}
			s, err := strconv.Atoi(args[i+1])
			if err != nil || s <= 0 {
				return errors.New("invalid --size value")
			}
			sizeGiB = s
			i++
		}
	}

	if sizeGiB <= 0 {
		return errors.New("usage: hubfly volume resize <volumeIdOrName> --size <gb>")
	}

	vol, err := findVolume(token, volumeIDOrName)
	if err != nil {
		return err
	}

	if vol.Type != "box" {
		return fmt.Errorf("resizing container volume '%s' is not supported via this command", vol.Name)
	}

	if sizeGiB <= vol.SizeGiB {
		return fmt.Errorf("new size (%d GiB) must be greater than current size (%d GiB)", sizeGiB, vol.SizeGiB)
	}

	fmt.Printf("Resizing volume '%s' (%d GiB -> %d GiB)...\n", vol.Name, vol.SizeGiB, sizeGiB)
	err = mutateProjectBoxVolume(token, vol.ProjectID, vol.ID, "resize", map[string]any{
		"sizeGib": sizeGiB,
	})
	if err != nil {
		return fmt.Errorf("failed to resize volume: %w", err)
	}

	fmt.Printf("Volume '%s' resized to %d GiB successfully.\n", vol.Name, sizeGiB)
	return nil
}

func volumeDeleteFlow(volumeIDOrName string, autoConfirm bool) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	vol, err := findVolume(token, volumeIDOrName)
	if err != nil {
		return err
	}

	if !autoConfirm {
		confirmed, promptErr := promptYesNo(fmt.Sprintf("Permanently delete volume '%s' (%s) in project '%s'?", vol.Name, vol.ID, vol.ProjectName), false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Printf("Deleting volume '%s' (%s)...\n", vol.Name, vol.ID)

	if vol.Type == "box" {
		err = mutateProjectBoxVolume(token, vol.ProjectID, vol.ID, "delete", nil)
	} else {
		err = removeProjectVolume(token, vol.ProjectID, vol.ID)
	}

	if err != nil {
		return fmt.Errorf("failed to delete volume: %w", err)
	}

	fmt.Printf("Volume '%s' deleted successfully.\n", vol.Name)
	return nil
}
