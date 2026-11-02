package githubpr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
)

// classify wraps an error from the GitHub API with what was being done. A
// refusal that a repeat cannot change, such as a missing repository, a
// token without the right, or a request GitHub finds invalid, also wraps
// action.ErrPermanent. Rate limits, server errors and network failures do
// not, so Sidekick tries again later.
func classify(doing string, err error) error {
	if _, ok := errors.AsType[*gh.RateLimitError](err); ok {
		return fmt.Errorf("%s: %w", doing, err)
	}

	if _, ok := errors.AsType[*gh.AbuseRateLimitError](err); ok {
		return fmt.Errorf("%s: %w", doing, err)
	}

	if resp, ok := errors.AsType[*gh.ErrorResponse](err); ok && resp.Response != nil {
		switch resp.Response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusUnprocessableEntity:
			return fmt.Errorf("%s: %w: %w", doing, action.ErrPermanent, err)
		}
	}

	return fmt.Errorf("%s: %w", doing, err)
}

// isStatus reports whether err is a GitHub error response with status.
func isStatus(err error, status int) bool {
	resp, ok := errors.AsType[*gh.ErrorResponse](err)

	return ok && resp.Response != nil && resp.Response.StatusCode == status
}

// alreadyExists reports whether err is GitHub's 422 for something that
// exists already: a reference or a pull request.
func alreadyExists(err error) bool {
	resp, ok := errors.AsType[*gh.ErrorResponse](err)
	if !ok || resp.Response == nil || resp.Response.StatusCode != http.StatusUnprocessableEntity {
		return false
	}

	if strings.Contains(strings.ToLower(resp.Message), "already exists") {
		return true
	}

	for _, e := range resp.Errors {
		if strings.Contains(strings.ToLower(e.Message), "already exists") {
			return true
		}
	}

	return false
}
