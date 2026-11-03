package githubpr

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

func TestCheckProposal(t *testing.T) {
	t.Parallel()

	t.Run("files that pass", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validatorPasses(1)
		b.github.ExpectAllCalled()
		b.validator.ExpectAllCalled()

		require.NoError(t, b.pr.CheckProposal(t.Context(), invocation(rulesParams()).Params))

		for _, call := range sent(b.github) {
			require.True(t, strings.HasPrefix(call, "GET "), "checking a proposal writes nothing: %s", call)
		}
	})

	t.Run("files that fail go back to the model with their errors", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validator.Handle(http.MethodPost, "/validate", reply(`{"ok":false,"errors":[
			{"path":"`+testRulesPath+`","line":6,"message":"bad duration"},
			{"path":"`+testRulesPath+`","message":"line one\nline two"}]}`))

		err := b.pr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "no pull request is opened until they are fixed:\n- "+testRulesPath+":6: bad duration\n- "+testRulesPath+": line one line two")
	})

	t.Run("a change that is too large", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, func(l *Limits) { l.MaxChangedLines = 1 })
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)

		err := b.pr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, action.ErrLimit)
		require.ErrorContains(t, err, "more than the 1 lines")
	})

	t.Run("a validator that cannot be reached", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validator.Handle(http.MethodPost, "/validate", replyStatus(http.StatusServiceUnavailable, `{"error":"the validator is busy"}`))

		err := b.pr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, validateclient.ErrBusy)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("parameters outside the limits stop before any call", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		p := rulesParams()
		p.Repo = "acme/other"

		err := b.pr.CheckProposal(t.Context(), invocation(p).Params)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, b.github.Requests())
	})
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  json.RawMessage
		wantErr string
		wantIs  error
	}{
		{name: "parameters within the limits", params: invocation(rulesParams()).Params},
		{name: "not JSON", params: json.RawMessage(`{`), wantErr: "decode the parameters"},
		{name: "an unknown field", params: json.RawMessage(`{"repo":"acme/monitors","force":true}`), wantErr: "unknown field"},
		{
			name: "outside the limits",
			params: func() json.RawMessage {
				p := rulesParams()
				p.Files[0].Path = "src/main.go"

				return invocation(p).Params
			}(),
			wantErr: "matches none of the limits.paths globs",
			wantIs:  action.ErrLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			err := b.pr.Validate(t.Context(), tt.params)
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.Empty(t, b.github.Requests(), "Validate makes no network call")
				require.Empty(t, b.validator.Requests())

				return
			}

			require.ErrorContains(t, err, tt.wantErr)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	spec := b.pr.Spec()

	require.Equal(t, "monitor-pr", spec.Name)

	for _, want := range []string{
		"acme/monitors", "Base branch: main", "At most 3 files and 40 added and removed lines",
		"prometheus/rules/** (prometheus_rules)", "datadog/** (terraform)", "grafana/alerting/** (grafana_alert_file)",
	} {
		require.Contains(t, spec.Description, want)
	}

	_, err := tools.ParseInputSchema(spec.ParamsSchema)
	require.NoError(t, err)
}

func TestValidationErrorText(t *testing.T) {
	t.Parallel()

	problems := make([]validateclient.Problem, 0, maxReportedProblems+3)
	for range maxReportedProblems + 3 {
		problems = append(problems, validateclient.Problem{Path: "a.yaml", Line: 2, Message: strings.Repeat("m", maxProblemBytes+50)})
	}

	text := (&validationError{problems: problems}).Error()

	require.Equal(t, maxReportedProblems+1, strings.Count(text, "\n"), "the listed problems and the line that counts the rest, each after the lead")
	require.Contains(t, text, "\n- and 3 more")
	require.NotContains(t, text, strings.Repeat("m", maxProblemBytes+1))
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), TypeName)

	yamlText := `
app: {appID: 11, installationID: 22, privateKeySecret: {name: monitor-pr, key: key.pem}}
limits:
  repos: [acme/monitors]
  paths:
    - {glob: "prometheus/rules/**", format: prometheus_rules}
  maxFiles: 2
  maxChangedLines: 30
  baseBranch: main
`

	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(yamlText), &node))

	built, err := registry.Actions.Build(t.Context(), TypeName, "monitor-pr", node.Content[0], registry.Deps{Secrets: secretsWithKey(t)})
	require.NoError(t, err)
	require.Equal(t, "monitor-pr", built.Spec().Name)

	_, isChecker := built.(action.ProposalChecker)
	require.True(t, isChecker)

	bad := strings.Replace(yamlText, "format: prometheus_rules", "format: nagios", 1)
	require.NoError(t, yaml.Unmarshal([]byte(bad), &node))

	_, err = registry.Actions.Check(t.Context(), TypeName, "monitor-pr", node.Content[0])
	require.ErrorContains(t, err, "limits.paths[0].format")
}

func TestValidatorOfDeps(t *testing.T) {
	t.Parallel()

	changes := []gitchange.PlannedFile{{Path: "prometheus/rules/a.yaml", Format: validateclient.FormatPrometheusRules, Content: text("groups: []\n")}}

	t.Run("a validator in the deps is used instead of the URL", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		validator := validateclient.NewMockValidator(ctrl)
		validator.EXPECT().Validate(gomock.Any(), validateclient.Request{
			Format: validateclient.FormatPrometheusRules,
			Files:  []validateclient.File{{Path: "prometheus/rules/a.yaml", Content: "groups: []\n"}},
		}).Return(validateclient.Result{OK: true, Errors: []validateclient.Problem{}}, nil)

		pr, err := newPR("monitor-pr", Config{App: testApp, Limits: validLimits()}, registry.Deps{
			Secrets:      secretsWithKey(t),
			ValidatorURL: "http://127.0.0.1:1",
			Validator:    validator,
		})
		require.NoError(t, err)

		require.NoError(t, pr.validateFiles(t.Context(), nil, changes))
	})

	t.Run("a job-mode error keeps its kind", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		validator := validateclient.NewMockValidator(ctrl)
		validator.EXPECT().Validate(gomock.Any(), gomock.Any()).Return(validateclient.Result{}, validateclient.ErrTimeout)

		pr, err := newPR("monitor-pr", Config{App: testApp, Limits: validLimits()}, registry.Deps{Secrets: secretsWithKey(t), Validator: validator})
		require.NoError(t, err)

		err = pr.validateFiles(t.Context(), nil, changes)
		require.ErrorIs(t, err, validateclient.ErrTimeout)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})
}
