package alertmanagersilence

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/httpauth"
)

// postedBody is the body of a POST that made a silence.
type postedBody struct {
	Matchers  []mute.Matcher `json:"matchers"`
	StartsAt  time.Time      `json:"startsAt"`
	EndsAt    time.Time      `json:"endsAt"`
	CreatedBy string         `json:"createdBy"`
	Comment   string         `json:"comment"`
}

func TestExecuteMakesTheSilence(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")
	s := newTestSilencer(t, api, am, testConfig(api.URL()))

	_, res := snapshotAndRun(t, s, checkoutParams(t))

	bodies := am.postedBodies()
	require.Len(t, bodies, 1)
	require.ElementsMatch(t, []string{"matchers", "startsAt", "endsAt", "createdBy", "comment"}, bodyKeys(t, bodies[0]),
		"only the silence is sent: the preview for the approver (firingNow) and the duration are not")

	var body postedBody
	require.NoError(t, json.Unmarshal(bodies[0], &body))
	require.True(t, body.StartsAt.Equal(testNow), "starts when it runs: %s", body.StartsAt)
	require.True(t, body.EndsAt.Equal(testNow.Add(time.Hour)), "lasts as long as the approver read: %s", body.EndsAt)
	require.Equal(t, "sidekick", body.CreatedBy)
	require.Equal(t, commentWithMarker("Planned database migration", proposalID), body.Comment)
	require.True(t, mute.HasMarker(body.Comment, proposalID))
	require.Len(t, body.Matchers, 3)
	require.True(t, mute.SameMatchers(body.Matchers, []mute.Matcher{
		eq("alertname", "HighLatency"), eq("service", "checkout-api"), {Name: "env", Value: "prod|stage", IsRegex: true, IsEqual: true},
	}))

	require.Equal(t, "Silenced for 1h (silence sil-1).", res.Summary)
	require.Equal(t, []action.Link{{Title: "Silence sil-1", URL: api.URL() + "/#/silences/sil-1"}}, res.Links)
	require.Equal(t, []string{"GET /api/v2/alerts", "GET /api/v2/silences", "POST /api/v2/silences"}, requestPaths(api))
}

func TestExecuteStartsWhenItRuns(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")
	s := newTestSilencer(t, api, am, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, err := s.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	am.advance(23 * time.Hour)

	_, err = s.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err, "a proposal approved a day later still makes a silence that lasts as long as the approver read")

	var body postedBody
	require.NoError(t, json.Unmarshal(am.postedBodies()[0], &body))
	require.True(t, body.StartsAt.Equal(testNow.Add(23*time.Hour)))
	require.True(t, body.EndsAt.Equal(testNow.Add(24*time.Hour)))
}

func TestExecuteTwiceMakesOneSilence(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")
	s := newTestSilencer(t, api, am, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, first := snapshotAndRun(t, s, raw)

	again, err := s.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)

	require.Equal(t, 1, am.postCount(), "a retry after a crash finds the silence by its marker")
	require.Equal(t, first.Links, again.Links)
	require.Equal(t, "Silence sil-1 exists already.", again.Summary)
}

func TestExecuteDoesNotMakeASecondSilenceWhenTheFirstEnded(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")
	s := newTestSilencer(t, api, am, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, _ := snapshotAndRun(t, s, raw)

	am.advance(2 * time.Hour)
	require.Equal(t, "expired", am.stateOf("sil-1"))

	_, err := s.Execute(t.Context(), invocation(raw, snap))
	require.NoError(t, err)
	require.Equal(t, 1, am.postCount(), "a silence that a person or the clock ended is not made again")
}

func TestExecuteIgnoresTheSilenceOfAnotherProposal(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")
	am.add(fakeSilence{
		Matchers: []mute.Matcher{eq("team", "payments")}, StartsAt: testNow, EndsAt: testNow.Add(time.Hour), CreatedBy: "ana",
		Comment: commentWithMarker("Other work", otherProposalID),
	})
	am.add(fakeSilence{
		Matchers: []mute.Matcher{eq("team", "payments")}, StartsAt: testNow, EndsAt: testNow.Add(time.Hour), CreatedBy: "ana",
		Comment: "[sidekick proposal " + proposalID + "] typed by a person, on the same line",
	})

	s := newTestSilencer(t, api, am, testConfig(api.URL()))

	_, res := snapshotAndRun(t, s, checkoutParams(t))
	require.Equal(t, 1, am.postCount())
	require.Equal(t, "Silenced for 1h (silence sil-3).", res.Summary)
}

func TestExecuteChecksTheLimitsAgain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  func(*Config)
	}{
		{name: "the scopes were tightened", cfg: func(c *Config) {
			c.Limits.Scopes = []mute.Scope{{Labels: map[string][]string{"team": {"payments"}}}}
		}},
		{name: "the longest duration was lowered", cfg: func(c *Config) { c.Limits.MaxDuration = 30 * time.Minute }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, am := newFakeAM(t, "")
			raw := checkoutParams(t)

			snap, err := newTestSilencer(t, api, am, testConfig(api.URL())).Snapshot(t.Context(), raw)
			require.NoError(t, err)

			calls := len(api.Requests())

			cfg := testConfig(api.URL())
			tt.cfg(&cfg)

			_, err = newTestSilencer(t, api, am, cfg).Execute(t.Context(), invocation(raw, snap))
			require.ErrorIs(t, err, action.ErrLimit)
			require.Len(t, api.Requests(), calls, "a refused run calls nothing")
		})
	}
}

