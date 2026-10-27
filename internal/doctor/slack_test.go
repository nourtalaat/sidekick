package doctor

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// slackConfig adds the slack section and one slack output at SLACK_URL.
const slackConfig = baseConfig + `
slack:
  botTokenSecret: { name: sidekick-slack, key: bot }
outputs:
  thread: { type: slack, channel: "#alerts", apiURL: "SLACK_URL" }
`

func TestCheckSlack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    string
		noToken   bool
		responses []fakehttp.Response
		// want are the results, with SLACK_URL standing for the fake
		// API's address.
		want []Result
	}{
		{
			name:   "no slack section",
			config: baseConfig,
			want:   []Result{{Check: "slack", Status: StatusSkip, Message: "no slack section"}},
		},
		{
			name:      "the token works",
			config:    slackConfig,
			responses: []fakehttp.Response{{Body: []byte(`{"ok":true,"team":"Acme","user":"sidekick"}`)}},
			want:      []Result{{Check: "slack SLACK_URL", Status: StatusOK, Message: "the bot token works: it is the bot user sidekick in the workspace Acme"}},
		},
		{
			name:      "Slack rejects the token",
			config:    slackConfig,
			responses: []fakehttp.Response{{Body: []byte(`{"ok":false,"error":"invalid_auth"}`)}},
			want: []Result{{
				Check: "slack SLACK_URL", Status: StatusFail,
				Message: "Slack rejected the bot token: invalid_auth",
				Fix:     "put the app's Bot User OAuth Token (xoxb-...), from the Slack app's OAuth & Permissions page, in the Secret sidekick-slack, key bot, and reinstall the app if it was removed",
			}},
		},
		{
			name:      "another Slack error",
			config:    slackConfig,
			responses: []fakehttp.Response{{Body: []byte(`{"ok":false,"error":"team_access_not_granted"}`)}},
			want: []Result{{
				Check: "slack SLACK_URL", Status: StatusFail,
				Message: "Slack answered team_access_not_granted",
				Fix:     "look the error code up at https://docs.slack.dev/reference/methods/auth.test",
			}},
		},
		{
			name:      "Slack cannot be reached",
			config:    slackConfig,
			responses: []fakehttp.Response{{Status: http.StatusBadGateway}},
			want: []Result{{
				Check: "slack SLACK_URL", Status: StatusFail,
				Message: "cannot reach Slack: check the bot token: call auth.test: HTTP status 502",
				Fix:     "check that Sidekick's pod can reach SLACK_URL over HTTPS, through any network policy or proxy",
			}},
		},
		{
			name:    "the token secret is missing",
			config:  slackConfig,
			noToken: true,
			want: []Result{{
				Check: "slack", Status: StatusFail,
				Message: "slack.botTokenSecret: cannot read secret sidekick-slack/bot: the file does not exist",
				Fix:     "create the Kubernetes Secret sidekick-slack with the key bot, and list sidekick-slack in the chart's secrets value so it is mounted",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if len(tt.responses) > 0 {
				api.Handle(http.MethodPost, "/api/auth.test", tt.responses...)
			}

			api.ExpectAllCalled()

			slackURL := api.URL() + "/api/"

			files := maps.Clone(defaultSecrets)
			if tt.noToken {
				delete(files, "sidekick-slack/bot")
			}

			cfg := parseConfig(t, tt.config, files, config.Registries{}, "SLACK_URL", slackURL)

			got := newDoctor(Options{Config: cfg, HTTP: api.Client()}).checkSlack(t.Context())

			want := make([]Result, len(tt.want))
			for i, r := range tt.want {
				r.Check = strings.ReplaceAll(r.Check, "SLACK_URL", slackURL)
				r.Fix = strings.ReplaceAll(r.Fix, "SLACK_URL", slackURL)
				want[i] = r
			}

			require.Equal(t, want, got)

			for _, req := range api.Requests() {
				require.Equal(t, "Bearer "+testBotToken, req.Header.Get("Authorization"), "the token is sent without its trailing newline")
			}
		})
	}
}

func TestCheckSlackOncePerAPIURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outputs string
		want    []string
	}{
		{
			name:    "two outputs on one address",
			outputs: "  a: { type: slack, channel: \"#a\", apiURL: \"SLACK_URL\" }\n  b: { type: slack, channel: \"#b\", apiURL: \"SLACK_URL\" }\n",
			want:    []string{"slack SLACK_URL"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, "/api/auth.test", fakehttp.Response{Body: []byte(`{"ok":true,"team":"Acme","user":"sidekick"}`)})
			api.ExpectAllCalled()

			slackURL := api.URL() + "/api/"
			content := baseConfig + "slack:\n  botTokenSecret: { name: sidekick-slack, key: bot }\noutputs:\n" + tt.outputs

			cfg := parseConfig(t, content, defaultSecrets, config.Registries{}, "SLACK_URL", slackURL)

			got := newDoctor(Options{Config: cfg, HTTP: api.Client()}).checkSlack(t.Context())

			want := make([]string, len(tt.want))
			for i, c := range tt.want {
				want[i] = strings.ReplaceAll(c, "SLACK_URL", slackURL)
			}

			require.Equal(t, want, checks(got))
		})
	}
}
