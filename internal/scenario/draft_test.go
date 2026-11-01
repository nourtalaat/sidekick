package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/scrub"
	"github.com/nourtalaat/sidekick/internal/source"
)

var testKey = []byte("test-hash-key")

// seed is the investigation the tests export: its confirmed verdict, the
// event that opened it and its recorded source calls.
type seed struct {
	InvestigationID uuid.UUID         `json:"investigation_id"`
	Route           string            `json:"route"`
	Strategy        string            `json:"strategy"`
	Title           string            `json:"title"`
	Verdict         report.Verdict    `json:"verdict"`
	Cause           string            `json:"cause"`
	SourceTypes     map[string]string `json:"source_types"`
	Event           struct {
		Intake     string          `json:"intake"`
		Adapter    string          `json:"adapter"`
		ReceivedAt time.Time       `json:"received_at"`
		Payload    json.RawMessage `json:"payload"`
	} `json:"event"`
	Calls []seedCall `json:"calls"`
}

// seedCall is one recorded source call of the seed.
type seedCall struct {
	Seq      int32             `json:"seq"`
	Source   string            `json:"source"`
	Tool     string            `json:"tool"`
	Name     string            `json:"name"`
	Input    json.RawMessage   `json:"input"`
	Status   string            `json:"status"`
	Text     string            `json:"text"`
	Evidence []source.Evidence `json:"evidence"`
}

// loadSeed reads fixture/seed.json.
func loadSeed(t *testing.T) seed {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("fixture", "seed.json")) //nolint:gosec // A fixture file.
	require.NoError(t, err)

	var s seed
	require.NoError(t, json.Unmarshal(raw, &s))

	return s
}

// input is the seed as the builder's input.
func (s seed) input() Input {
	in := Input{
		InvestigationID: s.InvestigationID, Route: s.Route, Strategy: s.Strategy, Title: s.Title,
		Verdict: s.Verdict, Cause: s.Cause, SourceTypes: s.SourceTypes,
		Event: EventInput{Adapter: s.Event.Adapter, Payload: s.Event.Payload, ReceivedAt: s.Event.ReceivedAt},
	}

	for i, c := range s.Calls {
		in.Calls = append(in.Calls, investigator.RecordedCall{
			Seq: c.Seq, CallID: "c" + string(rune('1'+i)), Source: c.Source, Tool: c.Tool, Name: c.Name,
			Input: c.Input, Status: c.Status, Text: c.Text, Evidence: c.Evidence,
		})
	}

	return in
}

// wantCleanedCause is the seed's cause once it is cleaned.
const wantCleanedCause = "DB connection pool exhausted on host-1 after commit abc1234def5678 by user-1 lowered the pool size to 5; host-4 (ip-1) refused new connections."

// newTestBuilder builds a Builder over a cleaner.
func newTestBuilder(t *testing.T, rubric RubricDrafter) (*Builder, *scrub.Cleaner) {
	t.Helper()

	cleaner, err := scrub.New(scrub.Options{HashKey: testKey})
	require.NoError(t, err)

	return NewBuilder(cleaner, rubric), cleaner
}

