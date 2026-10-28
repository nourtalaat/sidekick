//go:build integration

package slackapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/output"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/slackroles"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/watch"
	"github.com/nourtalaat/sidekick/strategies"
)

// The investigations in fixture/investigations.sql, and the watcher of
// the final one.
var (
	finalInvestigation   = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	queuedInvestigation  = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c2")
	triagedInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c3")
	runningWatcher       = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000d1")
)

// stopWatching is the action ID of the first Stop watching button.
const stopWatching = slackout.ActionStopWatcher + ":1"

// heldTriageKey adds the job that ran the triage of the triaged
// investigation, still running, as it is for a moment after the run ends.
const heldTriageKey = `INSERT INTO jobs (kind, route, payload, unique_key, status)
VALUES ('investigate', 'default', '{"investigation_id": "0192d3a4-0000-7000-8000-0000000000c3"}',
        'investigate:0192d3a4-0000-7000-8000-0000000000b3', 'running')`

// stopWithin is how soon a stopped run must end.
const stopWithin = 5 * time.Second

// responsePath is the path of the response URL of the test clicks.
const responsePath = "/actions/T01TEAM/1/xyz"

// testTriggerID is the trigger ID of the test clicks.
const testTriggerID = "T01TRIGGER.1.abc"

// testWorker is the locked_by value of the jobs the tests claim.
const testWorker = "test-worker"

// clickBy is a click on one of Sidekick's buttons.
type clickBy struct {
	user   string
	action string
	id     uuid.UUID
}

func TestInteractivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// role is the role every user has; zero means responder.
		role roles.Role
		// sql runs first, to set up the database.
		sql string
		// before are clicks sent first; their answers must be 200.
		before []clickBy
		send   clickBy
		// forged signs the request with the wrong secret.
		forged     bool
		wantStatus int
		// wantNotes are the notes posted to the user, in order.
		wantNotes []string
		check     func(t *testing.T, b *buttonsHarness)
	}{
		{
			name:       "Correct confirms the verdict and shows who gave it",
			send:       clickBy{user: "U01ANA", action: slackout.ActionVerdictCorrect, id: finalInvestigation},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				inv := b.investigation(t, finalInvestigation)
				require.Equal(t, "true_positive", *inv.ConfirmedVerdict)
				require.Equal(t, "U01ANA", *inv.ConfirmedBySlackUser)
				require.Equal(t, []output.Feedback{{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "U01ANA"}}}, b.ratings(t))
				require.Equal(t, int64(6), b.publishSeq(t, finalInvestigation), "the message is shown again")
				require.Equal(t, 1, b.publishJobs(t))
				require.InDelta(t, 1, b.feedbackCount(t, output.RatingCorrect), 0)
			},
		},
		{
			name:       "Wrong is stored without confirming the verdict",
			send:       clickBy{user: "U01ANA", action: slackout.ActionVerdictWrong, id: finalInvestigation},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Nil(t, b.investigation(t, finalInvestigation).ConfirmedVerdict)
				require.Equal(t, []output.Feedback{{Rating: output.RatingWrong, By: output.Person{SlackUserID: "U01ANA"}}}, b.ratings(t))
				require.Equal(t, int64(6), b.publishSeq(t, finalInvestigation))
				require.InDelta(t, 1, b.feedbackCount(t, output.RatingWrong), 0)
			},
		},
		{
			name:       "a repeated click changes nothing",
			before:     []clickBy{{user: "U01ANA", action: slackout.ActionVerdictCorrect, id: finalInvestigation}},
			send:       clickBy{user: "U01ANA", action: slackout.ActionVerdictCorrect, id: finalInvestigation},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Len(t, b.ratings(t), 1)
				require.Equal(t, int64(6), b.publishSeq(t, finalInvestigation), "only the first click shows the message again")
				require.InDelta(t, 1, b.feedbackCount(t, output.RatingCorrect), 0, "only the first click counts")
			},
		},
		{
			name:       "Stop asks the run to stop",
			send:       clickBy{user: "U01STOP", action: slackout.ActionStop, id: queuedInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteStopping},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				inv := b.investigation(t, queuedInvestigation)
				require.NotNil(t, inv.CancelRequestedAt)
				require.Equal(t, "U01STOP", *inv.CancelRequestedBySlackUser)
			},
		},
		{
			name:       "a repeated Stop says the run is stopping already",
			before:     []clickBy{{user: "U01STOP", action: slackout.ActionStop, id: queuedInvestigation}},
			send:       clickBy{user: "U01BEN", action: slackout.ActionStop, id: queuedInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteStopping, noteEnded},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, "U01STOP", *b.investigation(t, queuedInvestigation).CancelRequestedBySlackUser, "the first Stop is kept")
			},
		},
		{
			name:       "Investigate anyway starts a full investigation of a triage-only result",
			send:       clickBy{user: "U01BEN", action: slackout.ActionInvestigateAnyway, id: triagedInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteAnyway},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				inv := b.investigation(t, triagedInvestigation)
				require.Equal(t, investigator.StatusQueued, inv.Status)
				require.EqualValues(t, 2, inv.Run)
				require.Nil(t, inv.Verdict)
				require.Nil(t, inv.StopReason)
				require.Nil(t, inv.ConfirmedVerdict, "the confirmation of the triage verdict does not carry over")
				require.Nil(t, inv.ConfirmedBySlackUser)
				require.Nil(t, inv.ConfirmedAt)
				require.Equal(t, 1, b.feedbackRows(t, triagedInvestigation), "the rating of the triage verdict stays as history")
				require.Equal(t, []string{"investigate:0192d3a4-0000-7000-8000-0000000000b3"}, b.pendingInvestigateKeys(t, triagedInvestigation))
				require.Equal(t, []string{"user U01BEN investigation.investigate_anyway"}, b.auditActions(t, triagedInvestigation))
			},
		},
		{
			name:       "a second Investigate anyway changes nothing",
			before:     []clickBy{{user: "U01BEN", action: slackout.ActionInvestigateAnyway, id: triagedInvestigation}},
			send:       clickBy{user: "U01ANA", action: slackout.ActionInvestigateAnyway, id: triagedInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteAnyway, noteNotTriaged},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.EqualValues(t, 2, b.investigation(t, triagedInvestigation).Run)
				require.Len(t, b.pendingInvestigateKeys(t, triagedInvestigation), 1)
				require.Len(t, b.auditActions(t, triagedInvestigation), 1, "only the first click is audited")
			},
		},
		{
			name:       "Investigate anyway while the triage job is still finishing",
			sql:        heldTriageKey,
			send:       clickBy{user: "U01BEN", action: slackout.ActionInvestigateAnyway, id: triagedInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteBusy},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				inv := b.investigation(t, triagedInvestigation)
				require.Equal(t, investigator.StatusFinal, inv.Status, "the reset is rolled back")
				require.EqualValues(t, 1, inv.Run)
				require.Equal(t, "duplicate", *inv.ConfirmedVerdict)
				require.Empty(t, b.auditActions(t, triagedInvestigation))
			},
		},
		{
			name:       "Investigate anyway on a full investigation",
			send:       clickBy{user: "U01BEN", action: slackout.ActionInvestigateAnyway, id: finalInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteNotTriaged},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				inv := b.investigation(t, finalInvestigation)
				require.Equal(t, investigator.StatusFinal, inv.Status)
				require.EqualValues(t, 1, inv.Run)
			},
		},
		{
			name:       "a viewer cannot start a full investigation",
			role:       roles.Viewer,
			send:       clickBy{user: "U01VIEW", action: slackout.ActionInvestigateAnyway, id: triagedInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteDenied},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, investigator.StatusFinal, b.investigation(t, triagedInvestigation).Status)
				require.Empty(t, b.pendingInvestigateKeys(t, triagedInvestigation))
			},
		},
		{
			name:       "Stop watching stops the watcher and shows it",
			send:       clickBy{user: "U01STOP", action: stopWatching, id: runningWatcher},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteWatcherStopped},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				w := b.watcher(t)
				require.Equal(t, watch.StatusStopped, w.Status)
				require.Equal(t, new(watch.StopResponder), w.StopReason)
				require.Equal(t, new("U01STOP"), w.StoppedBySlackUser)
				require.NotNil(t, w.EndedAt)

				changes := collect[db.WatcherChange](t, b.pool, "SELECT * FROM watcher_changes")
				require.Len(t, changes, 1, "the outputs get the change")
				require.Equal(t, finalInvestigation, changes[0].InvestigationID)
				require.Equal(t, int64(6), changes[0].Seq)
				require.Equal(t, int64(7), b.publishSeq(t, finalInvestigation), "the message is shown again")
				require.Equal(t, 1, b.publishJobs(t))

				inbox := collect[db.InvestigationInbox](t, b.pool, "SELECT * FROM investigation_inbox")
				require.Len(t, inbox, 1, "the investigation's inbox gets the change")
				require.Equal(t, watch.InboxKind, inbox[0].Kind)

				var item watch.InboxItem
				require.NoError(t, json.Unmarshal(inbox[0].Body, &item))
				require.Equal(t, output.WatcherStopped, item.State.Status)
				require.Equal(t, &output.Person{SlackUserID: "U01STOP"}, item.State.StoppedBy)

				require.Equal(t, 1, b.count(t, "SELECT count(*) FROM jobs WHERE kind = 'close_group'"), "the group can close")
				require.InDelta(t, 1, b.stateChanges(t, "stopped"), 0)
			},
		},
		{
			name:       "a repeated Stop watching says the watcher has ended",
			before:     []clickBy{{user: "U01STOP", action: stopWatching, id: runningWatcher}},
			send:       clickBy{user: "U01BEN", action: stopWatching, id: runningWatcher},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteWatcherStopped, noteWatcherEnded},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, new("U01STOP"), b.watcher(t).StoppedBySlackUser, "the first stop is kept")
				require.InDelta(t, 1, b.stateChanges(t, "stopped"), 0)
			},
		},
		{
			name:       "a viewer cannot stop watching",
			role:       roles.Viewer,
			send:       clickBy{user: "U01VIEW", action: stopWatching, id: runningWatcher},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteDenied},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, watch.StatusRunning, b.watcher(t).Status)
				require.Empty(t, collect[db.WatcherChange](t, b.pool, "SELECT * FROM watcher_changes"))
			},
		},
		{
			name:       "a forged Stop watching is refused",
			send:       clickBy{user: "U01STOP", action: stopWatching, id: runningWatcher},
			forged:     true,
			wantStatus: http.StatusUnauthorized,
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, watch.StatusRunning, b.watcher(t).Status)
			},
		},
		{
			name:       "Correct before there is a verdict",
			send:       clickBy{user: "U01ANA", action: slackout.ActionVerdictCorrect, id: queuedInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteNoVerdict},
			check:      requireNoRatings,
		},
		{
			name:       "a viewer is denied",
			role:       roles.Viewer,
			send:       clickBy{user: "U01VIEW", action: slackout.ActionStop, id: queuedInvestigation},
			wantStatus: http.StatusOK,
			wantNotes:  []string{noteDenied},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Nil(t, b.investigation(t, queuedInvestigation).CancelRequestedAt)
			},
		},
		{
			name:       "a forged signature is refused",
			send:       clickBy{user: "U01ANA", action: slackout.ActionVerdictCorrect, id: finalInvestigation},
			forged:     true,
			wantStatus: http.StatusUnauthorized,
			check:      requireNoRatings,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			role := tt.role
			if role == 0 {
				role = roles.Responder
			}

			b := setupButtons(t, role)

			if tt.sql != "" {
				_, err := b.pool.Exec(t.Context(), tt.sql)
				require.NoError(t, err)
			}

			for _, c := range tt.before {
				require.Equal(t, http.StatusOK, b.click(t, c, false))
			}

			require.Equal(t, tt.wantStatus, b.click(t, tt.send, tt.forged))
			require.Equal(t, tt.wantNotes, b.notes(t))
			tt.check(t, b)
		})
	}
}

