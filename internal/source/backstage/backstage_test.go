package backstage

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// byName is the path of the entity lookup API.
const byName = "/api/catalog/entities/by-name/"

func TestSourceCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		tool          string
		input         string
		path          string
		response      fakehttp.Response
		want          string
		wantEvidence  []source.Evidence
		wantNoData    bool
		wantText      string
		wantToolError string
		wantErr       string
	}{
		{
			name:     "get_service of a bare name reads a component",
			tool:     toolGetService,
			input:    `{"service":"checkout-api"}`,
			path:     byName + "component/default/checkout-api",
			response: fixture("component.json"),
			want: `{"ref":"component:default/checkout-api","title":"Checkout API","description":"Takes orders and payments.",
				"type":"service","lifecycle":"production","owners":["group:default/payments"],"systems":["system:default/shop"],
				"dependsOn":["component:default/payments-api","resource:default/orders-db"],
				"dependencyOf":["component:default/storefront"],
				"providesApis":["api:default/checkout"],"consumesApis":["api:default/payments"],
				"tags":["go","payments"],"links":[{"title":"Dashboard","url":"https://grafana.example.com/d/checkout"}]}`,
			wantEvidence: []source.Evidence{entityLink("component:default/checkout-api", "default/component/checkout-api")},
		},
		{
			name:         "get_service of a resource ref",
			tool:         toolGetService,
			input:        `{"service":"Resource:Data/orders-db"}`,
			path:         byName + "resource/data/orders-db",
			response:     fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{"kind":"Resource","metadata":{"name":"orders-db"},"spec":{"type":"database"}}`)},
			want:         `{"ref":"resource:data/orders-db","type":"database"}`,
			wantEvidence: []source.Evidence{entityLink("resource:data/orders-db", "data/resource/orders-db")},
		},
		{
			name:     "get_owner of a bare name reads a group",
			tool:     toolGetOwner,
			input:    `{"owner":"payments"}`,
			path:     byName + "group/default/payments",
			response: fixture("group.json"),
			want: `{"ref":"group:default/payments","description":"The payments team.","type":"team","displayName":"Payments",
				"email":"payments@example.com","parents":["group:default/engineering"],
				"members":["user:default/ada","user:default/grace"],
				"owns":["component:default/checkout-api","system:default/shop"]}`,
			wantEvidence: []source.Evidence{entityLink("group:default/payments", "default/group/payments")},
		},
		{
			name:     "get_system",
			tool:     toolGetSystem,
			input:    `{"system":"system:default/shop"}`,
			path:     byName + "system/default/shop",
			response: fixture("system.json"),
			want: `{"ref":"system:default/shop","title":"Shop","owners":["group:default/payments"],
				"domains":["domain:default/commerce"],"parts":["component:default/checkout-api","resource:default/orders-db"]}`,
			wantEvidence: []source.Evidence{entityLink("system:default/shop", "default/system/shop")},
		},
		{
			name:       "an entity the catalog does not have is no data",
			tool:       toolGetService,
			input:      `{"service":"missing"}`,
			path:       byName + "component/default/missing",
			response:   failed(http.StatusNotFound, "No entity named 'missing' found"),
			wantNoData: true,
			wantText:   notFoundText,
		},
		{
			name:          "a request Backstage refuses as bad is a tool error",
			tool:          toolGetOwner,
			input:         `{"owner":"user:ada"}`,
			path:          byName + "user/default/ada",
			response:      failed(http.StatusBadRequest, "Invalid entity ref"),
			wantToolError: "Backstage answered 400: Invalid entity ref",
		},
		{
			name:     "a refused token is a failure of the source",
			tool:     toolGetService,
			input:    `{"service":"checkout-api"}`,
			path:     byName + "component/default/checkout-api",
			response: failed(http.StatusUnauthorized, "Missing credentials"),
			wantErr:  "Backstage answered 401: Missing credentials",
		},
		{
			name:     "a server error without a message is a failure of the source",
			tool:     toolGetService,
			input:    `{"service":"checkout-api"}`,
			path:     byName + "component/default/checkout-api",
			response: fakehttp.Response{Status: http.StatusBadGateway, Body: []byte("<html>bad gateway</html>")},
			wantErr:  "Backstage answered 502",
		},
		{
			name:     "an answer that is not JSON is a failure of the source",
			tool:     toolGetSystem,
			input:    `{"system":"shop"}`,
			path:     byName + "system/default/shop",
			response: fakehttp.Response{Headers: jsonHeaders, Body: []byte("{")},
			wantErr:  "decode the answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, tt.path, tt.response)
			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t), 0)

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			requests := api.Requests()
			require.Len(t, requests, 1)
			require.Equal(t, "Bearer "+testToken, requests[0].Header.Get("Authorization"))
			require.Equal(t, "application/json", requests[0].Header.Get("Accept"))
			require.True(t, strings.HasPrefix(requests[0].Header.Get("User-Agent"), "sidekick/"))

			switch {
			case tt.wantToolError != "":
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Equal(t, tt.wantToolError, toolErr.Message)
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)

				_, isToolErr := errors.AsType[*source.ToolError](err)
				require.False(t, isToolErr, "a failure of the source is not a tool error")
			case tt.wantNoData:
				require.NoError(t, err)
				require.True(t, res.NoData)
				require.Equal(t, tt.wantText, res.Text)
			default:
				require.NoError(t, err)
				require.JSONEq(t, tt.want, string(res.Structured))
				require.Equal(t, tt.wantEvidence, res.Evidence)
				require.False(t, res.Truncated)
			}
		})
	}
}

func TestSourceCallInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{name: "a kind the tool does not read", tool: toolGetService, input: `{"service":"group:payments"}`, want: "the kind must be one of component, resource, api"},
		{name: "a name that climbs out", tool: toolGetService, input: `{"service":"../secrets"}`, want: "is not a valid catalog namespace"},
		{name: "an empty name", tool: toolGetOwner, input: `{"owner":""}`, want: "is not a catalog name or entity ref"},
		{name: "a bad namespace", tool: toolGetSystem, input: `{"system":"system:a b/shop"}`, want: "is not a valid catalog namespace"},
		{name: "a name that is too long", tool: toolGetService, input: `{"service":"` + strings.Repeat("a", maxNameLen+1) + `"}`, want: "is not a catalog name or entity ref"},
		{name: "input that does not decode", tool: toolGetService, input: `{"service":7}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, registry.NewMockSecrets(gomock.NewController(t)), 0)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches Backstage")
		})
	}
}

