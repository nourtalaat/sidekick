package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/intake/alertmanager"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
)

// fakeSecret is the value writeConfig puts in every extra secret file,
// without its trailing newline.
const fakeSecret = "fake-value"

// accepted is what Sidekick answers a delivery it accepts.
var accepted = fakehttp.Response{Status: http.StatusAccepted, Body: []byte(`{"duplicate":false,"events":[{"id":"0199e0d4","status":"new","duplicate":false}]}`)}

func TestTestAlertCommand(t *testing.T) {
	t.Parallel()

	bearerHook := "    am: { adapter: alertmanager, auth: { bearer: { name: hooks, key: am } } }\n" //nolint:gosec // A secret reference, not a secret.
	hmacHook := "    am: { adapter: alertmanager, auth: { hmac: { name: hooks, key: am } } }\n"
	genericHook := "    ci: { adapter: generic, auth: { bearer: { name: hooks, key: ci } }, fields: { title: $.title } }\n"

	tests := []struct {
		name     string
		webhooks string
		args     []string
		noSecret bool
		response *fakehttp.Response
		// wantAuth checks the delivery as the endpoint would.
		wantAuth intake.Authenticator
		wantOut  []string
		wantErr  string
	}{
		{
			name:     "bearer token",
			webhooks: bearerHook,
			response: &accepted,
			wantAuth: intake.Bearer{Token: []byte(fakeSecret)},
			wantOut:  []string{"Sending a test alert to URL/hooks/am", "Sidekick answered 202 Accepted: " + string(accepted.Body), "The route that matches the alert"},
		},
		{
			name:     "HMAC signature",
			webhooks: hmacHook,
			response: &accepted,
			wantAuth: intake.HMAC{Secret: []byte(fakeSecret), Tolerance: time.Minute},
			wantOut:  []string{"Sidekick answered 202 Accepted"},
		},
		{
			name:     "the endpoint named with --webhook",
			webhooks: bearerHook + strings.Replace(bearerHook, "am:", "am2:", 1),
			args:     []string{"--webhook", "am2"},
			response: &accepted,
			wantAuth: intake.Bearer{Token: []byte(fakeSecret)},
			wantOut:  []string{"Sending a test alert to URL/hooks/am2"},
		},
		{
			name:     "Sidekick rejects the delivery",
			webhooks: bearerHook,
			response: &fakehttp.Response{Status: http.StatusUnauthorized, Body: []byte(`{"error":"unauthenticated"}`)},
			wantAuth: intake.Bearer{Token: []byte(fakeSecret)},
			wantOut:  []string{`Sidekick answered 401 Unauthorized: {"error":"unauthenticated"}`},
			wantErr:  "the test alert was not accepted: HTTP status 401",
		},
		{
			name:     "no alertmanager endpoint",
			webhooks: genericHook,
			wantErr:  "choose the webhook endpoint: " + errNoAlertmanagerWebhook.Error(),
		},
		{
			name:     "several alertmanager endpoints",
			webhooks: bearerHook + strings.Replace(bearerHook, "am:", "am2:", 1),
			wantErr:  "choose the webhook endpoint: several endpoints use the alertmanager adapter (am, am2); name one with --webhook",
		},
		{
			name:     "a named endpoint with another adapter",
			webhooks: genericHook,
			args:     []string{"--webhook", "ci"},
			wantErr:  `choose the webhook endpoint: endpoint "ci" uses the generic adapter: ` + errNotAlertmanager.Error(),
		},
		{
			name:     "a named endpoint that does not exist",
			webhooks: genericHook,
			args:     []string{"--webhook", "nope"},
			wantErr:  `choose the webhook endpoint: intake.webhooks has no endpoint "nope"; it has ci`,
		},
		{
			name:     "the token secret is missing",
			webhooks: bearerHook,
			noSecret: true,
			wantErr:  "authenticate as webhook am: bearer token: read auth.bearer, the Secret hooks with key am: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if tt.response != nil {
				path := "/hooks/am"
				if len(tt.args) > 1 {
					path = "/hooks/" + tt.args[1]
				}

				api.Handle(http.MethodPost, path, *tt.response)
			}

			api.ExpectAllCalled()

			var extra []string
			if !tt.noSecret {
				extra = []string{"hooks/am", "hooks/ci"}
			}

			configPath := writeConfig(t, validConfig+"intake:\n  webhooks:\n"+tt.webhooks, "", extra...)

			var out, stderr bytes.Buffer

			root := newTestRootCmd(newTestAlertCmd(fakeRegistries()))
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(append([]string{"test-alert", "-c", configPath, "--url", api.URL() + "/"}, tt.args...))

			err := root.Execute()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.Empty(t, stderr.String())
			}

			for _, want := range tt.wantOut {
				require.Contains(t, out.String(), strings.ReplaceAll(want, "URL", api.URL()))
			}

			require.NotContains(t, out.String(), fakeSecret, "the token is never printed")

			if tt.wantAuth != nil {
				checkTestAlert(t, api.Requests(), tt.wantAuth)
			}
		})
	}
}

