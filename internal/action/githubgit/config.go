package githubgit

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// defaultBaseURL is the REST API root of github.com.
const defaultBaseURL = "https://api.github.com/"

// repoPattern is the shape of a repository name, owner/name.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// CheckBaseURL checks that the base URL, when set, is an absolute https
// URL, so the installation token never crosses the network unencrypted.
// Plain http is allowed only for a loopback host.
func CheckBaseURL(raw string) error {
	if raw == "" {
		return nil
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return fmt.Errorf("baseURL: %q must be an absolute https URL", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}

// BaseURL returns the REST API root for a configured base URL, ending in a
// slash. An empty one means github.com.
func BaseURL(raw string) string {
	switch {
	case raw == "":
		return defaultBaseURL
	case strings.HasSuffix(raw, "/"):
		return raw
	default:
		return raw + "/"
	}
}

// CheckRepos checks that there is at least one repository and that each is a
// plain owner/name. Each error starts with the key limits.repos.
func CheckRepos(repos []string) []error {
	if len(repos) == 0 {
		return []error{errors.New("limits.repos: list at least one repository, as owner/name")}
	}

	var errs []error

	for i, repo := range repos {
		if !ValidRepo(repo) {
			errs = append(errs, fmt.Errorf("limits.repos[%d]: %q must have the form owner/name, with no wildcards", i, repo))
		}
	}

	return errs
}

// AllowsRepo reports whether repo is one of the repositories. GitHub names
// are not case-sensitive, so neither is the match.
func AllowsRepo(repos []string, repo string) bool {
	return slices.ContainsFunc(repos, func(r string) bool { return strings.EqualFold(r, repo) })
}

// ValidRepo reports whether s is a repository name, owner/name. Neither part
// may be "." or "..", which GitHub does not allow and which would change the
// meaning of an API path.
func ValidRepo(s string) bool {
	owner, name, _ := strings.Cut(s, "/")

	return repoPattern.MatchString(s) && !dots(owner) && !dots(name)
}

// dots reports whether s is "." or "..".
func dots(s string) bool {
	return s == "." || s == ".."
}
