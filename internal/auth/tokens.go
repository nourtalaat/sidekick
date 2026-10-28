package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// TokenPrefix starts every API token, so secret scanners can spot one
// that leaks.
const TokenPrefix = "skt_"

// MaxTokenLifetime is the longest an API token may last.
const MaxTokenLifetime = 366 * 24 * time.Hour

// MaxTokenNameLength is the longest name, in characters, a token may have.
const MaxTokenNameLength = 100

// tokenTouchInterval is how stale last_used_at may get before a request
// with the token writes it again.
const tokenTouchInterval = time.Minute

// bearerPrefix starts the Authorization header that carries a token.
const bearerPrefix = "bearer "

// The audit actions of API tokens.
const (
	ActionTokenCreated = "token.created"
	ActionTokenRevoked = "token.revoked"
)

var (
	// ErrTokenByToken means an API token tried to create or revoke API
	// tokens, which only a signed-in user may do.
	ErrTokenByToken = errors.New("only a signed-in user can create or revoke API tokens")
	// ErrTokenRole means a token's role is not a role, or is above its
	// creator's.
	ErrTokenRole = errors.New("the role is not a role, or is above your own")
	// ErrTokenExpiry means a token's expiry is in the past, or more than
	// MaxTokenLifetime away.
	ErrTokenExpiry = errors.New("the expiry must be in the future and at most a year away")
	// ErrTokenName means a token's name is empty or too long.
	ErrTokenName = errors.New("the name must be 1 to 100 characters")
	// ErrNoToken means there is no token with an ID, or it is already
	// revoked.
	ErrNoToken = errors.New("no such API token, or it is already revoked")
)

// NewToken is what an admin asks for when creating an API token.
type NewToken struct {
	Name      string
	Role      roles.Role
	ExpiresAt time.Time
}

// CreateToken creates an API token for by, a signed-in user, and writes it
// to the audit log. It returns the token, which is never stored or shown
// again, and its row.
func (s *Service) CreateToken(ctx context.Context, by Principal, nt NewToken) (string, db.ApiToken, error) {
	now := s.now()

	switch name := strings.TrimSpace(nt.Name); {
	case by.Kind != KindUser:
		return "", db.ApiToken{}, ErrTokenByToken
	case name == "" || utf8.RuneCountInString(name) > MaxTokenNameLength:
		return "", db.ApiToken{}, ErrTokenName
	case config.RoleNameOf(nt.Role) == "" || !by.Role.Has(nt.Role):
		return "", db.ApiToken{}, ErrTokenRole
	case !nt.ExpiresAt.After(now) || nt.ExpiresAt.After(now.Add(MaxTokenLifetime)):
		return "", db.ApiToken{}, ErrTokenExpiry
	}

	token := TokenPrefix + randomToken(tokenBytes)
	role := string(config.RoleNameOf(nt.Role))

	var row db.ApiToken

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error

		row, err = db.New(tx).CreateAPIToken(ctx, db.CreateAPITokenParams{
			ID: ids.New(), Name: strings.TrimSpace(nt.Name), TokenHash: hashToken(token), Role: role,
			CreatedBy: by.ID, CreatedAt: now, ExpiresAt: nt.ExpiresAt,
		})
		if err != nil {
			return fmt.Errorf("store the token: %w", err)
		}

		return s.auditToken(ctx, tx, by, ActionTokenCreated, row, nil, tokenDetails(row))
	})
	if err != nil {
		return "", db.ApiToken{}, fmt.Errorf("create API token: %w", err)
	}

	return token, row, nil
}

// ListTokens returns every API token, revoked and expired ones included,
// newest first.
func (s *Service) ListTokens(ctx context.Context) ([]db.ApiToken, error) {
	rows, err := db.New(s.pool).ListAPITokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}

	return rows, nil
}

// RevokeToken revokes the API token id for by, a signed-in user, and
// writes it to the audit log. It returns ErrNoToken when there is no such
// token or it is already revoked.
func (s *Service) RevokeToken(ctx context.Context, by Principal, id uuid.UUID) (db.ApiToken, error) {
	if by.Kind != KindUser {
		return db.ApiToken{}, ErrTokenByToken
	}

	var row db.ApiToken

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error

		row, err = db.New(tx).RevokeAPIToken(ctx, db.RevokeAPITokenParams{ID: id, Now: s.now()})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoToken
		}

		if err != nil {
			return fmt.Errorf("revoke the token: %w", err)
		}

		return s.auditToken(ctx, tx, by, ActionTokenRevoked, row, tokenDetails(row), nil)
	})
	if err != nil {
		return db.ApiToken{}, fmt.Errorf("revoke API token %s: %w", id, err)
	}

	return row, nil
}

// tokenPrincipal returns the API token in the Authorization header value
// h as the caller.
func (s *Service) tokenPrincipal(r *http.Request, h string) (Principal, error) {
	if len(h) <= len(bearerPrefix) || !strings.EqualFold(h[:len(bearerPrefix)], bearerPrefix) {
		return Principal{}, fmt.Errorf("an Authorization header that is not a bearer token: %w", ErrUnauthenticated)
	}

	token := strings.TrimSpace(h[len(bearerPrefix):])
	if !strings.HasPrefix(token, TokenPrefix) {
		return Principal{}, fmt.Errorf("a bearer token that is not a Sidekick API token: %w", ErrUnauthenticated)
	}

	ctx := r.Context()
	now := s.now()
	q := db.New(s.pool)

	row, err := q.GetActiveAPIToken(ctx, db.GetActiveAPITokenParams{TokenHash: hashToken(token), Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, fmt.Errorf("an unknown, revoked or expired API token: %w", ErrUnauthenticated)
	}

	if err != nil {
		return Principal{}, fmt.Errorf("read the API token: %w", err)
	}

	role := config.RoleName(row.Role).Role()
	if role == 0 {
		return Principal{}, fmt.Errorf("API token %s: %w", row.ID, errUnknownRole)
	}

	if err := q.TouchAPIToken(ctx, db.TouchAPITokenParams{ID: row.ID, Now: now, LastUsedBefore: now.Add(-tokenTouchInterval)}); err != nil {
		s.log.WarnContext(ctx, "could not record when an API token was used", "token", row.ID, "error", err)
	}

	return Principal{Kind: KindToken, ID: row.ID, Name: row.Name, Role: role}, nil
}

// auditToken writes a token change to the audit log through tx.
func (s *Service) auditToken(ctx context.Context, tx pgx.Tx, by Principal, action string, row db.ApiToken, before, after any) error {
	err := s.audit.Write(ctx, tx, audit.Entry{
		ActorKind: audit.ActorUser, ActorID: by.ID.String(), Action: action,
		TargetKind: "api_token", TargetID: row.ID.String(), Old: before, New: after,
	})
	if err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}

	return nil
}

// inTx runs fn in a transaction and commits it when fn succeeds.
func (s *Service) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return fmt.Errorf("run the transaction: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// tokenDetails is what the audit log keeps of a token: never the token.
func tokenDetails(row db.ApiToken) map[string]any {
	return map[string]any{"name": row.Name, "role": row.Role, "expiresAt": row.ExpiresAt}
}

// hashToken returns the hash of an API token, which is all the database
// keeps of it.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))

	return sum[:]
}
