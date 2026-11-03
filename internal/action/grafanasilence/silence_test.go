package grafanasilence

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// snapshotDoc is a snapshot's After, read back.
type snapshotDoc struct {
	Matchers  []mute.Matcher  `json:"matchers"`
	Duration  string          `json:"duration"`
	CreatedBy string          `json:"createdBy"`
	Comment   string          `json:"comment"`
	FiringNow json.RawMessage `json:"firingNow"`
}

// postedBody is the body of a POST that made a silence.
type postedBody struct {
	Matchers  []mute.Matcher `json:"matchers"`
	StartsAt  time.Time      `json:"startsAt"`
	EndsAt    time.Time      `json:"endsAt"`
	CreatedBy string         `json:"createdBy"`
	Comment   string         `json:"comment"`
}

func decodeAfter(t *testing.T, snap action.Snapshot) snapshotDoc {
	t.Helper()

	var doc snapshotDoc
	require.NoError(t, json.Unmarshal(snap.After, &doc))

	return doc
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	g.setAlerts(
		fakeAlert{Labels: map[string]string{"alertname": "HighLatency", "service": "checkout-api"}, State: "active"},
		fakeAlert{Labels: map[string]string{"alertname": "HighLatency", "service": "checkout-web"}, State: "suppressed"},
	)

	s, _ := newSilencer(t, api, testConfig(api.URL()))
	raw := paramsJSON(t, params{
		Matchers: []mute.Matcher{eq("alertname", "HighLatency"), eq("service", "checkout-api")},
		Duration: "90m",
		Comment:  "Planned\n database   migration",
	})

	snap, err := snapshotter(t, s).Snapshot(t.Context(), raw)
	require.NoError(t, err)

	require.Nil(t, snap.Before, "nothing exists before the silence")
	require.Equal(t, `grafana:silence{alertname="HighLatency",service="checkout-api"}`, snap.Target)

	doc := decodeAfter(t, snap)
	require.Equal(t, "sidekick", doc.CreatedBy)
	require.Equal(t, "1h30m", doc.Duration, "the snapshot holds the duration and no start or end")
	require.Equal(t, "Planned database migration", doc.Comment, "cleaned, and without the marker, which only Execute adds")
	require.Contains(t, string(doc.FiringNow), `"count":2`)
	require.NotContains(t, string(snap.After), "startsAt")

	require.Equal(t, []string{"GET " + apiPath + "/alerts"}, requestPaths(api), "a snapshot reads the alerts and changes nothing")
	require.Empty(t, g.posted())
}

func TestSnapshotPreviewThatCannotBeReadDoesNotRefuseTheProposal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "Grafana is not ready", status: http.StatusServiceUnavailable, want: "Grafana answered 503"},
		{name: "the token cannot read alerts", status: http.StatusForbidden, want: "Grafana answered 403"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, g := newFakeGrafana(t, "")
			g.failWith(0, 0, tt.status, 0)

			s, _ := newSilencer(t, api, testConfig(api.URL()))

			snap, err := snapshotter(t, s).Snapshot(t.Context(), checkoutParams(t))
			require.NoError(t, err)
			require.Contains(t, string(decodeAfter(t, snap).FiringNow), tt.want)
		})
	}
}

func TestSnapshotRefusesWhatTheLimitsDoNotAllow(t *testing.T) {
	t.Parallel()

	inside := []mute.Matcher{eq("alertname", "HighLatency"), eq("service", "checkout-api")}

	many := make([]mute.Matcher, 0, mute.MaxMatchers+1)
	for range mute.MaxMatchers + 1 {
		many = append(many, eq("alertname", "HighLatency"))
	}

	tests := []struct {
		name string
		p    params
		ok   bool
	}{
		{name: "inside the first scope", p: params{Matchers: inside, Duration: "1h", Comment: "work"}, ok: true},
		{name: "inside the second scope alone", p: params{Matchers: []mute.Matcher{eq("grafana_folder", "payments")}, Duration: "1h", Comment: "work"}, ok: true},
		{name: "one label of a scope is missing", p: params{Matchers: []mute.Matcher{eq("alertname", "HighLatency")}, Duration: "1h", Comment: "work"}},
		{name: "a value outside the scope", p: params{Matchers: []mute.Matcher{eq("alertname", "Other"), eq("service", "checkout-api")}, Duration: "1h", Comment: "work"}},
		{
			name: "a regex does not pin a scope label",
			p: params{Matchers: []mute.Matcher{
				eq("alertname", "HighLatency"), {Name: "service", Value: "checkout-.*", IsRegex: true, IsEqual: true},
			}, Duration: "1h", Comment: "work"},
		},
		{name: "the shortest duration", p: params{Matchers: inside, Duration: "5m", Comment: "work"}, ok: true},
		{name: "just below the shortest duration", p: params{Matchers: inside, Duration: "4m59s", Comment: "work"}},
		{name: "the longest duration", p: params{Matchers: inside, Duration: "2h", Comment: "work"}, ok: true},
		{name: "just above the longest duration", p: params{Matchers: inside, Duration: "2h0m1s", Comment: "work"}},
		{name: "no comment", p: params{Matchers: inside, Duration: "1h", Comment: "  "}},
		{name: "too many matchers", p: params{Matchers: many, Duration: "1h", Comment: "work"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, _ := newFakeGrafana(t, "")
			s, _ := newSilencer(t, api, testConfig(api.URL()))

			_, err := snapshotter(t, s).Snapshot(t.Context(), paramsJSON(t, tt.p))

			if tt.ok {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, action.ErrLimit)
			require.Empty(t, api.Requests(), "a refused proposal calls nothing")
		})
	}
}

