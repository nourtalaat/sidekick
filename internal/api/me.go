package api

import (
	"context"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/config"
)

// server implements the API operations.
type server struct {
	tokens TokenManager
}

var _ apigen.StrictServerInterface = (*server)(nil)

// GetMe returns the caller and its role.
func (s *server) GetMe(ctx context.Context, _ apigen.GetMeRequestObject) (apigen.GetMeResponseObject, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return nil, problemf(http.StatusUnauthorized, "Sign in, or send an API token.")
	}

	me := apigen.GetMe200JSONResponse{
		Kind: apigen.MeKind(p.Kind),
		Id:   p.ID.String(),
		Name: p.Name,
		Role: apigen.Role(config.RoleNameOf(p.Role)),
	}

	if p.Email != "" {
		me.Email = &p.Email
	}

	if p.CSRFToken != "" {
		me.CsrfToken = &p.CSRFToken
	}

	return me, nil
}
