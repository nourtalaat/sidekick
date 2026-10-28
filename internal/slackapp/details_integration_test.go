//go:build integration

package slackapp

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"

	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/roles"
)

// The fixture investigations the forms are about, besides the final one.
const (
	finalRef = "run:1/verdict"
	findRef  = "run:1/finding:f1"
	fixRef   = "run:1/fix:x1"
)

func TestDetailsButton(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role roles.Role
		// openError is the Slack error views.open answers with.
		openError string
		send      clickBy
		wantNotes []string
		// wantForm is the golden file of the form that is opened, empty
		// when none is.
		wantForm string
		// wantRatings are the ratings of the final investigation's verdict
		// at the end, by the user who clicked.
		wantRatings int
	}{
		{
			name:     "Details opens the form",
			send:     clickBy{user: "U01ANA", action: slackout.ActionFeedbackDetails, id: finalInvestigation},
			wantForm: "fixture/forms/open-details.json",
		},
		{
			name:        "Wrong opens the form with Wrong chosen, and stores the rating",
			send:        clickBy{user: "U01ANA", action: slackout.ActionVerdictWrong, id: finalInvestigation},
			wantForm:    "fixture/forms/open-wrong.json",
			wantRatings: 1,
		},
		{
			name:        "Correct opens no form",
			send:        clickBy{user: "U01ANA", action: slackout.ActionVerdictCorrect, id: finalInvestigation},
			wantRatings: 1,
		},
		{
			name:      "a viewer gets no form",
			role:      roles.Viewer,
			send:      clickBy{user: "U01VIEW", action: slackout.ActionFeedbackDetails, id: finalInvestigation},
			wantNotes: []string{noteDenied},
		},
		{
			name:      "a viewer who clicks Wrong gets no form and no rating",
			role:      roles.Viewer,
			send:      clickBy{user: "U01VIEW", action: slackout.ActionVerdictWrong, id: finalInvestigation},
			wantNotes: []string{noteDenied},
		},
		{
			name:      "Details before there is a verdict",
			send:      clickBy{user: "U01ANA", action: slackout.ActionFeedbackDetails, id: queuedInvestigation},
			wantNotes: []string{noteNoVerdict},
		},
		{
			name:      "Wrong before there is a verdict says so once",
			send:      clickBy{user: "U01ANA", action: slackout.ActionVerdictWrong, id: queuedInvestigation},
			wantNotes: []string{noteNoVerdict},
		},
		{
			name:      "Details when Slack does not open the form",
			openError: "expired_trigger_id",
			send:      clickBy{user: "U01ANA", action: slackout.ActionFeedbackDetails, id: finalInvestigation},
			wantNotes: []string{noteFormFailed},
			wantForm:  "fixture/forms/open-details.json",
		},
		{
			name:        "Wrong still stores the rating when Slack does not open the form",
			openError:   "expired_trigger_id",
			send:        clickBy{user: "U01ANA", action: slackout.ActionVerdictWrong, id: finalInvestigation},
			wantNotes:   []string{noteFormFailed},
			wantForm:    "fixture/forms/open-wrong.json",
			wantRatings: 1,
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
			b.openError = tt.openError

			require.Equal(t, http.StatusOK, b.click(t, tt.send, false))
			require.Equal(t, tt.wantNotes, b.notes(t))
			require.Len(t, b.ratings(t), tt.wantRatings)

			b.mu.Lock()
			defer b.mu.Unlock()

			if tt.wantForm == "" {
				require.Empty(t, b.views, "no form is opened")

				return
			}

			want, err := os.ReadFile(tt.wantForm)
			require.NoError(t, err)

			require.Len(t, b.views, 1)
			require.JSONEq(t, string(want), string(b.views[0]))
		})
	}
}

