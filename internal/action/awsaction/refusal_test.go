package awsaction

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestRefuse(t *testing.T) {
	t.Parallel()

	err := Refuse("function %s is not allowed", "checkout")

	require.EqualError(t, err, "function checkout is not allowed")
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.NotErrorIs(t, err, action.ErrConflict)
}
