package command

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/github/gh-cli/api"
	"github.com/github/gh-cli/context"
	"github.com/github/gh-cli/utils"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(issueCmd)
	issueCmd.AddCommand(issueStatusCmd)
	issueCmd.AddCommand(issueViewCmd)

	issueCmd.AddCommand(issueCreateCmd)
	issueCreateCmd.Flags().StringP("title", "t", "",
		"Supply a title. Will prompt for one otherwise.")
	issueCreateCmd.Flags().StringP("body", "b", "",
		"Supply a body. Will prompt for one otherwise.")
	issueCreateCmd.Flags().BoolP("web", "w", false, "Open the web browser to create an issue")

	issueCmd.AddCommand(issueListCmd)
	issueListCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	issueListCmd.Flags().StringSliceP("label", "l", nil, "Filter by label")
	issueListCmd.Flags().StringP("state", "s", "", "Filter by state: {open|closed|all}")
	issueListCmd.Flags().IntP("limit", "L", 30, "Maximum number of issues to fetch")
}

var issueCmd = &cobra.Command{
	Use:   "issue",
	Short: "Create and view issues",
	Long: `Work with GitHub issues.

An issue can be supplied as argument in any of the following formats:
- by number, e.g. "123"; or
- by URL, e.g. "https://github.com/<owner>/<repo>/issues/123".`,
}
var issueCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new issue",
	RunE:  issueCreate,
}
var issueListCmd = &cobra.Command{
	Use:   "list",
	Short: "List and filter issues in this repository",
	RunE:  issueList,
}
var issueStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of relevant issues",
	RunE:  issueStatus,
}
var issueViewCmd = &cobra.Command{
	Use:   "view {<number> | <url> | <branch>}",
	Args:  cobra.MinimumNArgs(1),
	Short: "View an issue in the browser",
	RunE:  issueView,
}

func issueList(cmd *cobra.Command, args []string) error {
	ctx := contextForCommand(cmd)
	apiClient, err := apiClientForContext(ctx)
	if err != nil {
		return err
	}

	baseRepo, err := ctx.BaseRepo()
	if err != nil {
		return err
	}

	state, err := cmd.Flags().GetString("state")
	if err != nil {
		return err
	}

	labels, err := cmd.Flags().GetStringSlice("label")
	if err != nil {
		return err
	}

	assignee, err := cmd.Flags().GetString("assignee")
	if err != nil {
		return err
	}

	limit, err := cmd.Flags().GetInt("limit")
	if err != nil {
		return err
	}

	issues, err := api.IssueList(apiClient, baseRepo, state, labels, assignee, limit)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	colorOut := colorableOut(cmd)

	if len(issues) == 0 {
		printMessage(colorOut, "There are no open issues")
		return nil
	}

	table := utils.NewTablePrinter(out)
	for _, issue := range issues {
		issueNum := strconv.Itoa(issue.Number)
		if table.IsTTY() {
			issueNum = "#" + issueNum
		}
		labels := labelList(issue)
		if labels != "" && table.IsTTY() {
			labels = fmt.Sprintf("(%s)", labels)
		}
		table.AddField(issueNum, nil, colorFuncForState(issue.State))
		table.AddField(replaceExcessiveWhitespace(issue.Title), nil, nil)
		table.AddField(labels, nil, utils.Gray)
		table.EndRow()
	}
	table.Render()

	return nil
}

func issueStatus(cmd *cobra.Command, args []string) error {
	ctx := contextForCommand(cmd)
	apiClient, err := apiClientForContext(ctx)
	if err != nil {
		return err
	}

	baseRepo, err := ctx.BaseRepo()
	if err != nil {
		return err
	}

	currentUser, err := ctx.AuthLogin()
	if err != nil {
		return err
	}

	issuePayload, err := api.IssueStatus(apiClient, baseRepo, currentUser)
	if err != nil {
		return err
	}

	out := colorableOut(cmd)

	printHeader(out, "Issues assigned to you")
	if issuePayload.Assigned != nil {
		printIssues(out, "  ", issuePayload.Assigned...)
	} else {
		message := fmt.Sprintf("  There are no issues assgined to you")
		printMessage(out, message)
	}
	fmt.Fprintln(out)

	printHeader(out, "Issues mentioning you")
	if len(issuePayload.Mentioned) > 0 {
		printIssues(out, "  ", issuePayload.Mentioned...)
	} else {
		printMessage(out, "  There are no issues mentioning you")
	}
	fmt.Fprintln(out)

	printHeader(out, "Issues opened by you")
	if len(issuePayload.Authored) > 0 {
		printIssues(out, "  ", issuePayload.Authored...)
	} else {
		printMessage(out, "  There are no issues opened by you")
	}
	fmt.Fprintln(out)

	return nil
}

