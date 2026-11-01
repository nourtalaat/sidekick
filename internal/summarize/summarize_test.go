package summarize_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/summarize"
)

func TestCallToolWithoutConnector(t *testing.T) {
	t.Parallel()

	caller := summarize.New(summarize.Options{})

	require.False(t, caller.Enabled())

	_, err := caller.CallTool(t.Context(), nil, summarize.Request{Route: "default"})
	require.ErrorIs(t, err, summarize.ErrDisabled)
}