func TestSourceCallCutsLongText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "long descriptions and lists are cut and the result says so"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tags := make([]string, maxListLen+5)
			for i := range tags {
				tags[i] = "tag"
			}

			body, err := json.Marshal(map[string]any{
				"kind":     "Component",
				"metadata": map[string]any{"name": "checkout-api", "description": strings.Repeat("é", maxDescriptionLen), "tags": tags},
			})
			require.NoError(t, err)

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, byName+"component/default/checkout-api", fakehttp.Response{Headers: jsonHeaders, Body: body})
			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t), 0)

			res, err := s.Call(t.Context(), source.Call{Tool: toolGetService, Input: json.RawMessage(`{"service":"checkout-api"}`)})
			require.NoError(t, err)
			require.True(t, res.Truncated)

			var got serviceResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.LessOrEqual(t, len(got.Description), maxDescriptionLen)
			require.Len(t, got.Tags, maxListLen)
		})
	}
}

func TestSourceToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secret  []byte
		err     error
		wantErr string
	}{
		{name: "an empty token secret", secret: []byte(" \n"), wantErr: "the token secret is empty"},
		{name: "a token secret that cannot be read", err: errors.New("no such file"), wantErr: "no such file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return(tt.secret, tt.err)

			api := fakehttp.New(t)
			s := newTestSource(t, api, secrets, 0)

			_, err := s.Call(t.Context(), source.Call{Tool: toolGetService, Input: json.RawMessage(`{"service":"checkout-api"}`)})
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, api.Requests(), "no request is sent without a token")
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "delete_entity"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t), registry.NewMockSecrets(gomock.NewController(t)), 0)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         Config
		withSecrets bool
		wantErr     string
	}{
		{
			name:    "without a secrets resolver",
			cfg:     Config{BaseURL: "https://backstage.example.com", TokenSecret: testTokenRef},
			wantErr: errNoSecrets.Error(),
		},
		{
			name:        "with settings that do not check",
			cfg:         Config{BaseURL: "https://backstage.example.com", TokenSecret: testTokenRef, Settings: source.Settings{Concurrency: -1}},
			withSecrets: true,
			wantErr:     "concurrency: must not be negative",
		},
		{
			name:        "with the default HTTP client and logger",
			cfg:         Config{BaseURL: "https://backstage.example.com", TokenSecret: testTokenRef},
			withSecrets: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var deps registry.Deps
			if tt.withSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "backstage", tt.cfg, deps)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, s)
			require.NoError(t, s.Close())
		})
	}
}