func TestFeedbackFormSubmission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role roles.Role
		// before are forms sent first; they must be stored.
		before []formBy
		send   formBy
		forged bool
		// wantStatus is the status of the answer, 200 when zero, and
		// wantBody its body, empty for an empty ack.
		wantStatus int
		wantBody   string
		check      func(t *testing.T, b *buttonsHarness)
	}{
		{
			name: "a wrong finding with a reason",
			send: formBy{user: "U01ANA", values: detailsValues{Part: findRef, Rating: "wrong", Reason: "Not at 14:02."}},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, []feedbackRow{{Kind: "finding", Ref: findRef, Rating: "wrong", Reason: "Not at 14:02.", Slack: "U01ANA", Channel: "slack"}}, b.feedbackRowsOf(t))
				require.Nil(t, b.investigation(t, finalInvestigation).ConfirmedVerdict, "a finding rating does not confirm")
				require.Equal(t, int64(5), b.publishSeq(t, finalInvestigation), "only a rating of the verdict shows the message again")
				require.InDelta(t, 1, b.feedbackTotal(t, "finding", "wrong"), 0)
			},
		},
		{
			name: "a fix",
			send: formBy{user: "U01ANA", values: detailsValues{Part: fixRef, Rating: "correct"}},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, []feedbackRow{{Kind: "fix", Ref: fixRef, Rating: "correct", Slack: "U01ANA", Channel: "slack"}}, b.feedbackRowsOf(t))
			},
		},
		{
			name: "a tool call",
			send: formBy{user: "U01ANA", values: detailsValues{Part: partToolCall, ToolCall: " call-1 ", Rating: "wrong", Reason: "Wrong service."}},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, []feedbackRow{{Kind: "tool_call", Ref: "call-1", Rating: "wrong", Reason: "Wrong service.", Slack: "U01ANA", Channel: "slack"}}, b.feedbackRowsOf(t))
			},
		},
		{
			name: "a Wrong on the verdict with the real verdict confirms it",
			send: formBy{user: "U01ANA", values: detailsValues{Part: finalRef, Rating: "wrong", Reason: "A load test.", Verdict: "false_positive", Cause: "A load test."}},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				inv := b.investigation(t, finalInvestigation)
				require.Equal(t, "false_positive", deref(inv.ConfirmedVerdict))
				require.Equal(t, "A load test.", deref(inv.ConfirmedCause))
				require.Equal(t, "U01ANA", deref(inv.ConfirmedBySlackUser))
				require.Equal(t, []string{"user U01ANA verdict.confirmed"}, b.auditActions(t, finalInvestigation))
				require.Equal(t, []string{"false_positive"}, b.memoryVerdicts(t))
				require.Equal(t, int64(6), b.publishSeq(t, finalInvestigation), "the rating of the verdict is shown")
				require.Equal(t, 1, b.publishJobs(t))
				require.InDelta(t, 1, b.feedbackCount(t, "wrong"), 0)
			},
		},
		{
			name: "a real verdict on a finding confirms the investigation too",
			send: formBy{user: "U01ANA", values: detailsValues{Part: findRef, Rating: "wrong", Verdict: "inconclusive"}},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, "inconclusive", deref(b.investigation(t, finalInvestigation).ConfirmedVerdict))
				require.Equal(t, []string{"user U01ANA verdict.confirmed"}, b.auditActions(t, finalInvestigation))
				require.Equal(t, int64(5), b.publishSeq(t, finalInvestigation))
			},
		},
		{
			name: "a rating after the Wrong button replaces it and keeps one row",
			before: []formBy{
				{user: "U01ANA", values: detailsValues{Part: finalRef, Rating: "wrong"}},
			},
			send: formBy{user: "U01ANA", values: detailsValues{Part: finalRef, Rating: "wrong", Reason: "It is a load test."}},
			check: func(t *testing.T, b *buttonsHarness) {
				t.Helper()

				require.Equal(t, []feedbackRow{{Kind: "verdict", Ref: finalRef, Rating: "wrong", Reason: "It is a load test.", Slack: "U01ANA", Channel: "slack"}}, b.feedbackRowsOf(t))
				require.InDelta(t, 1, b.feedbackCount(t, "wrong"), 0, "a new reason on the same rating is not counted again")
			},
		},
		{
			name:     "a tool call with no ID",
			send:     formBy{user: "U01ANA", values: detailsValues{Part: partToolCall, Rating: "wrong"}},
			wantBody: errorBody(blockToolCall, errorNoToolCall),
			check:    requireNothingStored,
		},
		{
			name:     "a tool call that is not in the steps",
			send:     formBy{user: "U01ANA", values: detailsValues{Part: partToolCall, ToolCall: "call-9", Rating: "wrong"}},
			wantBody: errorBody(blockToolCall, errorUnknownCall),
			check:    requireNothingStored,
		},
		{
			name:     "a finding the report does not have",
			send:     formBy{user: "U01ANA", values: detailsValues{Part: "run:1/finding:f9", Rating: "wrong"}},
			wantBody: errorBody(blockPart, errorOutOfDate),
			check:    requireNothingStored,
		},
		{
			name:     "a form for another run",
			send:     formBy{user: "U01ANA", values: detailsValues{Part: "run:2/verdict", Rating: "wrong"}},
			wantBody: errorBody(blockPart, errorOutOfDate),
			check:    requireNothingStored,
		},
		{
			name:     "Correct with another real verdict than the reported one",
			send:     formBy{user: "U01ANA", values: detailsValues{Part: finalRef, Rating: "correct", Verdict: "false_positive"}},
			wantBody: errorBody(blockVerdict, errorBadVerdict),
			check:    requireNothingStored,
		},
		{
			name:     "an investigation with no verdict",
			send:     formBy{user: "U01ANA", meta: queuedInvestigation.String(), values: detailsValues{Part: finalRef, Rating: "wrong"}},
			wantBody: errorBody(blockPart, errorNoVerdict),
			check:    requireNothingStored,
		},
		{
			name:     "an investigation that does not exist",
			send:     formBy{user: "U01ANA", meta: "0192d3a4-0000-7000-8000-0000000000ff", values: detailsValues{Part: finalRef, Rating: "wrong"}},
			wantBody: errorBody(blockPart, errorGone),
			check:    requireNothingStored,
		},
		{
			name:     "a form whose investigation is not an ID",
			send:     formBy{user: "U01ANA", meta: "not-an-id", values: detailsValues{Part: finalRef, Rating: "wrong"}},
			wantBody: errorBody(blockPart, errorOutOfDate),
			check:    requireNothingStored,
		},
		{
			name:     "a viewer is refused at the sending",
			role:     roles.Viewer,
			send:     formBy{user: "U01VIEW", values: detailsValues{Part: finalRef, Rating: "wrong"}},
			wantBody: errorBody(blockRating, errorDenied),
			check:    requireNothingStored,
		},
		{
			name:  "another form is ignored",
			send:  formBy{user: "U01ANA", callback: "another_form", values: detailsValues{Part: finalRef, Rating: "wrong"}},
			check: requireNothingStored,
		},
		{
			name:       "a forged signature is refused",
			send:       formBy{user: "U01ANA", values: detailsValues{Part: finalRef, Rating: "wrong"}},
			forged:     true,
			wantStatus: http.StatusUnauthorized,
			check:      requireNothingStored,
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

			for _, f := range tt.before {
				status, body := b.submitForm(t, f, false)
				require.Equal(t, http.StatusOK, status)
				require.Empty(t, body)
			}

			status, body := b.submitForm(t, tt.send, tt.forged)

			wantStatus := tt.wantStatus
			if wantStatus == 0 {
				wantStatus = http.StatusOK
			}

			require.Equal(t, wantStatus, status)

			if tt.wantBody == "" {
				if status == http.StatusOK {
					require.Empty(t, body, "an empty ack closes the form")
				}
			} else {
				require.JSONEq(t, tt.wantBody, body)
			}

			tt.check(t, b)
		})
	}
}

