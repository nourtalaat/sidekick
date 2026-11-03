package gitchange

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

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

// fakeReader serves the files of a repository from a map of path to text.
type fakeReader struct {
	files map[string]string
	err   error
}

func (r fakeReader) Dir(_ context.Context, dir string) ([]DirEntry, error) {
	if r.err != nil {
		return nil, r.err
	}

	var entries []DirEntry

	for p := range r.files {
		if path.Dir(p) == dir {
			entries = append(entries, DirEntry{Name: path.Base(p), Path: p, SHA: "sha-" + p, Size: len(r.files[p])})
		}
	}

	return entries, nil
}

func (r fakeReader) Blob(_ context.Context, e DirEntry) (string, error) {
	return r.files[e.Path], nil
}

func TestValidateFilesFlatFormats(t *testing.T) {
	t.Parallel()

	changes := []PlannedFile{
		{Path: "rules/a.yaml", Format: validateclient.FormatPrometheusRules, Content: text("a")},
		{Path: "rules/gone.yaml", Format: validateclient.FormatPrometheusRules},
		{Path: "grafana/g.yaml", Format: validateclient.FormatGrafanaAlertFile, Content: text("g")},
	}

	ctrl := gomock.NewController(t)
	validator := validateclient.NewMockValidator(ctrl)
	validator.EXPECT().Validate(gomock.Any(), validateclient.Request{
		Format: validateclient.FormatPrometheusRules, Files: []validateclient.File{{Path: "rules/a.yaml", Content: "a"}},
	}).Return(validateclient.Result{OK: true}, nil)
	validator.EXPECT().Validate(gomock.Any(), validateclient.Request{
		Format: validateclient.FormatGrafanaAlertFile, Files: []validateclient.File{{Path: "grafana/g.yaml", Content: "g"}},
	}).Return(validateclient.Result{OK: true}, nil)

	var seen []string

	err := ValidateFiles(t.Context(), validator, fakeReader{}, changes, func(format, result string) { seen = append(seen, format+":"+result) })
	require.NoError(t, err)
	require.Equal(t, []string{"prometheus_rules:passed", "grafana_alert_file:passed"}, seen, "a deleted file is not sent")
}

func TestValidateFilesResults(t *testing.T) {
	t.Parallel()

	changes := []PlannedFile{{Path: "rules/a.yaml", Format: validateclient.FormatPrometheusRules, Content: text("a")}}

	tests := []struct {
		name          string
		result        validateclient.Result
		err           error
		wantErr       string
		wantPermanent bool
		wantLimit     bool
		wantSeen      string
	}{
		{name: "the file fails", result: validateclient.Result{Errors: []validateclient.Problem{{Path: "rules/a.yaml", Line: 3, Message: "bad\nexpr"}}}, wantErr: "- rules/a.yaml:3: bad expr", wantPermanent: true, wantLimit: true, wantSeen: "prometheus_rules:failed"},
		{name: "the validator times out", err: validateclient.ErrTimeout, wantErr: "timed out", wantSeen: "prometheus_rules:unavailable"},
		{name: "the validator refuses the request", err: validateclient.ErrBadRequest, wantErr: "refused the request", wantPermanent: true, wantSeen: "prometheus_rules:unavailable"},
		{name: "the files are too large", err: validateclient.ErrTooLarge, wantErr: "too large", wantPermanent: true, wantLimit: true, wantSeen: "prometheus_rules:unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			validator := validateclient.NewMockValidator(ctrl)
			validator.EXPECT().Validate(gomock.Any(), gomock.Any()).Return(tt.result, tt.err)

			var seen []string

			err := ValidateFiles(t.Context(), validator, fakeReader{}, changes, func(format, result string) { seen = append(seen, format+":"+result) })
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
			require.Equal(t, []string{tt.wantSeen}, seen)
		})
	}
}

func TestValidateFilesNilObserver(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	validator := validateclient.NewMockValidator(ctrl)
	validator.EXPECT().Validate(gomock.Any(), gomock.Any()).Return(validateclient.Result{OK: true}, nil)

	changes := []PlannedFile{{Path: "rules/a.yaml", Format: validateclient.FormatPrometheusRules, Content: text("a")}}
	require.NoError(t, ValidateFiles(t.Context(), validator, fakeReader{}, changes, nil))
}

func TestValidateFilesTerraformModule(t *testing.T) {
	t.Parallel()

	repo := fakeReader{files: map[string]string{
		"tf/prod/main.tf":   "module \"m\" {\n  source = \"../mod\"\n}\n",
		"tf/prod/gone.tf":   "to delete\n",
		"tf/prod/README.md": "not a module file\n",
		"tf/mod/alert.tf":   "variable \"x\" {}\n",
		"tf/other/a.tf":     "a different module\n",
	}}

	changes := []PlannedFile{
		{Path: "tf/prod/main.tf", Format: validateclient.FormatTerraform, Content: text("changed\n")},
		{Path: "tf/prod/gone.tf", Format: validateclient.FormatTerraform},
		{Path: "tf/prod/new.tf", Format: validateclient.FormatTerraform, Content: text("new\n")},
		{Path: "tf/prod/README.md", Format: validateclient.FormatTerraform, Content: text("docs\n")},
	}

	ctrl := gomock.NewController(t)
	validator := validateclient.NewMockValidator(ctrl)
	validator.EXPECT().Validate(gomock.Any(), validateclient.Request{
		Format: validateclient.FormatTerraform,
		Dir:    "tf/prod",
		Files: []validateclient.File{
			{Path: "tf/mod/alert.tf", Content: "variable \"x\" {}\n"},
			{Path: "tf/prod/main.tf", Content: "changed\n"},
			{Path: "tf/prod/new.tf", Content: "new\n"},
		},
	}).Return(validateclient.Result{OK: true}, nil)

	require.NoError(t, ValidateFiles(t.Context(), validator, repo, changes, nil), "one request for the module: its files with the changes applied and the local module, no README, no other module")
}

func TestValidateFilesTerraformReadError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	validator := validateclient.NewMockValidator(ctrl)

	changes := []PlannedFile{{Path: "tf/main.tf", Format: validateclient.FormatTerraform, Content: text("a\n")}}
	err := ValidateFiles(t.Context(), validator, fakeReader{err: errors.New("repository unreachable")}, changes, nil)
	require.ErrorContains(t, err, "collect the Terraform files")
	require.ErrorContains(t, err, "repository unreachable")
}

func TestLocalModules(t *testing.T) {
	t.Parallel()

	tf := "module \"a\" {\n  source = \"../a\"\n}\nmodule \"b\" {\n  source = \"./b\"\n}\nmodule \"c\" {\n  source = \"../../../out\"\n}\nmodule \"d\" {\n  source = \"hashicorp/x/y\"\n}\n"
	require.Equal(t, []string{"tf/a", "tf/prod/b"}, localModules("tf/prod", tf), "a source that leaves the repository or is remote is left out")
}