// TestInteractivityRoles presses Stop with the roles of slack.roleUsers,
// as sidekick serve looks them up.
func TestInteractivityRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		user          string
		wantNotes     []string
		wantStoppedBy *string
	}{
		{name: "a viewer is refused", user: "U01VIEW", wantNotes: []string{noteDenied}},
		{name: "a responder stops the run", user: "U01ANA", wantNotes: []string{noteStopping}, wantStoppedBy: new("U01ANA")},
		{name: "an admin stops the run", user: "U01ADMIN", wantNotes: []string{noteStopping}, wantStoppedBy: new("U01ADMIN")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Slack{RoleUsers: map[string]config.RoleName{"U01VIEW": config.RoleViewer, "U01ADMIN": config.RoleAdmin}}

			lookup, err := slackroles.New(slackroles.Options{Slack: &cfg})
			require.NoError(t, err)

			b := setupButtonsWith(t, lookup)

			require.Equal(t, http.StatusOK, b.click(t, clickBy{user: tt.user, action: slackout.ActionStop, id: queuedInvestigation}, false))
			require.Equal(t, tt.wantNotes, b.notes(t))
			require.Equal(t, tt.wantStoppedBy, b.investigation(t, queuedInvestigation).CancelRequestedBySlackUser)
		})
	}
}