func TestExecuteRefusesASnapshotThatIsNotOfTheParameters(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")
	s := newTestSilencer(t, api, am, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, err := s.Snapshot(t.Context(), raw)
	require.NoError(t, err)

	tampered := func(change func(*snapshotDoc)) action.Snapshot {
		doc := decodeAfter(t, snap)
		change(&doc)

		out, err := json.Marshal(doc)
		require.NoError(t, err)

		return action.Snapshot{Target: snap.Target, After: out}
	}

	tests := []struct {
		name string
		snap *action.Snapshot
		want error
	}{
		{name: "no snapshot", want: errNoSnapshot},
		{name: "other matchers", snap: ptr(tampered(func(d *snapshotDoc) { d.Matchers = []mute.Matcher{eq("team", "payments")} })), want: errTargetDiffer},
		{name: "another duration", snap: ptr(tampered(func(d *snapshotDoc) { d.Duration = "2h" })), want: errTargetDiffer},
		{name: "another comment", snap: ptr(tampered(func(d *snapshotDoc) { d.Comment = "something else" })), want: errTargetDiffer},
		{name: "another target", snap: &action.Snapshot{Target: "alertmanager:silence{other}", After: snap.After}, want: errTargetDiffer},
		{name: "a creator name with a line break", snap: ptr(tampered(func(d *snapshotDoc) { d.CreatedBy = "a\nb" })), want: errBadSnapshot},
		{name: "no creator name", snap: ptr(tampered(func(d *snapshotDoc) { d.CreatedBy = "" })), want: errBadSnapshot},
		{name: "text that is not a silence", snap: &action.Snapshot{Target: snap.Target, After: json.RawMessage(`"x"`)}, want: errBadSnapshot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := s.Execute(t.Context(), action.Invocation{ProposalID: proposalID, Params: raw, Snapshot: tt.snap})
			require.ErrorIs(t, err, tt.want)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Zero(t, am.postCount())
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestExecuteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		list, post    int
		wantPermanent bool
		wantText      string
		wantPosts     int
	}{
		{name: "the silence is refused as invalid", post: http.StatusBadRequest, wantPermanent: true, wantText: "answered 400", wantPosts: 1},
		{name: "the token is refused", post: http.StatusUnauthorized, wantPermanent: true, wantText: "answered 401", wantPosts: 1},
		{name: "Alertmanager is down", post: http.StatusServiceUnavailable, wantText: "answered 503", wantPosts: 1},
		{name: "too many requests", post: http.StatusTooManyRequests, wantText: "answered 429", wantPosts: 1},
		{name: "the list fails", list: http.StatusInternalServerError, wantText: "answered 500"},
		{name: "the list is refused", list: http.StatusForbidden, wantPermanent: true, wantText: "answered 403"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, am := newFakeAM(t, "")
			s := newTestSilencer(t, api, am, testConfig(api.URL()))
			raw := checkoutParams(t)

			snap, err := s.Snapshot(t.Context(), raw)
			require.NoError(t, err)

			am.failWith(tt.list, tt.post, 0)

			_, err = s.Execute(t.Context(), invocation(raw, snap))
			require.ErrorContains(t, err, tt.wantText)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
			require.Equal(t, tt.wantPosts, am.postCount())
		})
	}
}

func TestRequestsCarryTheCredentialsAndHeaders(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "/alertmanager")

	cfg := testConfig(api.URL() + "/alertmanager/")
	cfg.BearerTokenSecret = ref("token")
	cfg.Headers = map[string]string{"X-Scope-OrgID": "team-a"}

	s := newTestSilencer(t, api, am, cfg)

	_, res := snapshotAndRun(t, s, checkoutParams(t))
	require.Equal(t, api.URL()+"/alertmanager/#/silences/sil-1", res.Links[0].URL)

	paths := requestPaths(api)
	require.Equal(t, []string{"GET /alertmanager/api/v2/alerts", "GET /alertmanager/api/v2/silences", "POST /alertmanager/api/v2/silences"}, paths)

	for _, r := range api.Requests() {
		require.Equal(t, "Bearer fake-value", r.Header.Get("Authorization"))
		require.Equal(t, "team-a", r.Header.Get("X-Scope-OrgID"))
		require.Equal(t, "application/json", r.Header.Get("Accept"))
	}
}

func TestRequestsCarryBasicAuth(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")

	cfg := testConfig(api.URL())
	cfg.BasicAuth = &httpauth.BasicAuth{Username: "sidekick", PasswordSecret: *ref("password")}

	s := newTestSilencer(t, api, am, cfg)

	_, err := s.Snapshot(t.Context(), checkoutParams(t))
	require.NoError(t, err)

	user, password, ok := basicAuthOf(t, api.Requests()[0])
	require.True(t, ok)
	require.Equal(t, "sidekick", user)
	require.Equal(t, "fake-value", password)
}
