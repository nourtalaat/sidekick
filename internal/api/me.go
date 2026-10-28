package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/redact"
)

// server implements the API operations.
type server struct {
	tokens   TokenManager
	cfg      *config.Config
	rules    RuntimeRules
	redactor *redact.Redactor
	pool     *pgxpool.Pool
	audit    *audit.Writer
	hub      *Hub
	log      *slog.Logger

	streamHeartbeat time.Duration
	streamLifetime  time.Duration
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
