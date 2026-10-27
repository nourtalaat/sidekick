package validate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrometheusRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		file       string
		wantErrors []string
		wantLine   bool
	}{
		{name: "good rules", file: "fixture/prometheus_rules/good/checkout.yaml"},
		{name: "expression that does not parse", file: "fixture/prometheus_rules/bad/bad-expression.yaml", wantErrors: []string{"could not parse expression"}, wantLine: true},
		{name: "unknown field", file: "fixture/prometheus_rules/bad/unknown-field.yaml", wantErrors: []string{"expression"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := fixtureFile(t, tt.file)

			res, err := newService(t, tools(t)).Validate(t.Context(), Request{Format: FormatPrometheusRules, Files: []File{f}})
			require.NoError(t, err)
			require.Equal(t, len(tt.wantErrors) == 0, res.OK, messages(res.Errors))
			require.Len(t, res.Errors, len(tt.wantErrors), messages(res.Errors))

			for i, want := range tt.wantErrors {
				require.Equal(t, f.Path, res.Errors[i].Path)
				require.Contains(t, res.Errors[i].Message, want)

				if tt.wantLine {
					require.Positive(t, res.Errors[i].Line)
				}
			}
		})
	}
}

func TestPrometheusRulesReportsEachFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		files     []string
		wantPaths []string
	}{
		{
			name:      "one good and one bad file",
			files:     []string{"fixture/prometheus_rules/good/checkout.yaml", "fixture/prometheus_rules/bad/bad-expression.yaml"},
			wantPaths: []string{"bad-expression.yaml"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var files []File
			for _, name := range tt.files {
				files = append(files, fixtureFile(t, name))
			}

			res, err := newService(t, tools(t)).Validate(t.Context(), Request{Format: FormatPrometheusRules, Files: files})
			require.NoError(t, err)

			var paths []string
			for _, p := range res.Errors {
				paths = append(paths, p.Path)
			}

			require.Equal(t, tt.wantPaths, paths, messages(res.Errors))
		})
	}
}

func TestPromtoolProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   []Problem
	}{
		{
			name:   "error with a position",
			output: "Checking rules.yaml\n  FAILED:\nrules.yaml: 5:15: group \"checkout\", rule 1, \"X\": could not parse expression: 1:5: parse error: unclosed left parenthesis\n",
			want:   []Problem{{Path: "rules.yaml", Line: 5, Message: `group "checkout", rule 1, "X": could not parse expression: 1:5: parse error: unclosed left parenthesis`}},
		},
		{
			name:   "error with details on the next lines",
			output: "Checking rules.yaml\n  FAILED:\nrules.yaml: yaml: unmarshal errors:\n  line 5: field expression not found in type rulefmt.Rule\n",
			want:   []Problem{{Path: "rules.yaml", Message: "yaml: unmarshal errors:; line 5: field expression not found in type rulefmt.Rule"}},
		},
		{
			name:   "output that names no file",
			output: "something went wrong\n",
			want:   []Problem{{Path: "rules.yaml", Message: "something went wrong"}},
		},
		{
			name:   "no output",
			output: "\n",
			want:   []Problem{{Path: "rules.yaml", Message: "promtool check rules failed and printed nothing"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, promtoolProblems("rules.yaml", tt.output))
		})
	}
}
