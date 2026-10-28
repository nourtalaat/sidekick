package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// tokenByTokenDetail answers an API token that tries to create or revoke
// API tokens.
const tokenByTokenDetail = "Only a signed-in user can create or revoke API tokens."

// ListTokens returns every API token, without the tokens themselves.
func (s *server) ListTokens(ctx context.Context, _ apigen.ListTokensRequestObject) (apigen.ListTokensResponseObject, error) {
	rows, err := s.tokens.ListTokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}

	list := apigen.ListTokens200JSONResponse{Tokens: make([]apigen.Token, 0, len(rows))}
	for _, row := range rows {
		list.Tokens = append(list.Tokens, tokenOf(row))
	}

	return list, nil
}

// CreateToken creates an API token for the signed-in caller, and returns
// it, the only time it is shown.
func (s *server) CreateToken(ctx context.Context, req apigen.CreateTokenRequestObject) (apigen.CreateTokenResponseObject, error) {
	p, _ := auth.PrincipalFrom(ctx)

	secret, row, err := s.tokens.CreateToken(ctx, p, auth.NewToken{
		Name: req.Body.Name, Role: config.RoleName(req.Body.Role).Role(), ExpiresAt: req.Body.ExpiresAt,
	})

	switch {
	case errors.Is(err, auth.ErrTokenByToken):
		return nil, problemf(http.StatusForbidden, tokenByTokenDetail)
	case errors.Is(err, auth.ErrTokenRole):
		return nil, problemf(http.StatusBadRequest, "The role must be viewer, responder, approver or admin, and at most your own.")
	case errors.Is(err, auth.ErrTokenExpiry):
		return nil, problemf(http.StatusBadRequest, "The expiry must be in the future and at most a year away.")
	case errors.Is(err, auth.ErrTokenName):
		return nil, problemf(http.StatusBadRequest, "The name must be 1 to %d characters.", auth.MaxTokenNameLength)
	case err != nil:
		return nil, fmt.Errorf("create API token: %w", err)
	}

	return apigen.CreateToken201JSONResponse{Token: tokenOf(row), Secret: secret}, nil
}

// RevokeToken revokes an API token for the signed-in caller.
func (s *server) RevokeToken(ctx context.Context, req apigen.RevokeTokenRequestObject) (apigen.RevokeTokenResponseObject, error) {
	p, _ := auth.PrincipalFrom(ctx)

	_, err := s.tokens.RevokeToken(ctx, p, req.Id)

	switch {
	case errors.Is(err, auth.ErrTokenByToken):
		return nil, problemf(http.StatusForbidden, tokenByTokenDetail)
	case errors.Is(err, auth.ErrNoToken):
		return nil, problemf(http.StatusNotFound, "No API token has this ID, or it is already revoked.")
	case err != nil:
		return nil, fmt.Errorf("revoke API token: %w", err)
	}

	return apigen.RevokeToken204Response{}, nil
}

// tokenOf is the API's view of a token row: never its hash.
func tokenOf(row db.ApiToken) apigen.Token {
	return apigen.Token{
		Id: row.ID, Name: row.Name, Role: apigen.Role(row.Role), CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt,
	}
}