// TestStopAcrossReplicas presses Stop on one replica while the run waits
// for a slow model call on another, and checks that the run ends as
// cancelled within a few seconds.
func TestStopAcrossReplicas(t *testing.T) {
	t.Parallel()

	b := setupButtons(t, roles.Responder)

	started := make(chan struct{})
	slow := modeltest.New(t, model.Capabilities{ToolCalling: true}, modeltest.Turn{
		Func: func(ctx context.Context, _ model.Request) (model.Response, error) {
			close(started)
			<-ctx.Done()

			return model.Response{}, &model.Error{Kind: model.ErrTimeout, Err: ctx.Err()}
		},
	})
	slow.ExpectAllUsed()

	// Replica A runs the investigation on a pool of its own.
	poolA, err := pgxpool.NewWithConfig(t.Context(), b.pool.Config().Copy())
	require.NoError(t, err)
	t.Cleanup(poolA.Close)

	canceller := investigator.NewCanceller(poolA, slog.New(slog.DiscardHandler))

	go func() { _ = canceller.Run(t.Context()) }()

	runner := newInvestigator(t, poolA, slow, canceller)

	jobs, err := db.New(poolA).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: testWorker, LeaseSeconds: time.Minute.Seconds(), Kinds: []string{queue.KindInvestigate}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	done := make(chan error, 1)

	go func() { done <- runner.Handle(t.Context(), jobs[0]) }()

	select {
	case <-started:
	case <-time.After(stopWithin):
		t.Fatal("the model call did not start")
	}

	// Replica B, the one Slack sends the click to, has the test's pool.
	require.Equal(t, http.StatusOK, b.click(t, clickBy{user: "U01STOP", action: slackout.ActionStop, id: queuedInvestigation}, false))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(stopWithin):
		t.Fatalf("the run did not end within %s of the Stop", stopWithin)
	}

	inv := b.investigation(t, queuedInvestigation)
	require.Equal(t, investigator.StatusCancelled, inv.Status)
	require.Equal(t, string(report.StopCancelled), *inv.StopReason)
	require.Equal(t, "U01STOP", *inv.CancelRequestedBySlackUser)
}

// buttonsHarness serves Buttons at InteractivityPattern on the Slack app
// harness, with the investigations of fixture/investigations.sql and a
// fake Slack that takes the notes posted to response URLs.
type buttonsHarness struct {
	*harness

	buttons *Buttons
	// responses is Slack's side of the response URL.
	responses *fakehttp.Server
	url       string

	mu sync.Mutex
	// posted are the notes posted to the user who clicked, in order.
	posted []string
	// views are the bodies of the views.open calls, in order, and
	// openError is the Slack error code they are answered with, empty for
	// none.
	views     [][]byte
	openError string
}

// fixedRole gives every user one role.
type fixedRole roles.Role

func (r fixedRole) Role(context.Context, string, string) (roles.Role, error) {
	return roles.Role(r), nil
}

func setupButtons(t *testing.T, role roles.Role) *buttonsHarness {
	t.Helper()

	return setupButtonsWith(t, fixedRole(role))
}

