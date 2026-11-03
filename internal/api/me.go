package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	actionproposals "github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/scenario"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// themeDetail is the answer to a theme that is not system, light or dark.
const themeDetail = "The theme must be system, light or dark."

// server implements the API operations.
type server struct {
	tokens     TokenManager
	cfg        *config.Config
	rules      RuleStore
	strategies StrategyStore
	// proposalReviewer reviews strategy proposals.
	proposalReviewer ProposalReviewer
	// strategyBase holds the built-in and file strategies.
	strategyBase *strategy.Set
	redactor     *redact.Redactor
	pool         *pgxpool.Pool
	audit        *audit.Writer
	hub          *Hub
	log          *slog.Logger

	feedbackMetrics *feedback.Metrics
	memory          memory.SyncOptions
	proposals       proposals.Trigger
	draftLessons    bool
	lessons         LessonStore
	memoryEntries   MemoryStore
	// stopper stops watchers and watchers records the changes that
	// resolving a group makes to them; now is the clock.
	stopper  *watch.Stopper
	watchers *watch.Recorder
	// actions decides the action proposals.
	actions *actionproposals.Service
	now     func() time.Time
	// scenarios exports, edits and reviews benchmark scenarios.
	scenarios *scenario.Service

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

	if p.Kind == auth.KindUser && p.Theme != "" {
		theme := apigen.Theme(p.Theme)
		me.Theme = &theme
	}

	return me, nil
}

// SetMyTheme saves the theme the signed-in user chose. It is a display
// preference, so it writes no audit row.
func (s *server) SetMyTheme(ctx context.Context, req apigen.SetMyThemeRequestObject) (apigen.SetMyThemeResponseObject, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return nil, problemf(http.StatusUnauthorized, "Sign in, or send an API token.")
	}

	if p.Kind == auth.KindToken {
		return nil, problemf(http.StatusForbidden, "API tokens have no theme.")
	}

	if req.Body == nil || !req.Body.Theme.Valid() {
		return nil, &Error{
			Status: http.StatusUnprocessableEntity, Code: codeValidation, Detail: themeDetail,
			Fields: []problem.FieldError{{Field: "theme", Message: themeDetail}},
		}
	}

	theme := string(req.Body.Theme)
	if err := db.New(s.pool).SetUserTheme(ctx, db.SetUserThemeParams{ID: p.ID, Theme: &theme}); err != nil {
		return nil, fmt.Errorf("save the theme of user %s: %w", p.ID, err)
	}

	return apigen.SetMyTheme204Response{}, nil
}