// TestFeedbackFormOnSocketMode sends the form through Socket Mode, which
// answers in the acknowledgement instead of the HTTP response.
func TestFeedbackFormOnSocketMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		send formBy
		// wantAck is the payload of the ack, empty for an empty ack.
		wantAck    string
		wantStored int
	}{
		{
			name:       "a form that is fine is acknowledged empty",
			send:       formBy{user: "U01ANA", values: detailsValues{Part: findRef, Rating: "wrong", Reason: "No."}},
			wantStored: 1,
		},
		{
			name:    "a form with an error is acknowledged with the error",
			send:    formBy{user: "U01ANA", values: detailsValues{Part: partToolCall, ToolCall: "call-9", Rating: "wrong"}},
			wantAck: errorBody(blockToolCall, errorUnknownCall),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := setupButtons(t, roles.Responder)
			f := newFakeSocket(t, b.slack)

			startSocket(t, b.harness, b.pool, &holders{}, func(o *SocketOptions) { o.Interactions = b.buttons })
			waitFor(t, func() bool { open, _, _ := f.counts(); return open == 1 })

			payload, err := json.Marshal(b.formInteraction(tt.send))
			require.NoError(t, err)

			f.sendRequest(socketmode.RequestTypeInteractive, "env-form", 0, payload)
			waitFor(t, func() bool { return f.acked("env-form") })

			if tt.wantAck == "" {
				require.Nil(t, f.ackPayload("env-form"))
			} else {
				require.JSONEq(t, tt.wantAck, string(f.ackPayload("env-form")))
			}

			require.Len(t, b.feedbackRowsOf(t), tt.wantStored)
		})
	}
}