func TestBuildGolden(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	rubric := NewMockRubricDrafter(ctrl)
	rubric.EXPECT().Enabled().Return(true)
	rubric.EXPECT().Draft(gomock.Any(), gomock.Nil(), "default", "true_positive", wantCleanedCause).Return([]bench.RubricItem{
		{ID: "x", Text: "Names DB connection pool exhaustion on checkout-7f9c.prod.acme.com as the cause", Weight: 2, Required: true},
		{ID: "y", Text: "Links the cause to commit abc1234def5678", Weight: 2, Required: true},
	}, nil)

	builder, cleaner := newTestBuilder(t, rubric)

	got, err := builder.Build(t.Context(), nil, loadSeed(t).input())
	require.NoError(t, err)

	want, err := os.ReadFile(filepath.Join("fixture", "golden.yaml")) //nolint:gosec // A fixture file.
	require.NoError(t, err)

	require.Equal(t, string(want), string(got.Body))
	require.Equal(t, "latency-alert-checkout-api-p99-latency-above-2s-for-5m-3f6e8d1c4b70", got.ID)
	require.Equal(t, "checkout-api p99 latency above 2s for 5m", got.Title)

	require.Equal(t, []Provenance{
		{Source: "github", Fixture: 0, Response: 0, Seq: 6},
		{Source: "github", Fixture: 1, Response: 0, Seq: 4},
		{Source: "prometheus", Fixture: 0, Response: 0, Seq: 10},
		{Source: "signoz", Fixture: 0, Response: 0, Seq: 2},
		{Source: "signoz", Fixture: 0, Response: 1, Seq: 12},
	}, got.Provenance)

	require.Equal(t, []Note{
		{Kind: NoteSkippedCalls, Text: "Calls left out because they have no answer to replay: 1 with status error."},
		{Kind: NoteMergedCalls, Text: "signoz search_logs: 1 more call(s) had the same loosened match, so one fixture answers them in call order."},
		{Kind: NoteDroppedArgs, Text: "github list_commits: the fixture does not match on since."},
		{Kind: NoteDroppedArgs, Text: "prometheus query: the fixture does not match on time."},
		{Kind: NoteDroppedArgs, Text: "signoz search_logs: the fixture does not match on end, limit, query, start."},
		{Kind: NoteUnavailable, Text: "k8s was unavailable in the investigation, so the scenario marks it unavailable and expects the report to say so."},
	}, got.Notes)

	// The draft is a valid scenario and holds nothing sensitive.
	sc, err := bench.ParseScenario(got.Body)
	require.NoError(t, err)
	require.Equal(t, report.TruePositive, sc.Expected.Verdict)
	require.Equal(t, []string{"k8s"}, sc.Expected.UnavailableReported)

	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal(got.Body, &doc))
	require.NoError(t, cleaner.Check(&doc))

	for _, value := range []string{
		"dana.reyes", "checkout-7f9c", "orders-db-1", "prometheus.acme.internal", "signoz.acme.com",
		"10.42.7.19", "checkout_svc", "acme.com", "@",
	} {
		require.NotContains(t, string(got.Body), value)
	}
}

func TestBuildWithoutRubric(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rubric   func(ctrl *gomock.Controller) RubricDrafter
		cause    string
		wantNote string
	}{
		{name: "no drafter", wantNote: NoteNoRubric},
		{
			name: "drafter that is off",
			rubric: func(ctrl *gomock.Controller) RubricDrafter {
				d := NewMockRubricDrafter(ctrl)
				d.EXPECT().Enabled().Return(false)

				return d
			},
			wantNote: NoteNoRubric,
		},
		{
			name: "drafter that fails",
			rubric: func(ctrl *gomock.Controller) RubricDrafter {
				d := NewMockRubricDrafter(ctrl)
				d.EXPECT().Enabled().Return(true)
				d.EXPECT().Draft(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, os.ErrInvalid)

				return d
			},
			wantNote: NoteNoRubric,
		},
		{name: "no confirmed cause", cause: "-", wantNote: NoteNoCause},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var rubric RubricDrafter
			if tt.rubric != nil {
				rubric = tt.rubric(gomock.NewController(t))
			}

			builder, _ := newTestBuilder(t, rubric)

			in := loadSeed(t).input()
			if tt.cause != "" {
				in.Cause = ""
			}

			got, err := builder.Build(t.Context(), nil, in)
			require.NoError(t, err)

			sc, err := bench.ParseScenario(got.Body)
			require.NoError(t, err)
			require.Empty(t, sc.Expected.RootCause.Rubric)
			require.Equal(t, tt.wantNote, got.Notes[len(got.Notes)-1].Kind)
		})
	}
}

func TestBuildErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(in *Input)
		want   error
	}{
		{name: "not confirmed", change: func(in *Input) { in.Verdict = "" }, want: ErrNotConfirmed},
		{name: "no source calls", change: func(in *Input) { in.Calls = nil }, want: ErrNoSourceCalls},
		{
			name: "only calls that failed still list the source",
			change: func(in *Input) {
				in.Calls = in.Calls[len(in.Calls)-1:]
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder, _ := newTestBuilder(t, nil)

			in := loadSeed(t).input()
			tt.change(&in)

			_, err := builder.Build(t.Context(), nil, in)
			if tt.want == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestScenarioID(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("0198f2a4-7c3e-7b1d-9a52-3f6e8d1c4b70")

	tests := []struct {
		name     string
		title    string
		strategy string
		want     string
	}{
		{name: "strategy and title", title: "Cart: error rate!", strategy: "error-rate-alert", want: "error-rate-alert-cart-error-rate-3f6e8d1c4b70"},
		{name: "no title words", title: "???", want: "scenario-3f6e8d1c4b70"},
		{
			name:  "long title is cut",
			title: "a very long alert title that goes on and on and on and on and on and on and on",
			want:  "a-very-long-alert-title-that-goes-on-and-on-and-on-and-on-an-3f6e8d1c4b70",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, scenarioID(tt.title, tt.strategy, id))
		})
	}
}