func TestExecuteMakesTheSilence(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	s, _ := newSilencer(t, api, testConfig(api.URL()))

	before := time.Now().UTC()
	_, res := snapshotAndRun(t, s, checkoutParams(t))
	after := time.Now().UTC()

	bodies := g.posted()
	require.Len(t, bodies, 1)

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(bodies[0], &keys))
	require.Len(t, keys, 5, "only the silence is sent: not the preview for the approver and not the duration")

	var body postedBody
	require.NoError(t, json.Unmarshal(bodies[0], &body))
	require.False(t, body.StartsAt.Before(before) || body.StartsAt.After(after), "starts when it runs: %s", body.StartsAt)
	require.Equal(t, time.Hour, body.EndsAt.Sub(body.StartsAt), "lasts as long as the approver read")
	require.Equal(t, "sidekick", body.CreatedBy)
	require.Equal(t, commentWithMarker("Planned database migration", proposalID), body.Comment)
	require.Len(t, body.Matchers, 3)

	require.Equal(t, "Silenced for 1h (silence sil-1).", res.Summary, "Grafana answers 202, which is a success")
	require.Equal(t, []action.Link{{Title: "Silence sil-1", URL: api.URL() + "/alerting/silence/sil-1/edit"}}, res.Links)

	require.Equal(t, []string{"GET " + apiPath + "/alerts", "GET " + apiPath + "/silences", "POST " + apiPath + "/silences"}, requestPaths(api))

	for _, r := range api.Requests() {
		require.Equal(t, "Bearer "+validToken, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Values("X-Grafana-Org-Id"), "the token fixes the organization")
		require.True(t, strings.HasPrefix(r.Header.Get("User-Agent"), "sidekick/"))
	}
}

func TestExecuteTwiceMakesOneSilence(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	s, _ := newSilencer(t, api, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, first := snapshotAndRun(t, s, raw)

	again, err := s.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)

	require.Len(t, g.posted(), 1, "a retry after a crash finds the silence by its marker")
	require.Equal(t, first.Links, again.Links)
	require.Equal(t, "Silence sil-1 exists already.", again.Summary)
}

func TestExecuteDoesNotMakeASecondSilenceWhenTheFirstEnded(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	s, _ := newSilencer(t, api, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, _ := snapshotAndRun(t, s, raw)

	g.advance(2 * time.Hour)
	require.Equal(t, "expired", g.stateOf("sil-1"))

	_, err := s.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)
	require.Len(t, g.posted(), 1, "a silence that a person or the clock ended is not made again")
}

func TestExecuteIgnoresTheSilenceOfAnotherProposal(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	now := time.Now().UTC()

	g.add(fakeSilence{
		Matchers: []mute.Matcher{eq("team", "payments")}, StartsAt: now, EndsAt: now.Add(time.Hour), CreatedBy: "ana",
		Comment: commentWithMarker("Other work", otherProposalID),
	})
	g.add(fakeSilence{
		Matchers: []mute.Matcher{eq("team", "payments")}, StartsAt: now, EndsAt: now.Add(time.Hour), CreatedBy: "ana",
		Comment: "[sidekick proposal " + proposalID + "] typed by a person, on the same line",
	})

	s, _ := newSilencer(t, api, testConfig(api.URL()))

	_, res := snapshotAndRun(t, s, checkoutParams(t))
	require.Len(t, g.posted(), 1)
	require.Equal(t, "Silenced for 1h (silence sil-3).", res.Summary)
}

