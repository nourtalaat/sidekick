package gitchange

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestRefuse(t *testing.T) {
	t.Parallel()

	cause := errors.New("path \"a\" is a symlink")
	err := Refuse(cause)

	require.EqualError(t, err, cause.Error())
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorIs(t, err, cause)
	require.NotErrorIs(t, err, action.ErrConflict)
}