func issueView(cmd *cobra.Command, args []string) error {
	ctx := contextForCommand(cmd)

	baseRepo, err := ctx.BaseRepo()
	if err != nil {
		return err
	}
	apiClient, err := apiClientForContext(ctx)
	if err != nil {
		return err
	}

	issue, err := issueFromArg(apiClient, baseRepo, args[0])
	if err != nil {
		return err
	}
	openURL := issue.URL

	cmd.Printf("Opening %s in your browser.\n", openURL)
	return utils.OpenInBrowser(openURL)
}

var issueURLRE = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/issues/(\d+)`)

func issueFromArg(apiClient *api.Client, baseRepo context.GitHubRepository, arg string) (*api.Issue, error) {
	if issueNumber, err := strconv.Atoi(arg); err == nil {
		return api.IssueByNumber(apiClient, baseRepo, issueNumber)
	}

	if m := issueURLRE.FindStringSubmatch(arg); m != nil {
		issueNumber, _ := strconv.Atoi(m[3])
		return api.IssueByNumber(apiClient, baseRepo, issueNumber)
	}

	return nil, fmt.Errorf("invalid issue format: %q", arg)
}

func issueCreate(cmd *cobra.Command, args []string) error {
	ctx := contextForCommand(cmd)

	baseRepo, err := ctx.BaseRepo()
	if err != nil {
		return err
	}

	if isWeb, err := cmd.Flags().GetBool("web"); err == nil && isWeb {
		// TODO: move URL generation into GitHubRepository
		openURL := fmt.Sprintf("https://github.com/%s/%s/issues/new", baseRepo.RepoOwner(), baseRepo.RepoName())
		// TODO: figure out how to stub this in tests
		if stat, err := os.Stat(".github/ISSUE_TEMPLATE"); err == nil && stat.IsDir() {
			openURL += "/choose"
		}
		return utils.OpenInBrowser(openURL)
	}

	apiClient, err := apiClientForContext(ctx)
	if err != nil {
		return err
	}

	title, err := cmd.Flags().GetString("title")
	if err != nil {
		return errors.Wrap(err, "could not parse title")
	}
	body, err := cmd.Flags().GetString("body")
	if err != nil {
		return errors.Wrap(err, "could not parse body")
	}

	interactive := title == "" || body == ""

	if interactive {
		tb, err := titleBodySurvey(cmd, title, body)
		if err != nil {
			return errors.Wrap(err, "could not collect title and/or body")
		}

		if tb == nil {
			// editing was canceled, we can just leave
			return nil
		}

		if title == "" {
			title = tb.Title
		}
		if body == "" {
			body = tb.Body
		}
	}
	params := map[string]interface{}{
		"title": title,
		"body":  body,
	}

	newIssue, err := api.IssueCreate(apiClient, baseRepo, params)
	if err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), newIssue.URL)
	return nil
}

func printIssues(w io.Writer, prefix string, issues ...api.Issue) {
	for _, issue := range issues {
		number := utils.Green("#" + strconv.Itoa(issue.Number))
		coloredLabels := labelList(issue)
		if coloredLabels != "" {
			coloredLabels = utils.Gray(fmt.Sprintf("  (%s)", coloredLabels))
		}
		fmt.Fprintf(w, "%s%s %s%s\n", prefix, number, truncate(70, replaceExcessiveWhitespace(issue.Title)), coloredLabels)
	}
}

func labelList(issue api.Issue) string {
	if len(issue.Labels.Nodes) == 0 {
		return ""
	}

	labelNames := []string{}
	for _, label := range issue.Labels.Nodes {
		labelNames = append(labelNames, label.Name)
	}

	list := strings.Join(labelNames, ", ")
	if issue.Labels.TotalCount > len(issue.Labels.Nodes) {
		list += ", …"
	}
	return list
}
