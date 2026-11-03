package gitlabapi

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// DefaultBaseURL is the root of gitlab.com.
const DefaultBaseURL = "https://gitlab.com"

// apiSuffix is the path of the REST API under the root of a GitLab.
const apiSuffix = "/api/v4"

// projectPattern is the shape of a project's full path, group/project or
// group/subgroup/project: two or more parts of the characters GitLab allows
// in a path.
var projectPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)

// CheckBaseURL checks that the base URL of a config, when set, is an absolute
// https URL for the root of a GitLab, so the token never crosses the network
// unencrypted. Plain http is allowed only for a loopback host, such as a
// local test server. The error starts with the key baseURL.
func CheckBaseURL(raw string) error {
	if raw == "" {
		return nil
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("baseURL: %q must be an absolute https URL, such as https://gitlab.example.com", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	case strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), apiSuffix):
		return fmt.Errorf("baseURL: %q must be the root of the GitLab, without %s", raw, apiSuffix)
	default:
		return nil
	}
}

// BaseURL returns the root of the GitLab for the base URL of a config,
// without a trailing slash, or DefaultBaseURL when it is empty.
func BaseURL(raw string) string {
	if raw == "" {
		return DefaultBaseURL
	}

	return strings.TrimSuffix(raw, "/")
}

// ValidProject reports whether s is a project's full path, group/project or
// group/subgroup/project. No part may be "." or "..", which would change the
// meaning of an API path.
func ValidProject(s string) bool {
	if !projectPattern.MatchString(s) {
		return false
	}

	for part := range strings.SplitSeq(s, "/") {
		if part == "." || part == ".." {
			return false
		}
	}

	return true
}