// setupButtonsWith is setupButtons with lookup finding the roles.
func setupButtonsWith(t *testing.T, lookup roles.Lookup) *buttonsHarness {
	t.Helper()

	h := setup(t)
	storetest.LoadFixture(t, h.pool, "fixture/investigations.sql")

	metrics, err := feedback.NewMetrics(h.reg)
	require.NoError(t, err)

	recorder, err := watch.NewRecorder(slog.New(slog.DiscardHandler), h.reg)
	require.NoError(t, err)

	stopper, err := watch.NewStopper(watch.StopperOptions{Pool: h.pool, CloseGroup: grouper.EnqueueClose, Recorder: recorder})
	require.NoError(t, err)

	b := &buttonsHarness{harness: h, responses: fakehttp.New(t)}
	b.responses.HandleFunc(http.MethodPost, responsePath, b.record)
	h.slack.HandleFunc(http.MethodPost, "/"+methodViewsOpen, b.openView)

	b.buttons = NewButtons(ButtonsOptions{
		Handler:  h.handler,
		Pool:     h.pool,
		Roles:    lookup,
		Metrics:  metrics,
		Watchers: stopper,
		HTTP:     b.responses.Client(),
		Logger:   slog.New(slog.DiscardHandler),

		PublicURL: testPublicURL,
	})

	mux := http.NewServeMux()
	mux.Handle(InteractivityPattern, b.buttons)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	b.url = server.URL + "/slack/interactivity"

	return b
}

// click sends c to POST /slack/interactivity as Slack does, signed with
// the test signing secret, or a wrong one when forged is set, and returns
// the status of the answer.
func (b *buttonsHarness) click(t *testing.T, c clickBy, forged bool) int {
	t.Helper()

	status, _ := b.post(t, map[string]any{
		"type":         "block_actions",
		"team":         map[string]string{"id": "T01TEAM"},
		"user":         map[string]string{"id": c.user},
		"api_app_id":   "A01SIDEKICK",
		"trigger_id":   testTriggerID,
		"response_url": b.responses.URL() + responsePath,
		"channel":      map[string]string{"id": "C01ALERTS"},
		"actions": []map[string]any{{
			"type": "button", "action_id": c.action, "block_id": "feedback", "value": c.id.String(),
			"action_ts": "1793023330.000300",
		}},
	}, forged)

	return status
}

// post sends the interaction payload to POST /slack/interactivity as Slack
// does, signed with the test signing secret, or a wrong one when forged is
// set, and returns the status and the body of the answer.
func (b *buttonsHarness) post(t *testing.T, interaction map[string]any, forged bool) (int, string) {
	t.Helper()

	payload, err := json.Marshal(interaction)
	require.NoError(t, err)

	body := []byte(url.Values{payloadField: {string(payload)}}.Encode())

	secret := testSigningSecret
	if forged {
		secret = "not-the-signing-secret"
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, b.url, bytes.NewReader(body))
	require.NoError(t, err)

	req.Header = signed(secret, time.Now(), body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	answer, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(answer)
}

// openView keeps the body of one views.open call, and answers it with
// openError when that is set.
func (b *buttonsHarness) openView(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	b.mu.Lock()
	b.views = append(b.views, raw)
	openError := b.openError
	b.mu.Unlock()

	if openError != "" {
		_, _ = w.Write([]byte(`{"ok": false, "error": "` + openError + `"}`))

		return
	}

	_, _ = w.Write([]byte(`{"ok": true}`))
}

// record keeps the text of one note posted to the response URL.
func (b *buttonsHarness) record(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	var note struct {
		ResponseType    string `json:"response_type"`
		ReplaceOriginal bool   `json:"replace_original"`
		Text            string `json:"text"`
	}

	if err := json.Unmarshal(raw, &note); err != nil || note.ResponseType != "ephemeral" || note.ReplaceOriginal {
		http.Error(w, "not an ephemeral note", http.StatusBadRequest)

		return
	}

	b.mu.Lock()
	b.posted = append(b.posted, note.Text)
	b.mu.Unlock()
}

// notes returns the notes posted so far.
func (b *buttonsHarness) notes(t *testing.T) []string {
	t.Helper()

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.posted) == 0 {
		return nil
	}

	return slices.Clone(b.posted)
}

// investigation returns the investigation with id.
func (b *buttonsHarness) investigation(t *testing.T, id uuid.UUID) db.Investigation {
	t.Helper()

	inv, err := db.New(b.pool).GetInvestigation(t.Context(), id)
	require.NoError(t, err)

	return inv
}

// ratings returns the ratings of the final investigation's verdict.
func (b *buttonsHarness) ratings(t *testing.T) []output.Feedback {
	t.Helper()

	got, err := feedback.Verdicts(t.Context(), b.pool, b.investigation(t, finalInvestigation))
	require.NoError(t, err)

	return got
}

