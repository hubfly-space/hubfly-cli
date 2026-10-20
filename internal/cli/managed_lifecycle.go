package cli

// Lifecycle commands shared by `hubfly db` and `hubfly realtime`: start, stop,
// restart, resize, upgrade, rename, protect and network. service is
// managedServiceDatabase or managedServiceRealtime. (Placeholder: implemented
// by the db-core work item.)

func runManagedLifecycleCommand(service, command string, args []string) error {
	return notImplemented("hubfly " + managedGroupFor(service).Command + " " + command)
}
