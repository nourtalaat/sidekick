package modeltest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

var errOverloaded = &model.Error{Kind: model.ErrOverloaded, Err: errors.New("busy")}

func TestConnector(t *testing.T) {
	t.Parallel()

	usage := model.Usage{InputTokens: 10, OutputTokens: 2}

	tests := []struct {
		name  string
		turns []Turn
		want  []model.Response
		// wantErrs holds the error of each call, nil for none.
		wantErrs []error
	}{
		{
			name:  "turns in order",
			turns: []Turn{ToolUse(usage, Call("c1", "search", `{}`)), Text(usage, "done")},
			want: []model.Response{
				{Message: model.Message{Role: model.Assistant, Parts: []model.Part{Call("c1", "search", `{}`)}}, StopReason: model.StopToolUse, Usage: usage},
				{Message: model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "done"}}}, StopReason: model.StopEndTurn, Usage: usage},
			},
			wantErrs: []error{nil, nil},
		},
		{
			name:     "a failing turn",
			turns:    []Turn{Fail(errOverloaded)},
			want:     []model.Response{{}},
			wantErrs: []error{errOverloaded},
		},
		{
			name: "a function turn",
			turns: []Turn{{Func: func(_ context.Context, req model.Request) (model.Response, error) {
				return model.Response{Model: req.Metadata["model"]}, nil
			}}},
			want:     []model.Response{{Model: "m"}},
			wantErrs: []error{nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := New(t, model.Capabilities{ToolCalling: true}, tt.turns...)
			c.ExpectAllUsed()
			require.True(t, c.Capabilities().ToolCalling)

			req := model.Request{
				Metadata: map[string]string{"model": "m"},
				Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: "hi"}}}},
			}

			for i := range tt.want {
				got, err := c.Generate(t.Context(), req)
				require.Equal(t, tt.wantErrs[i], err)
				require.Equal(t, tt.want[i], got)
			}

			req.Messages[0].Parts[0] = model.Text{Text: "changed"}

			requests := c.Requests()
			require.Len(t, requests, len(tt.want))
			require.Equal(t, model.Text{Text: "hi"}, requests[0].Messages[0].Parts[0], "the recorded request does not change with the caller's")
		})
	}
}

func TestConnectorFailsAfterTheLastTurn(t *testing.T) {
	t.Parallel()

	fake := &recordingTB{TB: t}
	c := New(fake, model.Capabilities{})

	_, err := c.Generate(t.Context(), model.Request{})
	require.ErrorIs(t, err, errNoTurnLeft)
	require.True(t, fake.failed)
}

// recordingTB records Errorf instead of failing the test.
type recordingTB struct {
	testing.TB

	failed bool
}

func (r *recordingTB) Errorf(string, ...any) {
	r.failed = true
}
