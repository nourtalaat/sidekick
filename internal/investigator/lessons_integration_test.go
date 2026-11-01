//go:build integration

package investigator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/lessons"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// The lessons of insertLessons, by the numbers in their IDs.
const (
	lessonPool     = 1 // service checkout-api
	lessonGlobal   = 2 // no scope
	lessonOther    = 3 // service search-api, which does not apply
	lessonStrategy = 4 // strategy latency-alert
)

const insertLessons = `INSERT INTO lessons (id, text, scope_service, scope_strategy, status, origin, helpful, created_at)
VALUES
  ('0192d3a4-0000-7000-8000-000000000001', 'Check the pool size.', 'checkout-api', NULL, 'active', 'ui', 2, '2026-10-01 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000002', 'Say what you could not check.', NULL, NULL, 'active', 'ui', 0, '2026-10-02 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000003', 'Only for search.', 'search-api', NULL, 'active', 'ui', 0, '2026-10-03 00:00:00+00'),
  ('0192d3a4-0000-7000-8000-000000000004', 'Compare p99 with p50.', NULL, 'latency-alert', 'active', 'ui', 0, '2026-10-04 00:00:00+00')`

// lessonID returns the ID of the lesson with number n in insertLessons.
func lessonID(n int) uuid.UUID {
	return uuid.MustParse("0192d3a4-0000-7000-8000-00000000000" + string(rune('0'+n)))
}

var defaultLessonLimits = lessons.Limits{MaxPerPrompt: 10, MaxTokens: 1500, GlobalPercent: 30}

func TestRunShowsLessons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		selector *lessons.Selector
		// inPrompt are the numbers of the lessons the first request shows,
		// in order, and notInPrompt those it must not show.
		inPrompt, notInPrompt []int
		// used are the lessons recorded as used, and leftOut the ones the
		// prompt step lists as left out, with their reasons.
		used    []int
		leftOut map[int]string
	}{
		{
			name:        "the lessons that apply are in the prompt and recorded",
			selector:    lessons.NewSelector(defaultLessonLimits),
			inPrompt:    []int{lessonPool, lessonGlobal},
			notInPrompt: []int{lessonOther, lessonStrategy},
			used:        []int{lessonPool, lessonGlobal},
		},
		{
			name:        "a lesson past the limit is listed as left out",
			selector:    lessons.NewSelector(lessons.Limits{MaxPerPrompt: 1, MaxTokens: 1500, GlobalPercent: 30}),
			inPrompt:    []int{lessonPool},
			notInPrompt: []int{lessonGlobal, lessonOther, lessonStrategy},
			used:        []int{lessonPool},
			leftOut:     map[int]string{lessonGlobal: lessons.ReasonMaxPerPrompt},
		},
		{
			name:        "global lessons past their share are listed as left out",
			selector:    lessons.NewSelector(lessons.Limits{MaxPerPrompt: 10, MaxTokens: 1500, GlobalPercent: 0}),
			inPrompt:    []int{lessonPool},
			notInPrompt: []int{lessonGlobal},
			used:        []int{lessonPool},
			leftOut:     map[int]string{lessonGlobal: lessons.ReasonGlobalShare},
		},
		{
			name:        "without a selector the prompt has no lessons",
			notInPrompt: []int{lessonPool, lessonGlobal, lessonOther, lessonStrategy},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{turns: []modeltest.Turn{
				turn(search("c1")),
				turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
			}})
			h.handler.opts.Lessons = tt.selector

			_, err := h.pool.Exec(t.Context(), insertLessons)
			require.NoError(t, err)

			require.NoError(t, h.run(t))

			text := userText(h.conn.Requests()[0].Messages[0])
			require.Equal(t, len(tt.inPrompt) > 0, strings.Contains(text, "# Lessons from past feedback"))

			last := -1

			for _, n := range tt.inPrompt {
				at := strings.Index(text, lessonText(n))
				require.Greater(t, at, last, "lesson %d is in the prompt, after the one before it", n)

				last = at
			}

			for _, n := range tt.notInPrompt {
				require.NotContains(t, text, lessonText(n))
			}

			require.Equal(t, lessonIDs(tt.used...), h.usedLessons(t))

			prompt := h.steps(t)[0]
			require.Equal(t, "prompt", prompt.Kind)

			if tt.selector == nil {
				require.Nil(t, prompt.Lessons, "lessons are off, so the step records none")

				return
			}

			var rec lessons.StepRecord
			require.NoError(t, json.Unmarshal(prompt.Lessons, &rec))
			require.Equal(t, lessonIDs(tt.used...), rec.Used)
			require.Len(t, rec.LeftOut, len(tt.leftOut))

			for _, l := range rec.LeftOut {
				found := false

				for n, reason := range tt.leftOut {
					if lessonID(n) == l.ID {
						require.Equal(t, reason, l.Reason)

						found = true
					}
				}

				require.True(t, found, "left-out lesson %s is expected", l.ID)
			}
		})
	}
}

