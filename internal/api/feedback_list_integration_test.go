//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// feedbackPart is a part of GET /investigations/{id}/feedback, as the tests
// read it.
type feedbackPart struct {
	TargetKind string `json:"targetKind"`
	TargetRef  string `json:"targetRef"`
	Correct    int    `json:"correct"`
	Wrong      int    `json:"wrong"`
	Mine       *struct {
		Rating         string  `json:"rating"`
		Reason         *string `json:"reason"`
		CorrectVerdict *string `json:"correctVerdict"`
		CorrectCause   *string `json:"correctCause"`
	} `json:"mine"`
}

func TestListInvestigationFeedback(t *testing.T) {
	t.Parallel()

	s := setupFeedback(t)

	// inv1 is in its second run. Its fixture has three ratings of the
	// verdict of run 2 by Slack users (two correct, one wrong), and one of
	// the verdict of run 1, which the list leaves out.
	verdict := feedbackPart{TargetKind: "verdict", TargetRef: "run:2/verdict", Correct: 2, Wrong: 1}

	read := func(t *testing.T, id, session string) []feedbackPart {
		t.Helper()

		rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+id+"/feedback", session, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		return decodeParts(t, rec)
	}

	require.Equal(t, []feedbackPart{verdict}, read(t, inv1, viSession), "a viewer sees the counts of the current run")
	require.Empty(t, read(t, inv2, viSession), "no feedback gives an empty list")

	for _, body := range []string{
		`{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"wrong","reason":"It was a load test.","correctVerdict":"false_positive","correctCause":"A load test."}`,
		`{"targetKind":"finding","targetRef":"run:2/finding:f1","rating":"correct"}`,
	} {
		rec := postFeedback(t, s, inv1, faSession, faCSRF, body)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}

	rec := postFeedback(t, s, inv3, faSession, faCSRF, `{"targetKind":"tool_call","targetRef":"c1","rating":"wrong"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	mine := read(t, inv1, faSession)
	require.Len(t, mine, 2)

	require.Equal(t, "finding", mine[0].TargetKind)
	require.Equal(t, "run:2/finding:f1", mine[0].TargetRef)
	require.Equal(t, 1, mine[0].Correct)
	require.Zero(t, mine[0].Wrong)
	require.NotNil(t, mine[0].Mine)
	require.Equal(t, "correct", mine[0].Mine.Rating)

	require.Equal(t, "run:2/verdict", mine[1].TargetRef)
	require.Equal(t, 2, mine[1].Correct)
	require.Equal(t, 2, mine[1].Wrong, "the caller's own rating counts")
	require.NotNil(t, mine[1].Mine)
	require.Equal(t, "wrong", mine[1].Mine.Rating)
	require.Equal(t, new("It was a load test."), mine[1].Mine.Reason)
	require.Equal(t, new("false_positive"), mine[1].Mine.CorrectVerdict)
	require.Equal(t, new("A load test."), mine[1].Mine.CorrectCause)

	others := read(t, inv1, fbSession)
	require.Len(t, others, 2)
	require.Equal(t, 2, others[1].Wrong, "another user sees the same counts")

	for _, part := range others {
		require.Nil(t, part.Mine, "and not the first user's opinion")
	}

	calls := read(t, inv3, faSession)
	require.Len(t, calls, 1)
	require.Equal(t, "tool_call", calls[0].TargetKind)
	require.Equal(t, "c1", calls[0].TargetRef)
	require.Equal(t, 1, calls[0].Wrong)
	require.NotNil(t, calls[0].Mine)

	token := s.doWithToken(t, http.MethodGet, "/api/v1/investigations/"+inv1+"/feedback", "skt_viewer", nil)
	require.Equal(t, http.StatusOK, token.Code)

	for _, part := range decodeParts(t, token) {
		require.Nil(t, part.Mine, "an API token has no opinion of its own")
	}
}

func TestListInvestigationFeedbackRefuses(t *testing.T) {
	t.Parallel()

	s := setupFeedback(t)

	tests := []struct {
		name       string
		id         string
		session    string
		wantStatus int
		wantDetail string
	}{
		{name: "no session", id: inv1, wantStatus: http.StatusUnauthorized, wantDetail: "Sign in, or send an API token."},
		{
			name: "an investigation that does not exist", id: "0192d3a4-0000-7000-8000-0000000000ff", session: viSession,
			wantStatus: http.StatusNotFound, wantDetail: "No investigation has this ID.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := "/api/v1/investigations/" + tt.id + "/feedback"

			rec := s.do(t, http.MethodGet, path, tt.session, "", nil)
			require.Equal(t, tt.wantStatus, rec.Code)
			require.JSONEq(t, problem(tt.wantStatus, tt.wantDetail, path), rec.Body.String())
		})
	}
}

// decodeParts reads the parts of an answer of GET /investigations/{id}/feedback.
func decodeParts(t *testing.T, rec *httptest.ResponseRecorder) []feedbackPart {
	t.Helper()

	var body struct {
		Parts []feedbackPart `json:"parts"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Parts, "an empty list is [], not null")

	return body.Parts
}
