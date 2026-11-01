package summarize

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

const (
	testToolName  = "submit_lesson"
	testRoute     = "payments"
	testConnector = "claude-small"
	testModel     = "claude-small-1"
)

// today is the day the tests run on.
var (
	testNow = time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC)
	today   = pgtype.Date{Time: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Valid: true}
)

// testPrices price testModel at 3 US dollars per million input tokens and 15
// per million output tokens.
var testPrices = pricing.New(map[string]model.Price{
	testModel: {InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000},
})

func testTool() model.ToolSpec {
	return model.ToolSpec{Name: testToolName, Description: "Submit a lesson.", InputSchema: []byte(`{"type":"object"}`)}
}

func testRequest() Request {
	return Request{
		Route:     testRoute,
		System:    "Draft a lesson.",
		Messages:  []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: "The feedback."}}}},
		Tool:      testTool(),
		MaxTokens: 500,
	}
}

func TestCallTool(t *testing.T) {
	t.Parallel()

	usage := model.Usage{InputTokens: 1000, OutputTokens: 200}
	wantSpend := db.AddSpendParams{
		Day: today, Route: testRoute, Connector: testConnector, Model: testModel,
		CostMicros: 6000, InputTokens: 1000, OutputTokens: 200,
	}

	tests := []struct {
		name string
		// off builds the Caller without a connector.
		off      bool
		caps     model.Capabilities
		daily    Daily
		modelID  string
		turns    []modeltest.Turn
		setup    func(l *Mockledger)
		wantErr  error
		wantJSON string
		// check looks at the request the model got.
		check func(t *testing.T, req model.Request)
	}{
		{
			name:    "a Caller without a connector is off",
			off:     true,
			wantErr: ErrDisabled,
		},
		{
			name:     "the tool is forced where the connector allows it",
			caps:     model.Capabilities{ToolCalling: true, ToolChoice: true},
			turns:    []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", testToolName, `{"text":"Check the deploy."}`))},
			setup:    func(l *Mockledger) { l.EXPECT().addSpend(gomock.Any(), wantSpend).Return(nil) },
			wantJSON: `{"text":"Check the deploy."}`,
			check: func(t *testing.T, req model.Request) {
				t.Helper()
				require.Equal(t, model.ToolChoice{Mode: model.ToolChoiceRequired, Name: testToolName}, req.ToolChoice)
				require.Equal(t, []model.Text{{Text: "Draft a lesson."}}, req.System)
				require.Equal(t, []model.ToolSpec{testTool()}, req.Tools)
				require.Equal(t, 500, req.MaxTokens)
				require.Equal(t, map[string]string{model.MetadataRole: "summarize", model.MetadataRoute: testRoute}, req.Metadata)
			},
		},
		{
			name:     "the system text asks for the tool where the connector cannot be forced",
			caps:     model.Capabilities{ToolCalling: true},
			turns:    []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", testToolName, `{"text":"x"}`))},
			setup:    func(l *Mockledger) { l.EXPECT().addSpend(gomock.Any(), wantSpend).Return(nil) },
			wantJSON: `{"text":"x"}`,
			check: func(t *testing.T, req model.Request) {
				t.Helper()
				require.Equal(t, model.ToolChoice{}, req.ToolChoice)
				require.Contains(t, req.System[0].Text, "Draft a lesson.")
				require.Contains(t, req.System[0].Text, "calling the submit_lesson tool exactly once")
			},
		},
		{
			name:  "a call under the route's cap goes through",
			caps:  model.Capabilities{ToolChoice: true},
			daily: Daily{RouteMicros: map[string]int64{testRoute: 10_000}},
			turns: []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", testToolName, `{}`))},
			setup: func(l *Mockledger) {
				l.EXPECT().dailySpend(gomock.Any(), testRoute, today).Return(spent{route: 9_999, total: 50_000}, nil)
				l.EXPECT().addSpend(gomock.Any(), wantSpend).Return(nil)
			},
			wantJSON: `{}`,
		},
		{
			name:  "a route at its cap is refused before the model is called",
			caps:  model.Capabilities{ToolChoice: true},
			daily: Daily{RouteMicros: map[string]int64{testRoute: 10_000}},
			setup: func(l *Mockledger) {
				l.EXPECT().dailySpend(gomock.Any(), testRoute, today).Return(spent{route: 10_000, total: 10_000}, nil)
			},
			wantErr: ErrOverCap,
		},
		{
			name:  "the global cap refuses a call too",
			caps:  model.Capabilities{ToolChoice: true},
			daily: Daily{MaxCostMicros: 100_000, RouteMicros: map[string]int64{testRoute: 10_000}},
			setup: func(l *Mockledger) {
				l.EXPECT().dailySpend(gomock.Any(), testRoute, today).Return(spent{route: 1, total: 100_000}, nil)
			},
			wantErr: ErrOverCap,
		},
		{
			name:  "another route's cap does not count",
			caps:  model.Capabilities{ToolChoice: true},
			daily: Daily{RouteMicros: map[string]int64{"other": 1}},
			turns: []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", testToolName, `{}`))},
			// No daily spend is read: no cap applies to this route.
			setup:    func(l *Mockledger) { l.EXPECT().addSpend(gomock.Any(), wantSpend).Return(nil) },
			wantJSON: `{}`,
		},
		{
			name:  "a failure to read the spend is an error",
			caps:  model.Capabilities{ToolChoice: true},
			daily: Daily{MaxCostMicros: 1},
			setup: func(l *Mockledger) {
				l.EXPECT().dailySpend(gomock.Any(), testRoute, today).Return(spent{}, errors.New("database down"))
			},
			wantErr: errors.New("read the daily spend"),
		},
		{
			name:    "the response's model prices the call when none is configured",
			caps:    model.Capabilities{ToolChoice: true},
			modelID: "-",
			turns: []modeltest.Turn{{Response: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{modeltest.Call("c1", testToolName, `{}`)}},
				Usage:   usage,
				Model:   testModel,
			}}},
			setup: func(l *Mockledger) {
				l.EXPECT().addSpend(gomock.Any(), wantSpend).Return(nil)
			},
			wantJSON: `{}`,
		},
		{
			name:  "a model with no price costs nothing",
			caps:  model.Capabilities{ToolChoice: true},
			turns: []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", testToolName, `{}`))},
			setup: func(l *Mockledger) {
				unpriced := wantSpend
				unpriced.Model, unpriced.CostMicros = "mystery-1", 0
				l.EXPECT().addSpend(gomock.Any(), unpriced).Return(nil)
			},
			modelID:  "mystery-1",
			wantJSON: `{}`,
		},
		{
			name:  "no tool call is an error, and the call is still paid for",
			caps:  model.Capabilities{ToolChoice: true},
			turns: []modeltest.Turn{modeltest.Text(usage, "Here is a lesson.")},
			setup: func(l *Mockledger) {
				l.EXPECT().addSpend(gomock.Any(), wantSpend).Return(nil)
			},
			wantErr: ErrNoToolCall,
		},
		{
			name:  "a call to another tool is not the answer",
			caps:  model.Capabilities{ToolChoice: true},
			turns: []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", "other_tool", `{}`))},
			setup: func(l *Mockledger) {
				l.EXPECT().addSpend(gomock.Any(), wantSpend).Return(nil)
			},
			wantErr: ErrNoToolCall,
		},
		{
			name:    "a failed model call adds nothing to the spend",
			caps:    model.Capabilities{ToolChoice: true},
			turns:   []modeltest.Turn{modeltest.Fail(&model.Error{Kind: model.ErrInvalid, Err: errors.New("bad request")})},
			wantErr: &model.Error{},
		},
		{
			name:  "a failure to save the spend is an error",
			caps:  model.Capabilities{ToolChoice: true},
			turns: []modeltest.Turn{modeltest.ToolUse(usage, modeltest.Call("c1", testToolName, `{}`))},
			setup: func(l *Mockledger) {
				l.EXPECT().addSpend(gomock.Any(), wantSpend).Return(errors.New("database down"))
			},
			wantErr: errors.New("add the call to the daily spend"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ledger := NewMockledger(gomock.NewController(t))
			if tt.setup != nil {
				tt.setup(ledger)
			}

			conn := modeltest.New(t, tt.caps, tt.turns...)
			conn.ExpectAllUsed()

			opts := Options{Name: testConnector, Model: testModel, Prices: testPrices, Daily: tt.daily, Now: func() time.Time { return testNow }}
			if !tt.off {
				opts.Connector = conn
			}

			switch tt.modelID {
			case "":
			case "-":
				opts.Model = ""
			default:
				opts.Model = tt.modelID
			}

			got, err := New(opts).callTool(t.Context(), ledger, testRequest())

			switch {
			case tt.wantErr == nil:
				require.NoError(t, err)
				require.JSONEq(t, tt.wantJSON, string(got))
			case errors.Is(tt.wantErr, ErrDisabled), errors.Is(tt.wantErr, ErrOverCap), errors.Is(tt.wantErr, ErrNoToolCall):
				require.ErrorIs(t, err, tt.wantErr)
			default:
				var merr *model.Error
				if errors.As(tt.wantErr, &merr) {
					require.ErrorAs(t, err, &merr)
				} else {
					require.ErrorContains(t, err, tt.wantErr.Error())
				}
			}

			if tt.check != nil {
				require.Len(t, conn.Requests(), 1)
				tt.check(t, conn.Requests()[0])
			}
		})
	}
}

func TestEnabled(t *testing.T) {
	t.Parallel()

	var nilCaller *Caller

	require.False(t, nilCaller.Enabled())
	require.False(t, New(Options{}).Enabled())
	require.True(t, New(Options{Connector: modeltest.New(t, model.Capabilities{})}).Enabled())
}

func TestNextDay(t *testing.T) {
	t.Parallel()

	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	tests := []struct {
		name string
		loc  *time.Location
		now  time.Time
		want time.Time
	}{
		{name: "UTC by default", now: testNow, want: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
		{
			name: "midnight in the caps' time zone",
			loc:  paris,
			now:  time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC),
			want: time.Date(2026, 11, 3, 0, 0, 0, 0, paris),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := New(Options{Daily: Daily{Location: tt.loc}, Now: func() time.Time { return tt.now }})
			require.True(t, c.NextDay().Equal(tt.want), "got %s, want %s", c.NextDay(), tt.want)
		})
	}
}
