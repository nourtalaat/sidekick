package signoz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// fakeKeys resolves the key secret to a fake key, with a line break after
// it as a mounted secret may have.
type fakeKeys struct{}

func (fakeKeys) Resolve(_ context.Context, ref registry.SecretRef) ([]byte, error) {
	return []byte("fake-" + ref.Key + "\n"), nil
}

// emptyKeys resolves every secret to nothing.
type emptyKeys struct{}

func (emptyKeys) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte(" \n"), nil
}

// newTestClient returns a client of api, and the buffer its log goes to.
func newTestClient(t *testing.T, api *fakehttp.Server) (*client, *bytes.Buffer) {
	t.Helper()

	logs := &bytes.Buffer{}

	return &client{
		http: api.Client(), baseURL: api.URL(), header: DefaultKeyHeader, apiKey: keyRef,
		secrets: fakeKeys{}, logger: slog.New(slog.NewTextHandler(logs, nil)),
	}, logs
}

// versionAnswer is the body SigNoz answers GET /api/v1/version with.
func versionAnswer(version string) fakehttp.Response {
	return fakehttp.Response{Body: []byte(`{"version": "` + version + `", "ee": "Y", "setupCompleted": true}`)}
}

func TestParseRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		text   string
		want   release
		wantOK bool
	}{
		{text: "v0.148.1", want: release{0, 148, 1}, wantOK: true},
		{text: "0.134.0", want: release{0, 134, 0}, wantOK: true},
		{text: "v0.134.0-cloud.1", want: release{0, 134, 0}, wantOK: true},
		{text: " v1.2.3 ", want: release{1, 2, 3}, wantOK: true},
		{text: "<unset>"},
		{text: ""},
		{text: "v0.148"},
		{text: "latest"},
		{text: "v99999999999999999999.0.0"},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			t.Parallel()

			got, ok := parseRelease(tt.text)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestReleaseBefore(t *testing.T) {
	t.Parallel()

	require.True(t, release{0, 133, 9}.before(minVersion))
	require.True(t, release{0, 100, 0}.before(minVersion))
	require.False(t, release{0, 134, 0}.before(minVersion))
	require.False(t, release{0, 134, 1}.before(minVersion))
	require.False(t, release{0, 148, 1}.before(minVersion))
	require.False(t, release{1, 0, 0}.before(minVersion))
	require.Equal(t, "v0.134.0", minVersion.String())
}

func TestEnsureSupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version string
		wantErr string
		wantLog bool
	}{
		{version: "v0.148.1"},
		{version: "v0.134.0"},
		{version: "v0.134.0-cloud.1"},
		{version: "v1.0.0"},
		{version: "v0.133.9", wantErr: "SigNoz v0.133.9 is older than v0.134.0"},
		{version: "v0.100.0", wantErr: "SigNoz v0.100.0 is older than v0.134.0"},
		{version: "<unset>", wantLog: true},
		{version: "", wantLog: true},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/api/v1/version", versionAnswer(tt.version), versionAnswer(tt.version))
			c, logs := newTestClient(t, api)

			err := c.ensureSupported(t.Context())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrPermanent)
				require.ErrorIs(t, err, action.ErrLimit)

				// A refused release is asked about again: SigNoz may be upgraded.
				require.Error(t, c.ensureSupported(t.Context()))
				require.Len(t, api.Requests(), 2)

				return
			}

			require.NoError(t, err)
			require.NoError(t, c.ensureSupported(t.Context()))
			require.Len(t, api.Requests(), 1, "a release that is checked is not asked about again")
			require.Equal(t, tt.wantLog, strings.Count(logs.String(), "level=WARN") == 1, logs.String())
		})
	}
}

func TestEnsureSupportedWhenSigNozFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		wantPerm  bool
		wantInErr string
	}{
		{name: "the server is down", status: http.StatusServiceUnavailable, wantInErr: "SigNoz answered 503"},
		{name: "this is not SigNoz", status: http.StatusNotFound, wantPerm: true, wantInErr: "SigNoz answered 404"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/api/v1/version", fakehttp.Response{Status: tt.status})
			c, _ := newTestClient(t, api)

			err := c.ensureSupported(t.Context())
			require.ErrorContains(t, err, tt.wantInErr)
			require.Equal(t, tt.wantPerm, isPermanent(err))
		})
	}
}

func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent)
}

