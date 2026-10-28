//go:build integration

package investigator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// pastCheckoutLatency is a confirmed past investigation of the same alert
// as the fixture's.
const pastCheckoutLatency = `INSERT INTO memory_entries (investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at)
VALUES ('0192d3a4-0000-7000-8000-0000000000e1', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout', 'true_positive',
        'p99 latency rose after a deploy.', 'Deploy 9a8b7c added a slow query.', '2026-10-20 10:00:00+00')`

func TestRunShowsMemory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		examples int
		// want says whether the first request shows the past investigation.
		want bool
	}{
		{name: "a similar confirmed investigation is in the prompt", examples: 3, want: true},
		{name: "memory turned off leaves it out", examples: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{turns: []modeltest.Turn{
				turn(search("c1")),
				turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
			}})
			h.handler.opts.MemoryExamples = tt.examples

			_, err := h.pool.Exec(t.Context(), pastCheckoutLatency)
			require.NoError(t, err)

			require.NoError(t, h.run(t))

			first := h.conn.Requests()[0]

			var texts []string

			for _, p := range first.Messages[0].Parts {
				if text, ok := p.(model.Text); ok {
					texts = append(texts, text.Text)
				}
			}

			prompt := strings.Join(texts, "\n")
			require.Equal(t, tt.want, strings.Contains(prompt, "# Similar past investigations"))
			require.Equal(t, tt.want, strings.Contains(prompt, "Investigation: 0192d3a4-0000-7000-8000-0000000000e1"))
			require.Equal(t, tt.want, strings.Contains(prompt, "Cause: Deploy 9a8b7c added a slow query."))
		})
	}
}
