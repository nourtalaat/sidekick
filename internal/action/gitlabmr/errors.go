package gitlabmr

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// classify wraps an error from the GitLab API with what was being done. A
// refusal that a repeat cannot change, such as a missing project, a token
// without the right, a request GitLab finds invalid, or a redirect that
// writes never follow, also wraps action.ErrPermanent. Rate limits, server
// errors and network failures do not, so Sidekick tries again later.
func classify(doing string, err error) error {
	if se, ok := errors.AsType[*gitlabapi.StatusError](err); ok && refusal(se.Code) {
		return fmt.Errorf("%s: %w: %w", doing, action.ErrPermanent, err)
	}

	return fmt.Errorf("%s: %w", doing, err)
}

// refusal reports whether a status is an answer that repeating the request
// does not change. The Commits API answers every failure with 400, and a
// duplicate merge request with 409.
func refusal(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusMethodNotAllowed, http.StatusConflict, http.StatusGone, http.StatusUnprocessableEntity:
		return true
	default:
		return code >= http.StatusMultipleChoices && code < http.StatusBadRequest
	}
}
