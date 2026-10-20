package cli

// Observation commands shared by `hubfly db` and `hubfly realtime`: logs,
// metrics and operations. service is managedServiceDatabase or
// managedServiceRealtime. (Placeholder: implemented by the db-data work item.)

func runManagedObserveCommand(service, command string, args []string) error {
	return notImplemented("hubfly " + managedGroupFor(service).Command + " " + command)
}
