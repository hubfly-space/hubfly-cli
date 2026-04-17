package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

type deployOptions struct {
	Advanced       bool
	Project        string
	Region         string
	AutoApprove    bool
	ConfigPath     string
	Detach         bool
	DockerfilePath string
	BuilderVersion string
}

func parseDeployOptions(args []string) (deployOptions, error) {
	var opts deployOptions
	rest := cloneStrings(args)
	if len(rest) > 0 && rest[0] == "advanced" {
		opts.Advanced = true
		rest = rest[1:]
	}

	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.Advanced, "advanced", opts.Advanced, "open config review mode before deploy")
	fs.StringVar(&opts.Project, "project", "", "target project id, name, or 'new'")
	fs.StringVar(&opts.Region, "region", "", "target region id or name")
	fs.BoolVar(&opts.AutoApprove, "yes", false, "skip interactive confirmation prompts")
	fs.StringVar(&opts.ConfigPath, "config", "", "path to hubfly.build.json or a project directory")
	fs.BoolVar(&opts.Detach, "detach", false, "return after upload without waiting for the deploy to finish")
	fs.StringVar(&opts.DockerfilePath, "dockerfile", "", "override the Dockerfile path for this deploy")
	fs.StringVar(&opts.BuilderVersion, "builder-version", "", "pin a specific hubfly-builder release tag, for example v1.7.1")
	if err := fs.Parse(rest); err != nil {
		return deployOptions{}, fmt.Errorf("%w\n%s", err, deployUsage())
	}
	if len(fs.Args()) > 0 {
		return deployOptions{}, fmt.Errorf("unexpected deploy arguments: %s\n%s", strings.Join(fs.Args(), " "), deployUsage())
	}
	return opts, nil
}

func deployUsage() string {
	return strings.TrimSpace(`
usage: hubfly deploy [advanced] [--advanced] [--project <id|name|new>] [--region <region>]
                     [--yes] [--config <path>] [--detach] [--dockerfile <path>]
                     [--builder-version <tag>]
`)
}

func isInteractiveShell() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