// publishSeq returns the seq of the publish state of id.
func (b *buttonsHarness) publishSeq(t *testing.T, id uuid.UUID) int64 {
	t.Helper()

	var seq int64
	require.NoError(t, b.pool.QueryRow(t.Context(), "SELECT seq FROM publish_states WHERE investigation_id = $1", id).Scan(&seq))

	return seq
}

// feedbackRows counts the feedback rows of id.
func (b *buttonsHarness) feedbackRows(t *testing.T, id uuid.UUID) int {
	t.Helper()

	var n int
	require.NoError(t, b.pool.QueryRow(t.Context(), "SELECT count(*) FROM feedback WHERE investigation_id = $1", id).Scan(&n))

	return n
}

// auditActions returns each audit entry of id as "actor_kind actor_id
// action".
func (b *buttonsHarness) auditActions(t *testing.T, id uuid.UUID) []string {
	t.Helper()

	rows, err := b.pool.Query(t.Context(),
		"SELECT actor_kind || ' ' || actor_id || ' ' || action FROM audit_log WHERE investigation_id = $1 ORDER BY id", id)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return out
}

// pendingInvestigateKeys returns the unique keys of the pending
// investigate jobs of id.
func (b *buttonsHarness) pendingInvestigateKeys(t *testing.T, id uuid.UUID) []string {
	t.Helper()

	rows, err := b.pool.Query(t.Context(),
		"SELECT unique_key FROM jobs WHERE kind = 'investigate' AND status = 'pending' AND payload->>'investigation_id' = $1", id.String())
	require.NoError(t, err)

	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return keys
}

// publishJobs counts the publish jobs.
func (b *buttonsHarness) publishJobs(t *testing.T) int {
	t.Helper()

	var n int
	require.NoError(t, b.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'publish'").Scan(&n))

	return n
}

// feedbackCount returns sidekick_feedback_total for verdict ratings of
// rating, or 0 when the series does not exist.
func (b *buttonsHarness) feedbackCount(t *testing.T, rating output.Rating) float64 {
	t.Helper()

	families, err := b.reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() != feedback.MetricFeedback {
			continue
		}

		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}

			if labels["target"] == feedback.TargetVerdict && labels["rating"] == string(rating) {
				return m.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// watcher returns the watcher of the final investigation.
func (b *buttonsHarness) watcher(t *testing.T) db.Watcher {
	t.Helper()

	w, err := db.New(b.pool).GetWatcher(t.Context(), runningWatcher)
	require.NoError(t, err)

	return w
}

// count returns the number query counts.
func (b *buttonsHarness) count(t *testing.T, query string) int {
	t.Helper()

	var n int
	require.NoError(t, b.pool.QueryRow(t.Context(), query).Scan(&n))

	return n
}

// stateChanges returns sidekick_watcher_state_changes_total for status, or
// 0 when the series does not exist.
func (b *buttonsHarness) stateChanges(t *testing.T, status string) float64 {
	t.Helper()

	families, err := b.reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() != watch.MetricStateChanges {
			continue
		}

		for _, m := range f.GetMetric() {
			if m.GetLabel()[0].GetValue() == status {
				return m.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// collect runs query and returns its rows as T, column by column.
func collect[T any](t *testing.T, pool *pgxpool.Pool, query string) []T {
	t.Helper()

	rows, err := pool.Query(t.Context(), query)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[T])
	require.NoError(t, err)

	return out
}

// requireNoRatings checks that no rating was stored.
func requireNoRatings(t *testing.T, b *buttonsHarness) {
	t.Helper()

	require.Empty(t, b.ratings(t))
	require.Nil(t, b.investigation(t, finalInvestigation).ConfirmedVerdict)
}

// newInvestigator returns an investigator on pool whose only connector,
// fake, is conn, with no sources.
func newInvestigator(t *testing.T, pool *pgxpool.Pool, conn model.Connector, canceller *investigator.Canceller) *investigator.Handler {
	t.Helper()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	guard, err := tools.New(nil, tools.Options{Redactor: redactor, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	set, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	h, err := investigator.New(investigator.Options{
		Pool:       pool,
		Guard:      guard,
		Connectors: map[string]investigator.Connector{"fake": {Connector: conn}},
		Prices:     pricing.Shipped(),
		Strategies: set,
		Routes:     map[string]investigator.Route{"default": {}},
		Rules:      report.Rules{},
		Canceller:  canceller,
	})
	require.NoError(t, err)

	return h
}