func TestGet(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, "/api/v2/rules/"+ruleOne, fakehttp.Response{Body: []byte(`{"status": "success", "data": {"id": "` + ruleOne + `", "alert": "x", "disabled": false}}`)})
	c, _ := newTestClient(t, api)

	rule, err := c.get(t.Context(), ruleOne)
	require.NoError(t, err)
	require.JSONEq(t, `"x"`, string(rule["alert"]))

	req := api.Requests()[0]
	require.Equal(t, "fake-apiKey", req.Header.Get("SIGNOZ-API-KEY"), "the key is trimmed")
	require.Equal(t, "application/json", req.Header.Get("Accept"))
}

func TestGetUsesTheHeaderOfTheConfig(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, "/api/v2/rules/"+ruleOne, fakehttp.Response{Body: []byte(`{"status": "success", "data": {}}`)})
	c, _ := newTestClient(t, api)
	c.header = "X-Key"

	_, err := c.get(t.Context(), ruleOne)
	require.NoError(t, err)

	req := api.Requests()[0]
	require.Equal(t, "fake-apiKey", req.Header.Get("X-Key"))
	require.Empty(t, req.Header.Get("SIGNOZ-API-KEY"))
}

func TestGetRefusesAnswersItCannotRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "not JSON", body: `<html>`},
		{name: "no data", body: `{"status": "success"}`},
		{name: "an error status", body: `{"status": "error", "data": {}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/api/v2/rules/"+ruleOne, fakehttp.Response{Body: []byte(tt.body)})
			c, _ := newTestClient(t, api)

			_, err := c.get(t.Context(), ruleOne)
			require.ErrorContains(t, err, "not something this action can read")
		})
	}
}

func TestStatusErrors(t *testing.T) {
	t.Parallel()

	const notFound = `{"status": "error", "error": {"type": "not-found", "code": "not_found", "message": "rule with ID: x does not exist", "errors": []}}`

	const invalid = `{"status": "error", "error": {"code": "invalid_input", "message": "alert rule is not valid", "errors": [{"message": "condition.target: field is required"}, {"message": "invalid label name: 1a"}]}}`

	tests := []struct {
		name     string
		status   int
		body     string
		wantPerm bool
		wantErr  string
	}{
		{name: "a rule that is not there", status: http.StatusNotFound, body: notFound, wantPerm: true, wantErr: "SigNoz answered 404: rule with ID: x does not exist"},
		{name: "a rule that is not valid", status: http.StatusBadRequest, body: invalid, wantPerm: true, wantErr: "SigNoz answered 400: alert rule is not valid; condition.target: field is required; invalid label name: 1a"},
		{name: "a key that is refused", status: http.StatusUnauthorized, body: `{"status": "error", "error": {"message": "invalid api key"}}`, wantPerm: true, wantErr: "SigNoz answered 401: invalid api key"},
		{name: "a role that cannot write", status: http.StatusForbidden, wantPerm: true, wantErr: "SigNoz answered 403"},
		{name: "too many requests", status: http.StatusTooManyRequests, wantErr: "SigNoz answered 429"},
		{name: "a failing server", status: http.StatusInternalServerError, body: `oops`, wantErr: "SigNoz answered 500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/api/v2/rules/"+ruleOne, fakehttp.Response{Status: tt.status, Body: []byte(tt.body)})
			c, _ := newTestClient(t, api)

			_, err := c.get(t.Context(), ruleOne)
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantPerm, isPermanent(err))
		})
	}
}

func TestUpdateSendsTheBodyAsGiven(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPut, "/api/v2/rules/"+ruleOne, fakehttp.Response{Status: http.StatusNoContent})
	c, _ := newTestClient(t, api)

	body := json.RawMessage(`{"alert":"x","disabled":false}`)
	require.NoError(t, c.update(t.Context(), ruleOne, body))

	req := api.Requests()[0]
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.JSONEq(t, string(body), string(req.Body))
}

func TestRemove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		wantErr string
	}{
		{name: "deleted", status: http.StatusNoContent},
		{name: "gone already", status: http.StatusNotFound},
		{name: "in use by a planned maintenance", status: http.StatusConflict, wantErr: "SigNoz answered 409"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodDelete, "/api/v2/rules/"+ruleOne, fakehttp.Response{Status: tt.status})
			c, _ := newTestClient(t, api)

			err := c.remove(t.Context(), ruleOne)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.True(t, isPermanent(err))
		})
	}
}

func TestAnEmptyKeyIsRefusedBeforeASend(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	c, _ := newTestClient(t, api)
	c.secrets = emptyKeys{}

	_, err := c.get(t.Context(), ruleOne)
	require.ErrorIs(t, err, errEmptyKey)
	require.Empty(t, api.Requests())
}
