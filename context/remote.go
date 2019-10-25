package context

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/github/gh-cli/git"
)

const defaultHostname = "github.com"

// Remotes represents a set of git remotes
type Remotes []*Remote

// FindByName returns the first Remote whose name matches the list
func (r Remotes) FindByName(names ...string) (*Remote, error) {
	for _, name := range names {
		for _, rem := range r {
			if rem.Name == name || name == "*" {
				return rem, nil
			}
		}
	}
	return nil, fmt.Errorf("no GitHub remotes found")
}

// Remote represents a git remote mapped to a GitHub repository
type Remote struct {
	*git.Remote
	Owner string
	Repo  string
}

// GitHubRepository represents a GitHub respository
type GitHubRepository struct {
	Name  string
	Owner string
}

// TODO: accept an interface instead of git.RemoteSet
func translateRemotes(gitRemotes git.RemoteSet, urlTranslate func(*url.URL) *url.URL) (remotes Remotes) {
	for _, r := range gitRemotes {
		var owner string
		var repo string
		if r.FetchURL != nil {
			owner, repo, _ = repoFromURL(urlTranslate(r.FetchURL))
		}
		if r.PushURL != nil && owner == "" {
			owner, repo, _ = repoFromURL(urlTranslate(r.PushURL))
		}
		remotes = append(remotes, &Remote{
			Remote: r,
			Owner:  owner,
			Repo:   repo,
		})
	}
	return
}

func repoFromURL(u *url.URL) (string, string, error) {
	if !strings.EqualFold(u.Hostname(), defaultHostname) {
		return "", "", fmt.Errorf("unsupported hostname: %s", u.Hostname())
	}
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 3)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid path: %s", u.Path)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}
