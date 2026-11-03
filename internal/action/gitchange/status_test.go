package gitchange

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// fakeReporter is an action that can report the state of its pull requests.
type fakeReporter struct {
	action.Action
}

func (fakeReporter) PullRequestState(context.Context, json.RawMessage, string) (string, *Status, error) {
	return "", nil, nil
}

func (fakeReporter) CountCheck(string) {}

func TestStatusCheckerEnabled(t *testing.T) {
	t.Parallel()

	enabled := NewStatusChecker(StatusOptions{Actions: map[string]action.Action{
		"b-prs":  fakeReporter{},
		"a-prs":  fakeReporter{},
		"a-note": struct{ action.Action }{},
	}})
	require.True(t, enabled.Enabled())
	require.Equal(t, []string{"a-prs", "b-prs"}, enabled.names, "only the actions that report a state, in order")

	none := NewStatusChecker(StatusOptions{Actions: map[string]action.Action{"a-note": struct{ action.Action }{}}})
	require.False(t, none.Enabled())
}
