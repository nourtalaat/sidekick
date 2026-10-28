//go:build integration

package memory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

// The entries in fixture/memory.sql.
const (
	entryDeploy    = "0192d3a4-0000-7000-8000-0000000000e1"
	entryFlapping  = "0192d3a4-0000-7000-8000-0000000000e2"
	entryBatchSlow = "0192d3a4-0000-7000-8000-0000000000e3"
)

func TestSearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		// want are the entries found, in any order; none means no data.
		want []string
	}{
		{name: "every word must match", input: `{"query":"deploy slow query"}`, want: []string{entryDeploy}},
		{name: "one service", input: `{"query":"latency","service":"orders-api"}`, want: []string{entryBatchSlow}},
		{name: "one verdict", input: `{"query":"latency","verdict":"flapping"}`, want: []string{entryFlapping}},
		{
			name:  "disabled entries are left out",
			input: `{"query":"latency","service":"checkout-api"}`,
			want:  []string{entryFlapping, entryDeploy},
		},
		{name: "empty filters are ignored", input: `{"query":"export","service":"","verdict":""}`, want: []string{entryBatchSlow}},
		{name: "only a disabled entry matches", input: `{"query":"region"}`},
		{name: "nothing matches", input: `{"query":"certificate expired"}`},
	}

	src := setup(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := src.Call(t.Context(), source.Call{ID: "c1", Tool: toolSearch, Input: json.RawMessage(tt.input)})
			require.NoError(t, err)

			if len(tt.want) == 0 {
				require.True(t, res.NoData)
				require.Equal(t, noMatchText, res.Text)

				return
			}

			var got searchResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))

			var ids, evidence []string
			for _, e := range got.Entries {
				ids = append(ids, e.InvestigationID)
			}

			for _, e := range res.Evidence {
				require.Equal(t, source.EvidenceIncident, e.Kind)
				evidence = append(evidence, e.Title)
			}

			require.ElementsMatch(t, tt.want, ids)
			require.Len(t, evidence, len(ids))
			require.False(t, res.Truncated)
		})
	}
}

func TestSearchResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "an entry with every field",
			input: `{"query":"deploy slow query"}`,
			want: `{"entries":[{"investigationId":"0192d3a4-0000-7000-8000-0000000000e1","confirmedVerdict":"true_positive",
				"service":"checkout-api","alertName":"CheckoutLatencyP99","summary":"p99 latency rose after a deploy.",
				"confirmedCause":"Deploy 4f2c1a added a slow query.","confirmedAt":"2026-10-20T10:00:00Z"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := setup(t)

			res, err := src.Call(t.Context(), source.Call{ID: "c1", Tool: toolSearch, Input: json.RawMessage(tt.input)})
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(res.Structured))
			require.Equal(t, []source.Evidence{{
				Kind:  source.EvidenceIncident,
				Title: "Confirmed investigation " + entryDeploy,
				At:    time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
			}}, res.Evidence)
		})
	}
}

func TestServiceNotes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		service string
		// want is the result, empty for no data.
		want string
	}{
		{
			name:    "a service with notes",
			service: "checkout-api",
			want: `{"service":"checkout-api","notes":"Latency alerts during the 02:00 batch run are expected.",
				"updatedAt":"2026-10-01T09:00:00Z"}`,
		},
		{name: "a service without notes", service: "payments-api"},
	}

	src := setup(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input, err := json.Marshal(serviceNotesInput{Service: tt.service})
			require.NoError(t, err)

			res, err := src.Call(t.Context(), source.Call{ID: "c1", Tool: toolServiceNotes, Input: input})
			require.NoError(t, err)

			if tt.want == "" {
				require.True(t, res.NoData)
				require.Equal(t, noNotesText, res.Text)

				return
			}

			require.JSONEq(t, tt.want, string(res.Structured))
		})
	}
}
