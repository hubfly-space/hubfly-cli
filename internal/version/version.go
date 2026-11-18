package version

import "runtime"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

const (
	RepoOwner = "hubfly-space"
	RepoName  = "hubfly-cli"
)

// UserAgent identifies the CLI build on outgoing HTTP requests.
func UserAgent() string {
	return "hubfly-cli/" + Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}