func TestExecuteChecksTheLimitsAgain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  func(*Config)
	}{
		{name: "the scopes were tightened", cfg: func(c *Config) {
			c.Limits.Scopes = []mute.Scope{{Labels: map[string][]string{"grafana_folder": {"payments"}}}}
		}},
		{name: "the longest duration was lowered", cfg: func(c *Config) { c.Limits.MaxDuration = 30 * time.Minute }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, g := newFakeGrafana(t, "")
			raw := checkoutParams(t)

			first, _ := newSilencer(t, api, testConfig(api.URL()))
			snap, err := snapshotter(t, first).Snapshot(t.Context(), raw)
			require.NoError(t, err)

			cfg := testConfig(api.URL())
			tt.cfg(&cfg)

			tightened, _ := newSilencer(t, api, cfg)

			_, err = tightened.Execute(t.Context(), invocation(raw, snap))
			require.ErrorIs(t, err, action.ErrLimit)
			require.Empty(t, g.posted())
		})
	}
}

func TestExecuteRefusesASnapshotThatIsNotOfTheParameters(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	s, _ := newSilencer(t, api, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, err := snapshotter(t, s).Snapshot(t.Context(), raw)
	require.NoError(t, err)

	tests := []struct {
		name string
		snap *action.Snapshot
	}{
		{name: "no snapshot"},
		{name: "another target", snap: &action.Snapshot{Target: "alertmanager:silence{other}", After: snap.After}},
		{name: "a snapshot that cannot be read", snap: &action.Snapshot{Target: snap.Target, After: json.RawMessage(`[1]`)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := s.Execute(t.Context(), action.Invocation{ProposalID: proposalID, Params: raw, Snapshot: tt.snap})
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Empty(t, g.posted())
		})
	}
}

func TestExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		token         string
		postStatus    int
		postMessage   string
		wantText      string
		wantPermanent bool
	}{
		{name: "the token is refused", token: "other", wantText: "Grafana answered 401", wantPermanent: true},
		{name: "the token may not create silences", postStatus: http.StatusForbidden, postMessage: "permission denied", wantText: "Grafana answered 403: permission denied", wantPermanent: true},
		{name: "Grafana refuses the silence", postStatus: http.StatusBadRequest, postMessage: "silence failed validation", wantText: "silence failed validation", wantPermanent: true},
		{name: "Grafana is down", postStatus: http.StatusServiceUnavailable, wantText: "Grafana answered 503"},
		{
			name: "Grafana has too many silences", postStatus: http.StatusTooManyRequests, postMessage: "Maximum number of silences has been reached.",
			wantText: "remove old silences in Grafana",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, g := newFakeGrafana(t, "")
			cfg := testConfig(api.URL())

			if tt.token != "" {
				cfg.TokenSecret = registry.SecretRef{Name: "other", Key: tt.token}
			}

			if tt.postStatus != 0 {
				g.failPostWith(tt.postStatus, tt.postMessage)
			}

			s, _ := newSilencer(t, api, cfg)
			raw := checkoutParams(t)

			snap, err := snapshotter(t, s).Snapshot(t.Context(), raw)
			require.NoError(t, err)

			_, err = s.Execute(t.Context(), invocation(raw, snap))
			require.ErrorContains(t, err, tt.wantText)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent), "a 429 and a server error are tried again")
		})
	}
}

func TestExecuteInAGrafanaServedFromASubPath(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "/grafana")
	s, _ := newSilencer(t, api, testConfig(api.URL()+"/grafana/"))

	_, res := snapshotAndRun(t, s, checkoutParams(t))

	require.Len(t, g.posted(), 1)
	require.Equal(t, api.URL()+"/grafana/alerting/silence/sil-1/edit", res.Links[0].URL)
}

func TestTheTokenIsReadOnEveryCall(t *testing.T) {
	t.Parallel()

	api, _ := newFakeGrafana(t, "")
	s, secrets := newSilencer(t, api, testConfig(api.URL()))

	snapshotAndRun(t, s, checkoutParams(t))

	require.Equal(t, int64(len(api.Requests())), secrets.calls.Load(), "a rotated token is used without a restart")
}

func TestUndoEndsTheSilence(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	s, _ := newSilencer(t, api, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, _ := snapshotAndRun(t, s, raw)
	g.add(fakeSilence{Matchers: []mute.Matcher{eq("team", "payments")}, StartsAt: time.Now().UTC(), EndsAt: time.Now().UTC().Add(time.Hour), CreatedBy: "ana", Comment: "Other"})

	res, err := undoer(t, s).Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)

	require.Equal(t, "Ended 1 silence early.", res.Summary)
	require.Equal(t, []string{"sil-1"}, g.deleted(), "only the silence of the proposal is ended")
	require.Equal(t, "expired", g.stateOf("sil-1"), "Grafana keeps an ended silence")
	require.Equal(t, "active", g.stateOf("sil-2"))

	again, err := undoer(t, s).Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err, "an undo is safe to repeat")
	require.Equal(t, "The silence has ended already.", again.Summary)
	require.Equal(t, []string{"sil-1"}, g.deleted())
}