// slackAnswer is what Slack answers chat.postMessage for the sample alert.
var slackAnswer = fakehttp.Response{Body: []byte(`{"ok": true, "channel": "C01ALERTS", "ts": "1761472980.000700", "message": {
	"type": "message", "subtype": "bot_message", "bot_id": "B09SIDEKICK", "app_id": "A01SIDEKICK",
	"bot_profile": {"id": "B09SIDEKICK", "app_id": "A01SIDEKICK", "name": "Sidekick"},
	"text": "Test alert", "blocks": [{"type": "section", "block_id": "` + slackout.TestAlertBlockID + `", "text": {"type": "mrkdwn", "text": "Test alert"}}]}}`)}

// slackSection is the slack section and listeners of the test configs, with
// API_URL to replace.
const slackSection = `slack:
  botTokenSecret: { name: slack, key: botToken }
  appTokenSecret: { name: slack, key: appToken }
  apiURL: API_URL
`

// botListener is a listener on #alerts that takes every bot message.
const botListener = `listeners:
  alerts:
    channel: "#alerts"
    match: { sender: { kind: bot } }
`

func TestTestAlertChannel(t *testing.T) {
	t.Parallel()

	found := func(context.Context, *config.Config, config.Registries) (string, string, bool, error) {
		return "shop", "api-5d8c9b7f4-m2xkq", true, nil
	}
	none := func(context.Context, *config.Config, config.Registries) (string, string, bool, error) {
		return "", "", false, nil
	}
	failing := func(context.Context, *config.Config, config.Registries) (string, string, bool, error) {
		return "", "", false, errors.New("list pods: the API server cannot be reached")
	}

	tests := []struct {
		name      string
		find      samplePodFinder
		config    string
		response  fakehttp.Response
		noSecret  bool
		wantText  []string
		wantOut   []string
		wantNoOut []string
		wantErr   string
	}{
		{
			name:      "an alert about a pod of the cluster, taken by a listener",
			find:      found,
			config:    slackSection + botListener,
			response:  slackAnswer,
			wantText:  []string{"shop/api-5d8c9b7f4-m2xkq", "This pod runs in the cluster"},
			wantOut:   []string{"Posted a test alert in #alerts, about the pod shop/api-5d8c9b7f4-m2xkq.", "The listener alerts takes it."},
			wantNoOut: []string{"made up"},
		},
		{
			name:     "a made-up pod when the cluster has none to name",
			find:     none,
			config:   slackSection + botListener,
			response: slackAnswer,
			wantText: []string{"default/checkout-7d9f8b6c5-x2k4p", "The pod name is made up"},
			wantOut:  []string{"about the pod default/checkout-7d9f8b6c5-x2k4p.", "The pod name is made up", "The listener alerts takes it."},
		},
		{
			name:     "a made-up pod when the source fails, with the reason",
			find:     failing,
			config:   slackSection + botListener,
			response: slackAnswer,
			wantText: []string{"The pod name is made up"},
			wantOut:  []string{"No pod could be read from the cluster: list pods: the API server cannot be reached", "The listener alerts takes it."},
		},
		{
			name:     "no listener takes the alert",
			find:     none,
			config:   slackSection + "listeners:\n  other:\n    channel: \"#ops\"\n",
			response: slackAnswer,
			wantOut:  []string{"No listener in the config takes this alert, so Sidekick will not investigate it"},
		},
		{
			name:     "a listener for other bots does not take it",
			find:     none,
			config:   slackSection + "listeners:\n  alerts:\n    channel: \"#alerts\"\n    match: { sender: { names: [SigNoz] } }\n",
			response: slackAnswer,
			wantOut:  []string{"No listener in the config takes this alert"},
		},
		{
			name:     "Slack says the bot is not in the channel",
			find:     none,
			config:   slackSection + botListener,
			response: fakehttp.Response{Body: []byte(`{"ok": false, "error": "not_in_channel"}`)},
			wantErr:  "post the test alert to #alerts: the test alert was not accepted by Slack: not_in_channel (invite the Sidekick app to the channel",
		},
		{
			name:    "the config has no slack section",
			find:    none,
			config:  "",
			wantErr: errNoSlackSection.Error(),
		},
		{
			name:     "the bot token secret is missing",
			find:     none,
			config:   slackSection + botListener,
			noSecret: true,
			wantErr:  "read slack.botTokenSecret, the Secret slack with key botToken",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if tt.response.Body != nil {
				api.Handle(http.MethodPost, "/chat.postMessage", tt.response)
			}

			api.ExpectAllCalled()

			var extra []string
			if !tt.noSecret {
				extra = []string{"slack/botToken", "slack/appToken"}
			}

			configPath := writeConfig(t, validConfig+strings.ReplaceAll(tt.config, "API_URL", api.URL()+"/"), "", extra...)

			cfg, err := config.Load(t.Context(), configPath, config.LoadOptions{Registries: fakeRegistries()})
			require.NoError(t, err)

			var out bytes.Buffer

			err = sendSlackTestAlert(t.Context(), &out, cfg, fakeRegistries(), "#alerts", api.Client(), tt.find)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			for _, want := range tt.wantOut {
				require.Contains(t, out.String(), want)
			}

			for _, unwanted := range tt.wantNoOut {
				require.NotContains(t, out.String(), unwanted)
			}

			require.NotContains(t, out.String(), fakeSecret, "the token is never printed")

			if tt.response.Body != nil {
				checkSlackPost(t, api.Requests(), tt.wantText)
			}
		})
	}
}

