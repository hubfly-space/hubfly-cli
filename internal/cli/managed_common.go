package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Shared plumbing for the managed service groups: `hubfly db` (service
// "database") and `hubfly realtime` (service "realtime"). Every managed
// instance lives under /api/v1/projects/{projectId}/managed/instances, so the
// rt* types in realtime_api.go (rtInstance, rtOperation, rtPrice, rtSize)
// describe instances of either service, and the rt* helpers in
// realtime_commands.go (flags, JSON output, confirmation, spinner) are shared.

const (
	managedServiceDatabase = "database"
	managedServiceRealtime = "realtime"
)

// managedGroup describes how one managed service is presented by the CLI.
type managedGroup struct {
	Service string // "database" or "realtime"
	Command string // CLI group: "db" or "realtime"
	Arg     string // positional placeholder: "<db>" or "<server>"
	Noun    string // human noun: "database" or "realtime server"
}

func managedGroupFor(service string) managedGroup {
	if service == managedServiceRealtime {
		return managedGroup{Service: service, Command: "realtime", Arg: "<server>", Noun: "realtime server"}
	}
	return managedGroup{Service: managedServiceDatabase, Command: "db", Arg: "<db>", Noun: "database"}
}

// listManagedInstancesOf returns the instances of one service across the
// caller's managed projects (or of one named project). Projects the caller
// cannot read are skipped unless one was named.
func listManagedInstancesOf(token, projectRef, service string) ([]rtInstance, error) {
	projects, err := fetchManagedProjects(token, projectRef)
	if err != nil {
		return nil, err
	}
	result := []rtInstance{}
	for _, item := range projects {
		instances, listErr := fetchManagedInstances(token, item.ID)
		if listErr != nil {
			if projectRef != "" {
				return nil, listErr
			}
			debugf("skipping project %s: %v", item.ID, listErr)
			continue
		}
		for _, instance := range instances {
			if instance.Service != service {
				continue
			}
			instance.ProjectID = item.ID
			instance.ProjectName = item.Name
			result = append(result, instance)
		}
	}
	return result, nil
}

// resolveManagedInstanceOf finds one instance of a service by id, then by
// case-insensitive name. Ambiguous names list the candidates.
func resolveManagedInstanceOf(token, ref, projectRef, service string) (rtInstance, error) {
	group := managedGroupFor(service)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return rtInstance{}, fmt.Errorf("missing %s id or name", group.Noun)
	}
	instances, err := listManagedInstancesOf(token, projectRef, service)
	if err != nil {
		return rtInstance{}, err
	}
	var byID, byName []rtInstance
	for _, instance := range instances {
		if instance.ID == ref {
			byID = append(byID, instance)
		} else if strings.EqualFold(instance.Name, ref) {
			byName = append(byName, instance)
		}
	}
	if len(byID) == 1 {
		return byID[0], nil
	}
	if len(byName) == 1 {
		return byName[0], nil
	}
	if len(byName) > 1 {
		names := make([]string, 0, len(byName))
		for _, instance := range byName {
			names = append(names, fmt.Sprintf("%s in project '%s'", instance.ID, instance.ProjectName))
		}
		return rtInstance{}, fmt.Errorf("%s name '%s' is ambiguous (%s); use the id or --project", group.Noun, ref, strings.Join(names, ", "))
	}
	return rtInstance{}, fmt.Errorf("%s '%s' not found (list them with: hubfly %s list)", group.Noun, ref, group.Command)
}

func resolveDatabaseInstance(token, ref, projectRef string) (rtInstance, error) {
	return resolveManagedInstanceOf(token, ref, projectRef, managedServiceDatabase)
}

// waitForManagedOperation polls an instance operation until it finishes. With
// goneIsDone a 404 (the instance is gone) counts as success. The spinner and
// progress lines go to stderr; quiet suppresses them (use it with --json).
func waitForManagedOperation(token string, instance rtInstance, operationID, label string, timeout time.Duration, quiet, goneIsDone bool) (rtOperation, error) {
	spinner := newRealtimeSpinner(quiet)
	defer spinner.stop()
	deadline := time.Now().Add(timeout)
	failures := 0
	for {
		operation, err := fetchManagedOperation(token, instance.ProjectID, instance.ID, operationID)
		if err != nil {
			var apiErr *apiError
			if goneIsDone && errors.As(err, &apiErr) && apiErr.Status == 404 {
				return rtOperation{ID: operationID, Status: "succeeded"}, nil
			}
			if errors.As(err, &apiErr) && apiErr.Status < 500 {
				return rtOperation{}, err
			}
			failures++
			if failures >= 5 {
				return rtOperation{}, fmt.Errorf("lost track of operation %s: %w", operationID, err)
			}
		} else {
			failures = 0
			switch operation.Status {
			case "succeeded":
				return operation, nil
			case "failed", "canceled":
				message := operation.Status
				if operation.Error != nil && strings.TrimSpace(operation.Error.Message) != "" {
					message = operation.Error.Message
				}
				return operation, fmt.Errorf("operation %s %s: %s", operationID, operation.Status, message)
			}
			spinner.update(realtimeOperationProgress(label, operation), quiet)
		}
		if time.Now().After(deadline) {
			group := managedGroupFor(instance.Service)
			return operation, fmt.Errorf("timed out after %s waiting for operation %s; it keeps running on HubFly (check with: hubfly %s info %s)", timeout, operationID, group.Command, instance.ID)
		}
		time.Sleep(rtPollInterval)
	}
}

// managedStatusLabel renders an instance status with its running operation.
func managedStatusLabel(instance rtInstance) string {
	status := instance.Status
	if instance.ActiveOperation != nil && instance.ActiveOperation.Status != "succeeded" {
		status += " (" + instance.ActiveOperation.Kind + ")"
	}
	return status
}

// notImplemented marks a command whose implementation has not landed yet.
func notImplemented(command string) error {
	return fmt.Errorf("%s is not available in this build yet", command)
}
