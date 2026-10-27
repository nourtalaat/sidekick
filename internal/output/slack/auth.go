package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// methodAuthTest is the Web API method that says who a token belongs to.
const methodAuthTest = "auth.test"

// Identity is who a bot token belongs to, as auth.test reports it.
type Identity struct {
	// Team is the workspace name.
	Team string `json:"team"`
	// User is the bot user's name.
	User string `json:"user"`
}

// AuthTest calls auth.test at apiURL, which ends in "/", with token, to
// check the token without posting anything. hc nil means the default
// Sidekick HTTP client. When Slack rejects the token, ErrorCode returns
// its error code, such as invalid_auth, from the error.
func AuthTest(ctx context.Context, apiURL string, hc *http.Client, token string) (Identity, error) {
	if hc == nil {
		hc = httpclient.New()
	}

	c := newClient(apiURL, hc, func(context.Context) (string, error) { return token, nil }, nil)

	var id Identity
	if err := c.call(ctx, methodAuthTest, "", jsonRequest(struct{}{}), &id); err != nil {
		return Identity{}, fmt.Errorf("check the bot token: %w", err)
	}

	return id, nil
}

// ErrorCode returns the Slack error code in err, such as invalid_auth, and
// false when err is not an error answer from Slack.
func ErrorCode(err error) (string, bool) {
	apiErr, ok := errors.AsType[*apiError](err)
	if !ok {
		return "", false
	}

	return apiErr.Code, true
}
