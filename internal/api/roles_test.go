package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	apispec "github.com/nourtalaat/sidekick/api"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/roles"
)

// TestEveryOperationHasARole walks api/openapi.yaml on its own, without
// OperationRoles, and fails for any operation without a known role.
func TestEveryOperationHasARole(t *testing.T) {
	t.Parallel()

	var doc struct {
		Paths map[string]map[string]map[string]any `yaml:"paths"`
	}

	require.NoError(t, yaml.Unmarshal(apispec.Spec, &doc))
	require.NotEmpty(t, doc.Paths)

	known := []string{string(config.RoleViewer), string(config.RoleResponder), string(config.RoleApprover), string(config.RoleAdmin)}

	for path, item := range doc.Paths {
		for method, op := range item {
			if !slices.Contains(httpMethods, method) {
				continue
			}

			role, _ := op[RoleExtension].(string)
			require.Contains(t, known, role, "%s %s needs %s set to one of %v", method, path, RoleExtension, known)
		}
	}
}

func TestOperationRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		doc     string
		want    map[string]roles.Role
		wantErr string
	}{
		{
			name: "every operation with a role",
			doc: `
paths:
  /me:
    parameters: []
    get: {operationId: GetMe, x-sidekick-role: viewer}
  /jobs/{id}/retry:
    post: {operationId: RetryJob, x-sidekick-role: admin}
`,
			want: map[string]roles.Role{"GetMe": roles.Viewer, "RetryJob": roles.Admin},
		},
		{
			name:    "an operation without a role",
			doc:     "paths:\n  /me:\n    get: {operationId: GetMe}\n",
			wantErr: `get /me (GetMe) has no known role in x-sidekick-role: ""`,
		},
		{
			name:    "an operation with an unknown role",
			doc:     "paths:\n  /me:\n    get: {operationId: GetMe, x-sidekick-role: owner}\n",
			wantErr: `get /me (GetMe) has no known role in x-sidekick-role: "owner"`,
		},
		{
			name:    "an operation without an ID",
			doc:     "paths:\n  /me:\n    get: {x-sidekick-role: viewer}\n",
			wantErr: "get /me has no operationId",
		},
		{
			name:    "no operations",
			doc:     "paths: {}\n",
			wantErr: "the spec has no operations",
		},
		{
			name:    "not YAML",
			doc:     "paths: [",
			wantErr: "decode the spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := OperationRoles([]byte(tt.doc))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOperationRolesOfTheSpec(t *testing.T) {
	t.Parallel()

	need, err := OperationRoles(apispec.Spec)
	require.NoError(t, err)
	require.Equal(t, roles.Viewer, need["GetMe"])
}

func TestRequireRoles(t *testing.T) {
	t.Parallel()

	need := map[string]roles.Role{"RetryJob": roles.Admin}

	tests := []struct {
		name       string
		operation  string
		principal  *auth.Principal
		wantStatus int
	}{
		{name: "the role the operation needs", operation: "RetryJob", principal: &auth.Principal{Role: roles.Admin}, wantStatus: http.StatusNoContent},
		{name: "a lower role", operation: "RetryJob", principal: &auth.Principal{Role: roles.Approver}, wantStatus: http.StatusForbidden},
		{name: "no caller", operation: "RetryJob", wantStatus: http.StatusForbidden},
		{name: "an operation without a role", operation: "Other", principal: &auth.Principal{Role: roles.Admin}, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			next := func(_ context.Context, w http.ResponseWriter, _ *http.Request, _ any) (any, error) {
				w.WriteHeader(http.StatusNoContent)

				return nil, nil
			}

			ctx := t.Context()
			if tt.principal != nil {
				ctx = auth.WithPrincipal(ctx, *tt.principal)
			}

			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/jobs/1/retry", nil)
			rec := httptest.NewRecorder()

			_, err := requireRoles(need)(next, tt.operation)(ctx, rec, req, nil)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