// checkSlackPost checks that the one request is a chat.postMessage as the
// bot, into #alerts, whose one block carries the marker and whose text has
// every part.
func checkSlackPost(t *testing.T, sent []fakehttp.Request, wantText []string) {
	t.Helper()

	require.Len(t, sent, 1)
	require.Equal(t, "Bearer "+fakeSecret, sent[0].Header.Get("Authorization"))

	var body struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
		Blocks  []struct {
			Type    string `json:"type"`
			BlockID string `json:"block_id"`
			Text    struct {
				Text string `json:"text"`
			} `json:"text"`
		} `json:"blocks"`
		LinkNames bool `json:"link_names"`
	}

	require.NoError(t, json.Unmarshal(sent[0].Body, &body))
	require.Equal(t, "#alerts", body.Channel)
	require.False(t, body.LinkNames)
	require.Len(t, body.Blocks, 1)
	require.Equal(t, slackout.TestAlertBlockID, body.Blocks[0].BlockID)
	require.Equal(t, body.Text, body.Blocks[0].Text.Text)

	for _, part := range wantText {
		require.Contains(t, body.Text, part)
	}
}

func TestTestAlertChannelRefusesWebhookFlags(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--webhook", "--url"} {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()

			configPath := writeConfig(t, validConfig, "")

			root := newTestRootCmd(newTestAlertCmd(fakeRegistries()))
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{"test-alert", "-c", configPath, "--channel", "#alerts", flag, "x"})

			require.ErrorIs(t, root.Execute(), errChannelWithWebhook)
		})
	}
}

// checkTestAlert checks that the one delivery passes auth and holds one
// firing test alert, as the alertmanager adapter reads it.
func checkTestAlert(t *testing.T, sent []fakehttp.Request, auth intake.Authenticator) {
	t.Helper()

	require.Len(t, sent, 1)

	req := intake.Request{Header: sent[0].Header, Body: sent[0].Body, ReceivedAt: time.Now()}
	require.NoError(t, auth.Authenticate(req))

	events, err := alertmanager.Alertmanager{}.Parse(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, event.KindAlert, events[0].Kind)
	require.Equal(t, testAlertName, events[0].AlertName)
	require.Equal(t, "Test alert from sidekick test-alert", events[0].Title)
	require.Equal(t, "sidekick", events[0].Service)
}

func TestSamplePayloadIsNewEachTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "two payloads sent at the same time have different keys"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
			keys := map[string]bool{}

			for range 2 {
				body, err := samplePayload(now)
				require.NoError(t, err)

				events, err := alertmanager.Alertmanager{}.Parse(t.Context(), intake.Request{Body: body, ReceivedAt: now})
				require.NoError(t, err)

				keys[events[0].Key] = true
			}

			require.Len(t, keys, 2, tt.name)
		})
	}
}

func TestLocalURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		listen  string
		want    string
		wantErr bool
	}{
		{listen: ":8080", want: "http://127.0.0.1:8080"},
		{listen: "0.0.0.0:8080", want: "http://127.0.0.1:8080"},
		{listen: "[::]:8080", want: "http://127.0.0.1:8080"},
		{listen: "10.0.0.5:9000", want: "http://10.0.0.5:9000"},
		{listen: "[fd00::5]:9000", want: "http://[fd00::5]:9000"},
		{listen: "8080", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.listen, func(t *testing.T) {
			t.Parallel()

			got, err := localURL(tt.listen)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
