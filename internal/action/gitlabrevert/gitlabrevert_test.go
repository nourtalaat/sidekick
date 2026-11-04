package gitlabrevert

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		change    func(p *Params)
		wantErr   string
		wantLimit bool
	}{
		{name: "a commit"},
		{name: "a merge request", change: func(p *Params) { p.Commit, p.MergeRequest = "", 7 }},
		{name: "the project in another case", change: func(p *Params) { p.Project = "ACME/Platform/Deploy" }},
		{name: "a project that is not allowed", change: func(p *Params) { p.Project = "acme/platform/other" }, wantErr: "is not one of limits.projects", wantLimit: true},
		{name: "a base branch that is not allowed", change: func(p *Params) { p.BaseBranch = "release" }, wantErr: "is not the allowed base branch", wantLimit: true},
		{name: "a commit and a merge request", change: func(p *Params) { p.MergeRequest = 7 }, wantErr: "either commit or mergeRequest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			p := commitParams()
			if tt.change != nil {
				tt.change(&p)
			}

			raw, err := json.Marshal(p)
			require.NoError(t, err)

			err = b.rv.Validate(t.Context(), raw)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
			require.Empty(t, b.w.srv.Requests(), "validating makes no call")
		})
	}
}

func TestRevertReadsCI(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)

	var reader gitchange.CIReader = b.rv

	require.Equal(t, "gitlab", reader.CIHost())
	require.Equal(t, 2*time.Hour, reader.CIReportTimeout())
}

func TestValidateUnknownField(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)

	require.Error(t, b.rv.Validate(t.Context(), json.RawMessage(`{"project":"acme/platform/deploy","baseBranch":"main","commit":"`+testSHA+`","title":"t","body":"b","draft":false}`)))
}

func TestSpec(t *testing.T) {
	t.Parallel()

	b := newBed(t, func(l *Limits) { l.Projects = []string{"acme/platform/deploy", "acme/web"} })
	spec := b.rv.Spec()

	require.Equal(t, "revert", spec.Name)
	require.NotEmpty(t, spec.ParamsSchema)

	for _, want := range []string{"acme/platform/deploy, acme/web", "Base branch: main", "at most 72h0m0s ago", "at most 5 files and 100 added and removed lines"} {
		require.Contains(t, spec.Description, want)
	}
}
