package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/roles"
)

// RoleExtension is the key of the spec's operations that names the lowest
// role allowed to call the operation.
const RoleExtension = "x-sidekick-role"

// httpMethods are the keys of an OpenAPI path item that hold operations.
var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// errNoOperations means the spec has no operations at all.
var errNoOperations = errors.New("the spec has no operations")

// spec is the part of an OpenAPI document that OperationRoles reads.
type spec struct {
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

// operation is the part of an OpenAPI operation that OperationRoles reads.
type operation struct {
	OperationID string          `yaml:"operationId"`
	Role        config.RoleName `yaml:"x-sidekick-role"`
}

// OperationRoles returns the role each operation of the OpenAPI document
// doc needs, by operation ID. It fails when an operation has no ID, or no
// known role in RoleExtension.
func OperationRoles(doc []byte) (map[string]roles.Role, error) {
	var s spec
	if err := yaml.Unmarshal(doc, &s); err != nil {
		return nil, fmt.Errorf("decode the spec: %w", err)
	}

	need := map[string]roles.Role{}

	var problems []error

	for _, path := range slices.Sorted(maps.Keys(s.Paths)) {
		for _, method := range httpMethods {
			node, ok := s.Paths[path][method]
			if !ok {
				continue
			}

			var op operation
			if err := node.Decode(&op); err != nil {
				return nil, fmt.Errorf("decode %s %s: %w", method, path, err)
			}

			role := op.Role.Role()

			switch {
			case op.OperationID == "":
				problems = append(problems, fmt.Errorf("%s %s has no operationId", method, path))
			case role == 0:
				problems = append(problems, fmt.Errorf("%s %s (%s) has no known role in %s: %q", method, path, op.OperationID, RoleExtension, op.Role))
			default:
				need[op.OperationID] = role
			}
		}
	}

	if err := errors.Join(problems...); err != nil {
		return nil, err
	}

	if len(need) == 0 {
		return nil, errNoOperations
	}

	return need, nil
}

// requireRoles returns the middleware that lets a caller run an operation
// only when its role includes the one need gives the operation. An
// operation need does not name is refused to everyone.
func requireRoles(need map[string]roles.Role) apigen.StrictMiddlewareFunc {
	return func(next apigen.StrictHandlerFunc, operationID string) apigen.StrictHandlerFunc {
		role, known := need[operationID]

		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			p, ok := auth.PrincipalFrom(ctx)
			if !known || !ok || !p.Role.Has(role) {
				problem.Write(w, r, http.StatusForbidden, fmt.Sprintf("This needs the %s role.", roleName(role)))

				return nil, nil
			}

			return next(ctx, w, r, request)
		}
	}
}

// roleName names role for a message, including the case where the
// operation has no role.
func roleName(role roles.Role) string {
	if name := config.RoleNameOf(role); name != "" {
		return string(name)
	}

	return "unknown"
}
