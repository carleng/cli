package actions

import (
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/pkg/cmdutil"
	"github.com/cli/cli/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ActionsOptions struct {
	IO *iostreams.IOStreams
}

func NewCmdActions(f *cmdutil.Factory) *cobra.Command {
	opts := ActionsOptions{
		IO: f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:    "actions",
		Short:  "Learn about working with GitHub actions",
		Long:   actionsExplainer(nil, false),
		Hidden: true,
		Run: func(cmd *cobra.Command, args []string) {
			actionsRun(opts)
		},
		Annotations: map[string]string{
			"IsActions": "true",
		},
	}

	return cmd
}

func actionsExplainer(cs *iostreams.ColorScheme, color bool) string {
	header := "Welcome to GitHub Actions on the command line."
	runHeader := "Interacting with workflow runs"
	workflowHeader := "Interacting with workflow files"
	if color {
		header = cs.Bold(header)
		runHeader = cs.Bold(runHeader)
		workflowHeader = cs.Bold(workflowHeader)
	}

	return heredoc.Docf(`
			%s

			gh integrates with Actions to help you manage runs and workflows.

			%s
			gh run list:    List recent workflow runs
			gh run view:    View details for a workflow run or one of its jobs
			gh run watch:   Watch a workflow run while it executes
			gh run rerun:   Rerun a failed workflow run

			To see more help, run 'gh help run <subcommand>'

			%s
			gh workflow list:      List all the workflow files in your repository
			gh workflow enable:    Enable a workflow file
			gh workflow disable:   Disable a workflow file
			gh workflow run:       Trigger a workflow_dispatch run for a workflow file

			To see more help, run 'gh help workflow <subcommand>'

			For more in depth help including examples, see online documentation at:
			https://docs.github.com/en/actions/guides/managing-github-actions-with-github-cli
		`, header, runHeader, workflowHeader)
}

func actionsRun(opts ActionsOptions) {
	cs := opts.IO.ColorScheme()
	fmt.Fprintln(opts.IO.Out, actionsExplainer(cs, true))
}