// formBy is the sending of the feedback form by a user.
type formBy struct {
	user string
	// callback is the callback ID of the form, the feedback form's when
	// empty, and meta its private metadata, the final investigation's ID
	// when empty.
	callback string
	meta     string
	values   detailsValues
}

// feedbackRow is a stored feedback row as the tests compare it.
type feedbackRow struct {
	Kind, Ref, Rating, Reason, Slack, Channel string
}

// submitForm sends f to POST /slack/interactivity as Slack does when a
// person sends a form, and returns the status and the body of the answer.
func (b *buttonsHarness) submitForm(t *testing.T, f formBy, forged bool) (int, string) {
	t.Helper()

	return b.post(t, b.formInteraction(f), forged)
}

// formInteraction is the view_submission payload of f.
func (b *buttonsHarness) formInteraction(f formBy) map[string]any {
	callback := f.callback
	if callback == "" {
		callback = callbackFeedbackDetails
	}

	meta := f.meta
	if meta == "" {
		meta = finalInvestigation.String()
	}

	return map[string]any{
		"type":       "view_submission",
		"team":       map[string]string{"id": "T01TEAM"},
		"user":       map[string]string{"id": f.user},
		"api_app_id": "A01SIDEKICK",
		"trigger_id": testTriggerID,
		"view": map[string]any{
			"id": "V01FORM", "type": "modal", "callback_id": callback, "private_metadata": meta,
			"state": map[string]any{"values": formState(f.values)},
		},
	}
}

// formState is the state of a form with the values d, as Slack sends it.
func formState(d detailsValues) map[string]any {
	text := func(action, value string) map[string]any {
		return map[string]any{action: map[string]any{"type": "plain_text_input", "value": value}}
	}

	choice := func(action, kind, value string) map[string]any {
		if value == "" {
			return map[string]any{action: map[string]any{"type": kind, "selected_option": nil}}
		}

		return map[string]any{action: map[string]any{"type": kind, "selected_option": map[string]any{"value": value}}}
	}

	return map[string]any{
		blockPart:     choice(actionPart, "static_select", d.Part),
		blockToolCall: text(actionToolCall, d.ToolCall),
		blockRating:   choice(actionRating, "radio_buttons", d.Rating),
		blockReason:   text(actionReason, d.Reason),
		blockVerdict:  choice(actionVerdict, "static_select", d.Verdict),
		blockCause:    text(actionCause, d.Cause),
	}
}

// errorBody is the answer that shows one error in the form.
func errorBody(block, message string) string {
	body, err := json.Marshal(map[string]any{"response_action": "errors", "errors": map[string]string{block: message}})
	if err != nil {
		panic(err)
	}

	return string(body)
}

// feedbackRowsOf returns the feedback rows of the final investigation,
// oldest first.
func (b *buttonsHarness) feedbackRowsOf(t *testing.T) []feedbackRow {
	t.Helper()

	rows := collect[feedbackRow](t, b.pool,
		`SELECT target_kind, target_ref, rating, coalesce(reason, ''), coalesce(slack_user_id, ''), channel
		 FROM feedback WHERE investigation_id = '`+finalInvestigation.String()+`' ORDER BY created_at, id`)

	return rows
}

// memoryVerdicts returns the verdicts of the memory entries of the final
// investigation.
func (b *buttonsHarness) memoryVerdicts(t *testing.T) []string {
	t.Helper()

	rows, err := b.pool.Query(t.Context(), "SELECT verdict FROM memory_entries WHERE investigation_id = $1", finalInvestigation)
	require.NoError(t, err)

	verdicts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return verdicts
}

// feedbackTotal returns sidekick_feedback_total for target and rating, or
// 0 when the series does not exist.
func (b *buttonsHarness) feedbackTotal(t *testing.T, target, rating string) float64 {
	t.Helper()

	families, err := b.reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() != "sidekick_feedback_total" {
			continue
		}

		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}

			if labels["target"] == target && labels["rating"] == rating {
				return m.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// requireNothingStored checks that the form stored no feedback and changed
// no confirmation.
func requireNothingStored(t *testing.T, b *buttonsHarness) {
	t.Helper()

	require.Empty(t, b.feedbackRowsOf(t))
	require.Nil(t, b.investigation(t, finalInvestigation).ConfirmedVerdict)
	require.Empty(t, b.auditActions(t, finalInvestigation))
}

// deref returns the string s points to, or "".
func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
