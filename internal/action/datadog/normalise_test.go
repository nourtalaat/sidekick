package datadog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestNormalise(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "tags are sorted", in: `{"tags": ["b", "a", "c:1"]}`, want: `{"tags": ["a", "b", "c:1"]}`},
		{name: "query and message are trimmed", in: `{"query": "  avg(x) > 1\n", "message": "\tHello @slack-ops  "}`, want: `{"query": "avg(x) > 1", "message": "Hello @slack-ops"}`},
		{name: "a query alert is a metric alert", in: `{"type": "query alert"}`, want: `{"type": "metric alert"}`},
		{name: "a metric alert stays one", in: `{"type": "metric alert"}`, want: `{"type": "metric alert"}`},
		{name: "another type stays", in: `{"type": "service check"}`, want: `{"type": "service check"}`},
		{name: "the name and the options are left as they are", in: `{"name": " Spaced ", "options": {"thresholds": {"critical": 3.0}}}`, want: `{"name": " Spaced ", "options": {"thresholds": {"critical": 3.0}}}`},
		{name: "tags that are no list are left", in: `{"tags": "a"}`, want: `{"tags": "a"}`},
		{name: "a message that is no text is left", in: `{"message": 3}`, want: `{"message": 3}`},
		{name: "a missing field stays missing", in: `{}`, want: `{}`},
		{name: "a null option is a missing one", in: `{"options": {"no_data_timeframe": null, "notify_no_data": true}}`, want: `{"options": {"notify_no_data": true}}`},
		{name: "a null at the top is left as it is", in: `{"priority": null, "options": {}}`, want: `{"priority": null, "options": {}}`},
		{name: "options that are no object are left", in: `{"options": 3}`, want: `{"options": 3}`},
		{name: "options that are null are left", in: `{"options": null}`, want: `{"options": null}`},
		{name: "a null deeper in the options is left", in: `{"options": {"thresholds": {"warning": null}}}`, want: `{"options": {"thresholds": {"warning": null}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalise(json.RawMessage(tt.in))
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
		})
	}

	_, err := normalise(json.RawMessage(`[`))
	require.Error(t, err)
}

func TestANullOptionIsNotThereForTheChecks(t *testing.T) {
	t.Parallel()

	// A change that removes an option by setting it to null.
	snap := action.Snapshot{
		Before: json.RawMessage(`{"options": {"notify_no_data": true, "no_data_timeframe": 10}}`),
		After:  json.RawMessage(`{"options": {"notify_no_data": false, "no_data_timeframe": null}}`),
	}

	after, err := holdsAfter(json.RawMessage(`{"options": {"notify_no_data": false, "notify_audit": false}}`), snap)
	require.NoError(t, err)
	require.True(t, after, "Datadog read the null back as no option")

	before, err := holdsBefore(json.RawMessage(`{"options": {"notify_no_data": true, "no_data_timeframe": 10}}`), snap)
	require.NoError(t, err)
	require.True(t, before)

	before, err = holdsBefore(json.RawMessage(`{"options": {"notify_no_data": true}}`), snap)
	require.NoError(t, err)
	require.False(t, before, "the timeframe is not back")
}

func TestHoldsAfterIgnoresWhatDatadogAdds(t *testing.T) {
	t.Parallel()

	snap := action.Snapshot{
		Before: json.RawMessage(`{"message": "old", "tags": ["a", "b"], "options": {"thresholds": {"critical": 2}}}`),
		After:  json.RawMessage(`{"message": "new", "tags": ["b", "c"], "options": {"thresholds": {"critical": 3}}}`),
	}

	tests := []struct {
		name       string
		live       string
		wantAfter  bool
		wantBefore bool
	}{
		{
			name:      "as written, normalised, with keys Datadog added",
			live:      `{"message": "new  ", "tags": ["c", "b"], "options": {"thresholds": {"critical": 3.0}, "notify_audit": false, "silenced": {}}}`,
			wantAfter: true,
		},
		{
			name:       "as it was, with keys Datadog added",
			live:       `{"message": "old", "tags": ["b", "a"], "options": {"thresholds": {"critical": 2.0}, "notify_audit": false, "silenced": {}}}`,
			wantBefore: true,
		},
		{name: "one changed field has another value", live: `{"message": "new", "tags": ["b", "c"], "options": {"thresholds": {"critical": 4}}}`},
		{name: "one changed field is back to the old value", live: `{"message": "old", "tags": ["b", "c"], "options": {"thresholds": {"critical": 3}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			after, err := holdsAfter(json.RawMessage(tt.live), snap)
			require.NoError(t, err)
			require.Equal(t, tt.wantAfter, after)

			before, err := holdsBefore(json.RawMessage(tt.live), snap)
			require.NoError(t, err)
			require.Equal(t, tt.wantBefore, before)
		})
	}
}

func TestSameDefinitionIsTheSameAsDatadogReadsIt(t *testing.T) {
	t.Parallel()

	same, err := sameDefinition(
		json.RawMessage(`{"type": "query alert", "tags": ["a", "b"], "query": "q", "options": {"thresholds": {"critical": 3}}}`),
		json.RawMessage(`{"type": "metric alert", "tags": ["b", "a"], "query": " q ", "options": {"thresholds": {"critical": 3.0}}}`))
	require.NoError(t, err)
	require.True(t, same)

	same, err = sameDefinition(json.RawMessage(`{"tags": ["a"]}`), json.RawMessage(`{"tags": ["a", "b"]}`))
	require.NoError(t, err)
	require.False(t, same)

	_, err = sameDefinition(json.RawMessage(`[`), json.RawMessage(`{}`))
	require.Error(t, err)
}
