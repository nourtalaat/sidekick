package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

var errDatabase = errors.New("database down")

func TestStrategyError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantFields []problem.FieldError
	}{
		{
			name: "a reason and body problems",
			err: fmt.Errorf("check: %w", &strategystore.InvalidError{
				Reason: "a reason is required",
				Body: []strategy.FieldProblem{
					{Path: "match.textPattern", Line: 4, Message: "invalid regular expression"},
					{Path: "name", Message: "required"},
					{Message: "line 2: did not find expected node content"},
				},
			}),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "validation",
			wantFields: []problem.FieldError{
				{Field: "reason", Message: "a reason is required"},
				{Field: "body.match.textPattern", Message: "invalid regular expression (line 4)"},
				{Field: "body.name", Message: "required"},
				{Field: "body", Message: "line 2: did not find expected node content"},
			},
		},
		{
			name:       "the same body",
			err:        fmt.Errorf("save: %w", strategystore.ErrUnchanged),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "unchanged",
			wantFields: []problem.FieldError{{Field: "body", Message: "The strategy is the same as its current version."}},
		},
		{name: "a stale base version", err: fmt.Errorf("save: %w", strategystore.ErrStale), wantStatus: http.StatusConflict, wantCode: "stale"},
		{name: "two first saves", err: fmt.Errorf("save: %w", strategystore.ErrConflict), wantStatus: http.StatusConflict, wantCode: "conflict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := errors.AsType[*Error](strategyError(tt.err, "checkout-latency"))
			require.True(t, ok)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.wantCode, got.Code)
			require.Equal(t, tt.wantFields, got.Fields)
		})
	}

	t.Run("an error it does not know", func(t *testing.T) {
		t.Parallel()

		err := strategyError(errDatabase, "checkout-latency")
		require.ErrorIs(t, err, errDatabase)

		_, isProblem := errors.AsType[*Error](err)
		require.False(t, isProblem, "it answers 500")
	})
}
