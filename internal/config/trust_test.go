package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const trustWebhooks = `
intake:
  webhooks:
    alertmanager:
      adapter: alertmanager
      auth: { bearer: { name: sidekick-hooks, key: alertmanager } }
      trusted: true
    datadog:
      adapter: alertmanager
      auth: { bearer: { name: sidekick-hooks, key: datadog } }
`

const trustOnlyTrustedWebhooks = `
intake:
  webhooks:
    alertmanager:
      adapter: alertmanager
      auth: { bearer: { name: sidekick-hooks, key: alertmanager } }
      trusted: true
`

const trustSlack = `
slack:
  botTokenSecret: { name: sidekick-slack, key: botToken }
  appTokenSecret: { name: sidekick-slack, key: appToken }
  trustedBots: [B01ABCDEF]
listeners:
  alerts:
    channel: "#alerts"
    trusted: true
  chat:
    channel: "#chat"
`

const trustActions = `
actions:
  monitor-note:
    type: note.create
    tokenSecret: { name: notes, key: token }
    limits: { boards: [ops] }
`

// trustProblem is the problem the trusted-sender rule reports for the
// route at routes[i] when it can select the quoted intakes.
func trustProblem(i int, names ...string) string {
	quoted := make([]string, len(names))
	for j, n := range names {
		quoted[j] = fmt.Sprintf("%q", n)
	}

	return fmt.Sprintf("routes[%d].mode: mode read-write needs senders that are trusted, but this route can select %s; "+
		"list only trusted intakes in match.intake, mark them trusted: true, or set allowUntrustedWrite: true", i, strings.Join(quoted, ", "))
}

func TestTrustedSenderRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		intake string
		routes string
		want   []string
	}{
		{
			name: "a trusted intake", intake: trustWebhooks,
			routes: "- {name: ops, match: {intake: [alertmanager]}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
		},
		{
			name: "an untrusted intake", intake: trustWebhooks,
			routes: "- {name: ops, match: {intake: [datadog]}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
			want:   []string{trustProblem(0, "datadog")},
		},
		{
			name: "a trusted and an untrusted intake", intake: trustWebhooks,
			routes: "- {name: ops, match: {intake: [alertmanager, datadog]}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
			want:   []string{trustProblem(0, "datadog")},
		},
		{
			name: "no intake selects every intake", intake: trustWebhooks,
			routes: "- {name: ops, match: {service: \"checkout-*\"}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
			want:   []string{trustProblem(0, "datadog")},
		},
		{
			name: "no intake and only trusted intakes", intake: trustOnlyTrustedWebhooks,
			routes: "- {name: ops, match: {service: \"checkout-*\"}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
		},
		{
			name: "no intake with the Slack app, whose mentions are never trusted", intake: trustOnlyTrustedWebhooks + trustSlack,
			routes: "- {name: ops, match: {service: \"checkout-*\"}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
			want:   []string{trustProblem(0, "chat", "slack-mention")},
		},
		{
			name: "a trusted listener and a trusted intake", intake: trustOnlyTrustedWebhooks + trustSlack,
			routes: "- {name: ops, match: {intake: [alerts, alertmanager]}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
		},
		{
			name: "a listener that is not trusted", intake: trustOnlyTrustedWebhooks + trustSlack,
			routes: "- {name: ops, match: {intake: [chat]}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
			want:   []string{trustProblem(0, "chat")},
		},
		{
			name: "a mention", intake: trustOnlyTrustedWebhooks + trustSlack,
			routes: "- {name: ops, match: {intake: [alerts, slack-mention]}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
			want:   []string{trustProblem(0, "slack-mention")},
		},
		{
			name: "the override", intake: trustWebhooks + trustSlack,
			routes: "- {name: ops, match: {service: \"checkout-*\"}, mode: read-write, allowUntrustedWrite: true, actions: [monitor-note]}\n  - {name: default}",
		},
		{
			name: "the default route", intake: trustWebhooks,
			routes: "- {name: default, mode: read-write, actions: [monitor-note]}",
			want:   []string{trustProblem(0, "datadog")},
		},
		{
			name: "an intake that is not defined is reported once", intake: trustWebhooks,
			routes: "- {name: ops, match: {intake: [alertmanger]}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
			want:   []string{`routes[0].match.intake[0]: intake "alertmanger" is not defined (did you mean "alertmanager"?)`},
		},
		{
			name: "approved mode is not limited", intake: trustWebhooks,
			routes: "- {name: ops, match: {intake: [datadog]}, mode: approved, actions: [monitor-note]}\n  - {name: default}",
		},
		{
			name: "read-write lists at least one action", intake: trustOnlyTrustedWebhooks,
			routes: "- {name: ops, match: {intake: [alertmanager]}, mode: read-write}\n  - {name: default}",
			want:   []string{"routes[0].actions: list at least one action: mode read-write lets the model propose only these"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			yaml := baseYAML + tt.intake + trustActions + "\nroutes:\n  " + tt.routes + "\n"

			cfg, err := Parse(t.Context(), []byte(yaml), testOptions())
			if len(tt.want) == 0 {
				require.NoError(t, err)
				require.NotNil(t, cfg)

				return
			}

			var errs Errors
			require.ErrorAs(t, err, &errs)
			require.Equal(t, strings.Join(tt.want, "\n"), errs.Error())
		})
	}
}

func TestAllowUntrustedWriteIsRead(t *testing.T) {
	t.Parallel()

	yaml := baseYAML + trustWebhooks + trustActions + `
routes:
  - {name: ops, mode: read-write, allowUntrustedWrite: true, actions: [monitor-note]}
  - {name: default}
`

	cfg, err := Parse(t.Context(), []byte(yaml), testOptions())
	require.NoError(t, err)
	require.True(t, cfg.Routes[0].AllowUntrustedWrite)
	require.False(t, cfg.Routes[1].AllowUntrustedWrite)
}
