package doctor

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// rejectedTokenCodes are the Slack error codes for a bot token that is
// wrong, revoked or belongs to a disabled app.
var rejectedTokenCodes = []string{"invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive", "not_allowed_token_type"}

// checkSlack checks the bot token with auth.test, once for each Slack API
// address the slack outputs use.
func (d *doctor) checkSlack(ctx context.Context) []Result {
	const name = "slack"

	// Config validation already rejects slack outputs without a slack
	// section.
	if d.cfg.Slack == nil {
		return []Result{{Check: name, Status: StatusSkip, Message: "no slack section"}}
	}

	apiURLs, err := d.slackAPIURLs()
	if err != nil {
		return []Result{{Check: name, Status: StatusFail, Message: err.Error(), Fix: "run `sidekick config validate` and fix the slack outputs"}}
	}

	ref := d.cfg.Slack.BotTokenSecret

	token, err := secrets.ResolveText(ctx, d.secrets, ref)
	if err != nil {
		return []Result{{Check: name, Status: StatusFail, Message: secretProblem("slack.botTokenSecret", ref, err), Fix: secretFix(ref)}}
	}

	if len(apiURLs) == 0 {
		apiURLs = []string{slack.DefaultAPIURL}
	}

	results := make([]Result, 0, len(apiURLs))

	for _, apiURL := range apiURLs {
		check := name
		if apiURL != slack.DefaultAPIURL {
			check = name + " " + apiURL
		}

		results = append(results, d.authTest(ctx, check, apiURL, token))
	}

	return results
}

// authTest runs auth.test at apiURL.
func (d *doctor) authTest(ctx context.Context, check, apiURL, token string) Result {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	id, err := slack.AuthTest(ctx, apiURL, d.http, token)
	if err == nil {
		return Result{Check: check, Status: StatusOK, Message: fmt.Sprintf("the bot token works: it is the bot user %s in the workspace %s", id.User, id.Team)}
	}

	ref := d.cfg.Slack.BotTokenSecret

	code, isSlack := slack.ErrorCode(err)
	if isSlack && slices.Contains(rejectedTokenCodes, code) {
		return Result{
			Check: check, Status: StatusFail,
			Message: "Slack rejected the bot token: " + code,
			Fix:     fmt.Sprintf("put the app's Bot User OAuth Token (xoxb-...), from the Slack app's OAuth & Permissions page, in the Secret %s, key %s, and reinstall the app if it was removed", ref.Name, ref.Key),
		}
	}

	if isSlack {
		return Result{Check: check, Status: StatusFail, Message: "Slack answered " + code, Fix: "look the error code up at https://docs.slack.dev/reference/methods/auth.test"}
	}

	return Result{Check: check, Status: StatusFail, Message: "cannot reach Slack: " + err.Error(), Fix: "check that Sidekick's pod can reach " + apiURL + " over HTTPS, through any network policy or proxy"}
}

// slackAPIURLs returns the distinct Slack API addresses of the slack
// outputs, sorted.
func (d *doctor) slackAPIURLs() ([]string, error) {
	var urls []string

	for _, name := range slices.Sorted(maps.Keys(d.cfg.Outputs)) {
		section := d.cfg.Outputs[name]
		if section.Type != slack.Type {
			continue
		}

		var c slack.Config
		if err := section.Decode(&c); err != nil {
			return nil, fmt.Errorf("read output %s: %w", name, err)
		}

		if u := cmp.Or(c.APIURL, slack.DefaultAPIURL); !slices.Contains(urls, u) {
			urls = append(urls, u)
		}
	}

	slices.Sort(urls)

	return urls, nil
}
