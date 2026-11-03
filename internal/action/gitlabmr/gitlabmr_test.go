package gitlabmr

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
		b.gitlab.ExpectAllCalled()
		b.validator.ExpectAllCalled()

		require.NoError(t, b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params))

		for _, call := range sent(b.gitlab) {
			require.True(t, strings.HasPrefix(call, "GET "), "checking a proposal writes nothing: %s", call)
		}

		require.Equal(t, testBaseSHA, queryOf(t, b.gitlab, http.MethodGet, apiPath("repository/files/"+testRulesPath)).Get("ref"), "files are read at the commit of the base branch")
		require.Equal(t, testTokenValue, b.gitlab.Requests()[0].Header.Get("PRIVATE-TOKEN"))
	})

	t.Run("files that fail go back to the model with their errors", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validator.Handle(http.MethodPost, "/validate", reply(`{"ok":false,"errors":[
			{"path":"`+testRulesPath+`","line":6,"message":"bad duration"},
			{"path":"`+testRulesPath+`","message":"line one\nline two"}]}`))

		err := b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "until they are fixed:\n- "+testRulesPath+":6: bad duration\n- "+testRulesPath+": line one line two")
	})

	t.Run("a change that is too large", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, func(l *Limits) { l.MaxChangedLines = 1 })
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)

		err := b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, action.ErrLimit)
		require.ErrorContains(t, err, "more than the 1 lines")
	})

	t.Run("a change of exactly the most lines", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, func(l *Limits) { l.MaxChangedLines = 2 })
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validatorPasses(1)

		require.NoError(t, b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params))
	})

	t.Run("a file that already has the new content", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectBase()
		b.expectFile(testRulesPath, testRulesAfter)

		err := b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "already has this content")
	})

	t.Run("a delete of a file that does not exist", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectBase()
		b.expectNoFile(testRulesPath)

		p := validParams()
		p.Files = []gitchange.FileChange{{Path: testRulesPath, Delete: true}}

		err := b.mr.CheckProposal(t.Context(), invocation(p).Params)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "cannot be deleted")
	})

	t.Run("a base branch that does not exist", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/main"), notFound)

		err := b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "404 Not Found")
	})

	t.Run("a GitLab that is down is tried again", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/main"), failed(http.StatusBadGateway, "bad gateway"))

		err := b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.Error(t, err)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("a validator that cannot be reached", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectBase()
		b.expectFile(testRulesPath, testRulesBefore)
		b.validator.Handle(http.MethodPost, "/validate", replyStatus(http.StatusServiceUnavailable, `{"error":"the validator is busy"}`))

		err := b.mr.CheckProposal(t.Context(), invocation(rulesParams()).Params)
		require.ErrorIs(t, err, validateclient.ErrBusy)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("parameters outside the limits stop before any call", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		p := rulesParams()
		p.Project = "acme/other"

		err := b.mr.CheckProposal(t.Context(), invocation(p).Params)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, b.gitlab.Requests())
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
		{name: "an unknown field", params: json.RawMessage(`{"project":"acme/platform/monitors","force":true}`), wantErr: "unknown field"},
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

			err := b.mr.Validate(t.Context(), tt.params)
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.Empty(t, b.gitlab.Requests(), "Validate makes no network call")
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
	spec := b.mr.Spec()

	require.Equal(t, "monitor-mr", spec.Name)

	for _, want := range []string{
		"acme/platform/monitors", "Base branch: main", "At most 3 files and 40 added and removed lines",
		"prometheus/rules/** (prometheus_rules)", "datadog/** (terraform)", "grafana/alerting/** (grafana_alert_file)",
	} {
		require.Contains(t, spec.Description, want)
	}

	_, err := tools.ParseInputSchema(spec.ParamsSchema)
	require.NoError(t, err)
}

func TestFixKind(t *testing.T) {
	t.Parallel()

	require.Equal(t, action.FixPullRequest, (&MR{}).FixKind())
}

func TestNewNeedsSecrets(t *testing.T) {
	t.Parallel()

	_, err := newMR("monitor-mr", Config{TokenSecret: testToken, Limits: validLimits()}, registry.Deps{})
	require.Error(t, err)
}

func TestValidatorOfDeps(t *testing.T) {
	t.Parallel()

	changes := []gitchange.PlannedFile{{Path: "prometheus/rules/a.yaml", Format: validateclient.FormatPrometheusRules, Content: text("groups: []\n")}}

	secrets := func(t *testing.T) registry.Secrets {
		t.Helper()

		s := registry.NewMockSecrets(gomock.NewController(t))
		s.EXPECT().Resolve(gomock.Any(), testToken).Return([]byte(testTokenValue), nil).AnyTimes()

		return s
	}

	t.Run("a validator in the deps is used instead of the URL", func(t *testing.T) {
		t.Parallel()

		validator := validateclient.NewMockValidator(gomock.NewController(t))
		validator.EXPECT().Validate(gomock.Any(), validateclient.Request{
			Format: validateclient.FormatPrometheusRules,
			Files:  []validateclient.File{{Path: "prometheus/rules/a.yaml", Content: "groups: []\n"}},
		}).Return(validateclient.Result{OK: true, Errors: []validateclient.Problem{}}, nil)

		mr, err := newMR("monitor-mr", Config{TokenSecret: testToken, Limits: validLimits()}, registry.Deps{
			Secrets:      secrets(t),
			ValidatorURL: "http://127.0.0.1:1",
			Validator:    validator,
		})
		require.NoError(t, err)

		require.NoError(t, mr.validateFiles(t.Context(), nil, changes))
	})

	t.Run("a job-mode error keeps its kind", func(t *testing.T) {
		t.Parallel()

		validator := validateclient.NewMockValidator(gomock.NewController(t))
		validator.EXPECT().Validate(gomock.Any(), gomock.Any()).Return(validateclient.Result{}, validateclient.ErrTimeout)

		mr, err := newMR("monitor-mr", Config{TokenSecret: testToken, Limits: validLimits()}, registry.Deps{Secrets: secrets(t), Validator: validator})
		require.NoError(t, err)

		err = mr.validateFiles(t.Context(), nil, changes)
		require.ErrorIs(t, err, validateclient.ErrTimeout)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), TypeName)

	yamlText := `
baseURL: https://gitlab.example.com
tokenSecret: {name: monitor-mr, key: token}
limits:
  projects: [acme/platform/monitors]
  paths:
    - {glob: "prometheus/rules/**", format: prometheus_rules}
  maxFiles: 2
  maxChangedLines: 30
  baseBranch: main
`

	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(yamlText), &node))

	built, err := registry.Actions.Build(t.Context(), TypeName, "monitor-mr", node.Content[0], registry.Deps{Secrets: registrySecrets(t)})
	require.NoError(t, err)
	require.Equal(t, "monitor-mr", built.Spec().Name)

	_, isChecker := built.(action.ProposalChecker)
	require.True(t, isChecker)

	_, isReader := built.(action.FileReader)
	require.True(t, isReader)

	_, isUndoer := built.(action.Undoer)
	require.True(t, isUndoer)

	_, isFixer := built.(action.MonitorFixer)
	require.True(t, isFixer)

	bad := strings.Replace(yamlText, "format: prometheus_rules", "format: nagios", 1)
	require.NoError(t, yaml.Unmarshal([]byte(bad), &node))

	_, err = registry.Actions.Check(t.Context(), TypeName, "monitor-mr", node.Content[0])
	require.ErrorContains(t, err, "limits.paths[0].format")

	plain := strings.Replace(yamlText, "https://gitlab.example.com", "http://gitlab.example.com", 1)
	require.NoError(t, yaml.Unmarshal([]byte(plain), &node))

	_, err = registry.Actions.Check(t.Context(), TypeName, "monitor-mr", node.Content[0])
	require.ErrorContains(t, err, "must use https")
}
