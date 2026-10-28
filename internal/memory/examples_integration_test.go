//go:build integration

package memory

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/prompt"
)

// The entries in fixture/entries.sql.
var (
	entrySameFingerprintOld = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e1")
	entrySameFingerprintNew = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e2")
	entrySameServiceAlert   = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e4")
	entryLatencyWords       = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e6")
	entryDiskWords          = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e7")
)

// checkoutLatency is an alert like the checkout entries of the fixture.
var checkoutLatency = Match{
	Investigation: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000ff"),
	Fingerprint:   "fp-checkout",
	Service:       "checkout-api",
	AlertName:     "CheckoutLatencyP99",
	Text:          "CheckoutLatencyP99 checkout-api p99 latency above 2s p99 is 2.6 s.",
}

// chosen is an example as the tests compare it: which entry, and why.
type chosen struct {
	id    uuid.UUID
	match prompt.MemoryMatch
}

func TestExamples(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		match Match
		limit int
		want  []chosen
	}{
		{
			name:  "every tier in order, each entry once",
			match: checkoutLatency,
			limit: MaxExamples,
			want: []chosen{
				{entrySameFingerprintNew, prompt.MatchFingerprint},
				{entrySameFingerprintOld, prompt.MatchFingerprint},
				{entrySameServiceAlert, prompt.MatchServiceAlert},
				{entryLatencyWords, prompt.MatchText},
			},
		},
		{
			name:  "the limit stops at the first tiers",
			match: checkoutLatency,
			limit: DefaultExamples,
			want: []chosen{
				{entrySameFingerprintNew, prompt.MatchFingerprint},
				{entrySameFingerprintOld, prompt.MatchFingerprint},
				{entrySameServiceAlert, prompt.MatchServiceAlert},
			},
		},
		{
			name:  "the same fingerprint, newest first",
			match: checkoutLatency,
			limit: 1,
			want:  []chosen{{entrySameFingerprintNew, prompt.MatchFingerprint}},
		},
		{
			name:  "the same service and alert name when no fingerprint matches",
			match: Match{Fingerprint: "fp-new", Service: "checkout-api", AlertName: "CheckoutLatencyP99", Text: checkoutLatency.Text},
			limit: MaxExamples,
			want: []chosen{
				{entrySameFingerprintNew, prompt.MatchServiceAlert},
				{entrySameServiceAlert, prompt.MatchServiceAlert},
				{entrySameFingerprintOld, prompt.MatchServiceAlert},
				{entryLatencyWords, prompt.MatchText},
			},
		},
		{
			name:  "words in common when nothing else matches",
			match: Match{Fingerprint: "fp-new", Service: "billing-api", AlertName: "BillingDisk", Text: "disk full of logs"},
			limit: MaxExamples,
			want:  []chosen{{entryDiskWords, prompt.MatchText}},
		},
		{
			name:  "the investigation's own entry is left out",
			match: Match{Investigation: entrySameFingerprintNew, Fingerprint: "fp-checkout", Service: "checkout-api", AlertName: "CheckoutLatencyP99", Text: checkoutLatency.Text},
			limit: MaxExamples,
			want: []chosen{
				{entrySameFingerprintOld, prompt.MatchFingerprint},
				{entrySameServiceAlert, prompt.MatchServiceAlert},
				{entryLatencyWords, prompt.MatchText},
			},
		},
		{
			name:  "disabled entries never match",
			match: Match{Fingerprint: "fp-ledger", Service: "ledger", AlertName: "LedgerLag", Text: "ledger lag while the replica was rebuilt"},
			limit: MaxExamples,
		},
		{
			name:  "no words, service or fingerprint in common",
			match: Match{Fingerprint: "fp-new", Service: "search", AlertName: "IndexStale", Text: "index stale"},
			limit: MaxExamples,
		},
		{
			name:  "a limit of zero shows none",
			match: checkoutLatency,
			limit: 0,
		},
	}

	pool := setup(t, "fixture/entries.sql")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Examples(t.Context(), pool, tt.match, tt.limit)
			require.NoError(t, err)

			var ids []chosen
			for _, ex := range got {
				ids = append(ids, chosen{uuid.MustParse(ex.InvestigationID), ex.Match})
			}

			require.Equal(t, tt.want, ids)
		})
	}
}

func TestExamplesFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want prompt.Example
	}{
		{
			name: "every field of the entry",
			want: prompt.Example{
				InvestigationID: entrySameFingerprintOld.String(),
				Match:           prompt.MatchFingerprint,
				Verdict:         "true_positive",
				Service:         "checkout-api",
				AlertName:       "CheckoutLatencyP99",
				Summary:         "p99 latency rose after a deploy.",
				Cause:           "Deploy 4f2c1a added a slow query.",
				ConfirmedAt:     time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t, "fixture/entries.sql")

			m := checkoutLatency
			m.Investigation = entrySameFingerprintNew

			got, err := Examples(t.Context(), pool, m, 1)
			require.NoError(t, err)
			require.Len(t, got, 1)

			got[0].ConfirmedAt = got[0].ConfirmedAt.UTC()
			require.Equal(t, tt.want, got[0])
		})
	}
}
