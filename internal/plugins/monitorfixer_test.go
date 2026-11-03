package plugins_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/datadog"
	"github.com/nourtalaat/sidekick/internal/action/githubcode"
	"github.com/nourtalaat/sidekick/internal/action/githubpr"
	"github.com/nourtalaat/sidekick/internal/action/githubrevert"
	"github.com/nourtalaat/sidekick/internal/action/gitlabmr"
	"github.com/nourtalaat/sidekick/internal/action/grafana"
	"github.com/nourtalaat/sidekick/internal/action/helmrollback"
	"github.com/nourtalaat/sidekick/internal/action/jira"
	"github.com/nourtalaat/sidekick/internal/action/kubernetesrollback"
	"github.com/nourtalaat/sidekick/internal/action/prometheusrule"
	"github.com/nourtalaat/sidekick/internal/action/signoz"
	"github.com/nourtalaat/sidekick/internal/action/slack"
	"github.com/nourtalaat/sidekick/internal/action/unleashflag"
)

func TestMonitorFixers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action action.Action
		want   action.FixKind
	}{
		{name: "datadog.monitor", action: &datadog.Monitor{}, want: action.FixAPI},
		{name: "signoz.alert_rule", action: &signoz.AlertRule{}, want: action.FixAPI},
		{name: "grafana.alert_rule", action: &grafana.Rule{}, want: action.FixAPI},
		{name: "prometheus.rule", action: &prometheusrule.Rule{}, want: action.FixAPI},
		{name: "github.pull_request", action: &githubpr.PR{}, want: action.FixPullRequest},
		{name: "github.revert fixes nothing", action: &githubrevert.Revert{}},
		{name: "github.code_change fixes nothing", action: &githubcode.Code{}},
		{name: "gitlab.merge_request", action: &gitlabmr.MR{}, want: action.FixPullRequest},
		{name: "helm.rollback fixes nothing", action: &helmrollback.Rollback{}},
		{name: "jira.issue fixes nothing", action: &jira.Issue{}},
		{name: "slack.reply fixes nothing", action: &slack.Reply{}},
		{name: "unleash.flag fixes nothing", action: &unleashflag.Flag{}},
		{name: "kubernetes.rollback fixes nothing", action: &kubernetesrollback.Rollback{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixer, ok := tt.action.(action.MonitorFixer)
			require.Equal(t, tt.want != "", ok)

			if ok {
				require.Equal(t, tt.want, fixer.FixKind())
			}
		})
	}
}
