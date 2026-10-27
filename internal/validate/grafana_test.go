package validate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrafanaAlertFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		file           string
		wantErrors     []string
		wantNotChecked []string
	}{
		{
			name:           "good YAML with a Loki query, which is listed as not checked",
			file:           "fixture/grafana_alert_file/good/payments.yaml",
			wantNotChecked: []string{`groups[0].rules[1] ("Payments errors in the logs") data[0] (refId A): a loki query, not PromQL`},
		},
		{
			name: "good JSON with a query that names no datasource type",
			file: "fixture/grafana_alert_file/good/checkout.json",
		},
		{
			name: "missing required fields",
			file: "fixture/grafana_alert_file/bad/missing-fields.yaml",
			wantErrors: []string{
				`groups[0]: required: missing properties: ["interval"]`,
				`groups[0].rules[0]: required: missing properties: ["condition" "for"]`,
			},
		},
		{
			name:       "query that is not PromQL",
			file:       "fixture/grafana_alert_file/bad/bad-promql.yaml",
			wantErrors: []string{`groups[0].rules[0] ("Payments errors") data[0] (refId A): PromQL 1:`},
		},
		{
			name: "durations that Grafana does not read",
			file: "fixture/grafana_alert_file/bad/bad-duration.yaml",
			wantErrors: []string{
				`groups[0]: interval: pattern:`,
				`groups[0].rules[0]: for: type: 5 has type "integer", want "string"`,
			},
		},
		{
			name:       "Prometheus query with no expr",
			file:       "fixture/grafana_alert_file/bad/no-expr.yaml",
			wantErrors: []string{`groups[0].rules[0] ("Payments errors") data[0] (refId A): the Prometheus query has no expr`},
		},
		{
			name:       "not YAML",
			file:       "fixture/grafana_alert_file/bad/not-yaml.yaml",
			wantErrors: []string{"not valid YAML or JSON"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := fixtureFile(t, tt.file)

			res, err := newService(t, Options{}).Validate(t.Context(), Request{Format: FormatGrafanaAlertFile, Files: []File{f}})
			require.NoError(t, err)
			require.Equal(t, len(tt.wantErrors) == 0, res.OK, messages(res.Errors))
			require.Len(t, res.Errors, len(tt.wantErrors), messages(res.Errors))

			for i, want := range tt.wantErrors {
				require.Equal(t, f.Path, res.Errors[i].Path)
				require.Contains(t, res.Errors[i].Message, want)
			}

			require.Len(t, res.NotChecked, len(tt.wantNotChecked), messages(res.NotChecked))

			for i, want := range tt.wantNotChecked {
				require.Equal(t, want, res.NotChecked[i].Message)
			}
		})
	}
}

func TestGrafanaVariablesParse(t *testing.T) {
	t.Parallel()

	c, err := newGrafanaChecker()
	require.NoError(t, err)

	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{name: "rate interval", expr: `rate(x[$__rate_interval])`},
		{name: "interval in braces", expr: `rate(x[${__interval}])`},
		{name: "range in milliseconds as a number", expr: `x > $__range_ms`},
		{name: "a plain query", expr: `sum by (job) (up)`},
		{name: "a broken query", expr: `sum by (job) (up`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := c.parse(tt.expr)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
		})
	}
}