func TestUndoOfASilenceThatIsGoneIsASuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*fakeGrafana)
		want   string
	}{
		{name: "it was removed", want: "No silence of this proposal exists any more"},
		{name: "it ended", change: func(g *fakeGrafana) { g.advance(2 * time.Hour) }, want: "The silence has ended already."},
		{name: "it vanished between the list and the delete", change: func(g *fakeGrafana) { g.failWith(0, 0, 0, http.StatusNotFound) }, want: "Ended 1 silence early."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, g := newFakeGrafana(t, "")
			s, _ := newSilencer(t, api, testConfig(api.URL()))
			raw := checkoutParams(t)

			snap, _ := snapshotAndRun(t, s, raw)

			if tt.change != nil {
				tt.change(g)
			} else {
				g.mu.Lock()
				g.silences = nil
				g.mu.Unlock()
			}

			res, err := undoer(t, s).Undo(t.Context(), undoInvocation(raw, snap), snap, false)
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.want)
		})
	}
}

func TestUndoShowsAnEditedSilenceAndForceEndsIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*fakeSilence)
	}{
		{name: "the matchers changed", edit: func(s *fakeSilence) { s.Matchers = []mute.Matcher{eq("team", "payments")} }},
		{name: "the end moved", edit: func(s *fakeSilence) { s.EndsAt = s.EndsAt.Add(3 * time.Hour) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, g := newFakeGrafana(t, "")
			s, _ := newSilencer(t, api, testConfig(api.URL()))
			raw := checkoutParams(t)

			snap, _ := snapshotAndRun(t, s, raw)
			g.edit("sil-1", tt.edit)

			_, err := undoer(t, s).Undo(t.Context(), undoInvocation(raw, snap), snap, false)

			var conflict *action.ConflictError
			require.ErrorAs(t, err, &conflict)
			require.Contains(t, string(conflict.Live), `"id":"sil-1"`)
			require.Empty(t, g.deleted(), "an edited silence is not ended without force")
			require.Equal(t, "active", g.stateOf("sil-1"))

			res, err := undoer(t, s).Undo(t.Context(), undoInvocation(raw, snap), snap, true)
			require.NoError(t, err)
			require.Equal(t, "Ended 1 silence early.", res.Summary)
			require.Equal(t, "expired", g.stateOf("sil-1"))
		})
	}
}

func TestUndoFindsASilenceThatGotANewID(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	s, _ := newSilencer(t, api, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, _ := snapshotAndRun(t, s, raw)

	// Grafana makes a new silence, with the comment and so the marker, when
	// a person edits the matchers in its UI.
	g.edit("sil-1", func(s *fakeSilence) { s.ID = "sil-7" })

	_, err := undoer(t, s).Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, []string{"sil-7"}, g.deleted())
}

func TestUndoDoesNotCheckTheLimitsAgain(t *testing.T) {
	t.Parallel()

	api, g := newFakeGrafana(t, "")
	raw := checkoutParams(t)

	first, _ := newSilencer(t, api, testConfig(api.URL()))
	snap, _ := snapshotAndRun(t, first, raw)

	cfg := testConfig(api.URL())
	cfg.Limits.Scopes = []mute.Scope{{Labels: map[string][]string{"grafana_folder": {"payments"}}}}

	tightened, _ := newSilencer(t, api, cfg)

	_, err := undoer(t, tightened).Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err, "ending a silence is the safe direction")
	require.Equal(t, []string{"sil-1"}, g.deleted())
}

func TestUndoErrors(t *testing.T) {
	t.Parallel()

	api, _ := newFakeGrafana(t, "")
	s, _ := newSilencer(t, api, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, _ := snapshotAndRun(t, s, raw)

	t.Run("it does not say which proposal it undoes", func(t *testing.T) {
		t.Parallel()

		_, err := undoer(t, s).Undo(t.Context(), action.Invocation{ProposalID: otherProposalID, Params: raw}, snap, false)
		require.ErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("the snapshot cannot be read", func(t *testing.T) {
		t.Parallel()

		_, err := undoer(t, s).Undo(t.Context(), undoInvocation(raw, snap), action.Snapshot{After: json.RawMessage(`[1]`)}, false)
		require.ErrorIs(t, err, action.ErrPermanent)
	})
}
