package validate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeJWT has the shape of a service account token; it signs nothing.
const fakeJWT = "eyJhbGciOiJSUzI1NiIsImtpZCI6InRlc3QifQ.eyJzdWIiOiJzeXN0ZW06c2VydmljZWFjY291bnQ6c2lkZWtpY2s6c2lkZWtpY2sifQ.c2lnbmF0dXJlLW5vdC1yZWFsLWF0LWFsbA"

func TestRedactTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "token in an OpenTofu error",
			in:   `Invalid function argument: cannot convert "` + fakeJWT + `" to number.`,
			want: `Invalid function argument: cannot convert "<redacted>" to number.`,
		},
		{name: "two tokens", in: fakeJWT + " and " + fakeJWT, want: "<redacted> and <redacted>"},
		{name: "token on its own line", in: "line 1\n" + fakeJWT + "\nline 3", want: "line 1\n<redacted>\nline 3"},
		{name: "version numbers", in: "datadog 4.25.0 and tofu 1.13.1", want: "datadog 4.25.0 and tofu 1.13.1"},
		{name: "provider address", in: "registry.opentofu.org/datadog/datadog", want: "registry.opentofu.org/datadog/datadog"},
		{name: "rule path", in: `groups[0].rules[1] ("Payments errors") data[0]`, want: `groups[0].rules[1] ("Payments errors") data[0]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, RedactTokens(tt.in))
		})
	}
}

func TestValidateRedactsTokensInResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		file File
	}{
		{
			name: "token in a value the schema quotes",
			file: File{Path: "a.yaml", Content: "apiVersion: 1\ngroups:\n  - name: g\n    folder: f\n    interval: " + fakeJWT + "\n    rules: []\n"},
		},
		{
			name: "token in a rule title that a query error names",
			file: File{Path: "a.yaml", Content: "apiVersion: 1\ngroups:\n  - name: g\n    folder: f\n    interval: 1m\n    rules:\n" +
				"      - uid: u\n        title: " + fakeJWT + "\n        condition: A\n        for: 5m\n        data:\n" +
				"          - refId: A\n            datasourceUid: prometheus\n            model: { expr: 'up{' }\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := newService(t, Options{}).Validate(t.Context(), Request{Format: FormatGrafanaAlertFile, Files: []File{tt.file}})
			require.NoError(t, err)
			require.False(t, res.OK)
			require.NotEmpty(t, res.Errors)

			for _, p := range res.Errors {
				require.NotContains(t, p.Message, fakeJWT)
			}

			require.Contains(t, messages(res.Errors)[0], redacted)
		})
	}
}

func TestHandlerRedactsTokensInErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "token in an unknown format", body: `{"format":"` + fakeJWT + `","files":[{"path":"a"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/validate", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			newService(t, Options{}).Handler().ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.NotContains(t, rec.Body.String(), fakeJWT)
			require.Contains(t, rec.Body.String(), redacted)
		})
	}
}

func TestTerraformRedactsTokensThatFileReads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []File
	}{
		{
			name: "file() read into a type error",
			files: []File{
				{Path: "main.tf", Content: "locals {\n  x = tonumber(file(\"token\"))\n}\n\noutput \"x\" {\n  value = local.x\n}\n"},
				{Path: "token", Content: fakeJWT},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := newService(t, tools(t)).Validate(t.Context(), Request{Format: FormatTerraform, Files: tt.files})
			require.NoError(t, err)
			require.False(t, res.OK)

			for _, p := range res.Errors {
				require.NotContains(t, p.Message, fakeJWT)
			}

			require.Contains(t, strings.Join(messages(res.Errors), "\n"), redacted)
		})
	}
}
