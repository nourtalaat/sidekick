package queue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestEnqueueRejectsBadJobs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		job     NewJob
		wantErr string
	}{
		{name: "no kind", job: NewJob{Payload: []byte(`{}`)}, wantErr: "kind is empty"},
		{name: "no payload", job: NewJob{Kind: KindPublish}, wantErr: "payload is not valid JSON"},
		{name: "payload that is not JSON", job: NewJob{Kind: KindPublish, Payload: []byte(`{`)}, wantErr: "payload is not valid JSON"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The job is rejected before the database is used.
			_, err := Enqueue(t.Context(), nil, tt.job)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestTraceparent(t *testing.T) {
	t.Parallel()

	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		TraceFlags: trace.FlagsSampled,
	})

	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "no span", ctx: context.Background(), want: ""},
		{
			name: "sampled span",
			ctx:  trace.ContextWithSpanContext(context.Background(), spanContext),
			want: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, traceparent(tt.ctx))
		})
	}
}

func TestErrorText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "no error", err: nil, want: ""},
		{name: "short error", err: errors.New("model timed out"), want: "model timed out"},
		{name: "NUL bytes are dropped", err: errors.New("bad\x00byte"), want: "badbyte"},
		{name: "invalid UTF-8 is dropped", err: errors.New("bad\xffbyte"), want: "badbyte"},
		{name: "long error is cut", err: errors.New(strings.Repeat("x", maxErrorBytes+10)), want: strings.Repeat("x", maxErrorBytes)},
		{
			name: "a character split by the cut is dropped",
			err:  errors.New(strings.Repeat("x", maxErrorBytes-1) + "é"),
			want: strings.Repeat("x", maxErrorBytes-1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, errorText(tt.err))
		})
	}
}
