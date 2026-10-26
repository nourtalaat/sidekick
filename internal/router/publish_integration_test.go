//go:build integration

package router

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestRoutingOpensTheRouteOutputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		event     uuid.UUID
		wantSinks []string
	}{
		{name: "a route with two outputs", event: paymentsEvent, wantSinks: []string{"slack-thread", "callback"}},
		{name: "a route without outputs", event: otherEvent, wantSinks: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, tt.event)))

			inv := h.investigations(t)[0]

			states := collect[db.PublishState](t, h.pool, "SELECT * FROM publish_states")
			require.Len(t, states, 1)
			require.Equal(t, inv.ID, states[0].InvestigationID)
			require.Equal(t, tt.wantSinks, states[0].Sinks)
			require.Equal(t, publish.SeqQueued, states[0].Seq)
			require.Equal(t, string(output.PhaseQueued), states[0].Phase)

			jobs := collect[publishJob](t, h.pool, "SELECT unique_key, payload FROM jobs WHERE kind = 'publish' ORDER BY id")
			require.Len(t, jobs, len(tt.wantSinks))

			for i, sink := range tt.wantSinks {
				require.Equal(t, publish.UniqueKey(inv.ID, sink), *jobs[i].UniqueKey)

				var p publish.Payload
				require.NoError(t, json.Unmarshal(jobs[i].Payload, &p))
				require.Equal(t, publish.Payload{InvestigationID: inv.ID, Sink: sink}, p)
			}
		})
	}
}

// TestTheFirstPublishStoresTheOutputRef routes an event, then runs its
// first publish job with the Slack output against a fake Slack, and checks
// that the message's ref is stored.
func TestTheFirstPublishStoresTheOutputRef(t *testing.T) {
	t.Parallel()

	h := setup(t)
	require.NoError(t, h.router.Handle(t.Context(), h.routeJob(t, paymentsEvent)))

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, "/api/chat.postMessage", fakehttp.Response{BodyFile: "fixture/slack-posted.json"})
	api.ExpectAllCalled()

	sink, err := slack.New(t.Context(), "slack-thread", slack.Config{Channel: "#alerts", APIURL: api.URL() + "/api/"}, registry.Deps{
		Secrets:       fakeSecrets{},
		HTTP:          api.Client(),
		SlackBotToken: registry.SecretRef{Name: "slack", Key: "botToken"},
	})
	require.NoError(t, err)

	handler := publish.New(publish.Options{Pool: h.pool, Sinks: map[string]output.Sink{"slack-thread": sink}})

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindPublish}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NoError(t, handler.Handle(t.Context(), jobs[0]))

	var sent struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(api.Requests()[0].Body, &sent))
	require.Equal(t, "#alerts", sent.Channel)
	require.Equal(t, "Looking into it: High p99 latency", sent.Text)

	inv := h.investigations(t)[0]
	refs := collect[db.OutputRef](t, h.pool, "SELECT * FROM output_refs")
	require.Len(t, refs, 1)
	require.Equal(t, inv.ID, refs[0].InvestigationID)
	require.Equal(t, "slack-thread", refs[0].Sink)
	require.Equal(t, "C0123ALERTS:1793023321.000100", refs[0].External)
	require.Equal(t, publish.SeqQueued, refs[0].LastSeq)
}

// publishJob is a publish job as the tests check it.
type publishJob struct {
	UniqueKey *string
	Payload   json.RawMessage
}

// fakeSecrets resolves every reference to a fake value.
type fakeSecrets struct{}

func (fakeSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte("fake-value"), nil
}