func TestTriageShowsLessons(t *testing.T) {
	t.Parallel()

	h := setup(t, options{
		triageTurns: []modeltest.Turn{triageTurn(triageLatency)},
		turns:       []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, notSure))},
	})
	h.handler.opts.Lessons = lessons.NewSelector(defaultLessonLimits)

	_, err := h.pool.Exec(t.Context(), insertLessons)
	require.NoError(t, err)

	require.NoError(t, h.run(t))

	// Triage sees the lessons for the route's strategy, generic-alert: the
	// lesson for the strategy it then chooses, latency-alert, is for the
	// full investigation.
	triage := userText(h.triage.Requests()[0].Messages[0])
	require.Contains(t, triage, lessonText(lessonPool))
	require.Contains(t, triage, lessonText(lessonGlobal))
	require.NotContains(t, triage, lessonText(lessonStrategy))

	full := userText(h.conn.Requests()[0].Messages[0])
	require.Contains(t, full, lessonText(lessonPool))
	require.Contains(t, full, lessonText(lessonGlobal))
	require.Contains(t, full, lessonText(lessonStrategy))
	require.NotContains(t, full, lessonText(lessonOther))

	// Both steps record their own selection; the investigation used the
	// lessons of both once each.
	byKind := map[string]lessons.StepRecord{}

	for _, s := range h.steps(t) {
		if s.Kind != "triage_prompt" && s.Kind != "prompt" {
			continue
		}

		var rec lessons.StepRecord
		require.NoError(t, json.Unmarshal(s.Lessons, &rec), s.Kind)

		byKind[s.Kind] = rec
	}

	require.Equal(t, lessonIDs(lessonPool, lessonGlobal), byKind["triage_prompt"].Used)
	require.Equal(t, lessonIDs(lessonPool, lessonStrategy, lessonGlobal), byKind["prompt"].Used)
	require.ElementsMatch(t, lessonIDs(lessonPool, lessonGlobal, lessonStrategy), h.usedLessons(t))
}

// lessonText is the text of the lesson with number n in insertLessons.
func lessonText(n int) string {
	return map[int]string{
		lessonPool:     "Check the pool size.",
		lessonGlobal:   "Say what you could not check.",
		lessonOther:    "Only for search.",
		lessonStrategy: "Compare p99 with p50.",
	}[n]
}

// lessonIDs returns the IDs of the lessons with the numbers.
func lessonIDs(numbers ...int) []uuid.UUID {
	out := make([]uuid.UUID, len(numbers))
	for i, n := range numbers {
		out[i] = lessonID(n)
	}

	return out
}

// usedLessons returns the IDs of the lessons the test investigation used,
// sorted. It returns an empty slice when it used none.
func (h *harness) usedLessons(t *testing.T) []uuid.UUID {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT lesson_id FROM lesson_uses WHERE investigation_id = $1 ORDER BY lesson_id", testInvestigation)
	require.NoError(t, err)

	defer rows.Close()

	out := []uuid.UUID{}

	for rows.Next() {
		var id uuid.UUID

		require.NoError(t, rows.Scan(&id))

		out = append(out, id)
	}

	require.NoError(t, rows.Err())

	return out
}
