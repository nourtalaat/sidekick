package gitchange

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDecideCI(t *testing.T) {
	t.Parallel()

	const timeout = 2 * time.Hour

	tests := []struct {
		name       string
		ci         CI
		readFailed bool
		elapsed    time.Duration
		timeout    time.Duration
		want       ciDecision
	}{
		{name: "passed is final", ci: CI{State: CIPassed}, elapsed: time.Minute, timeout: timeout, want: ciDecision{final: true, state: CIPassed}},
		{name: "failed is final", ci: CI{State: CIFailed}, elapsed: time.Minute, timeout: timeout, want: ciDecision{final: true, state: CIFailed}},
		{name: "unknown is final", ci: CI{State: CIUnknown}, elapsed: time.Minute, timeout: timeout, want: ciDecision{final: true, state: CIUnknown}},
		{name: "nothing yet waits for the grace", ci: CI{State: CINone}, elapsed: ciStartGrace - time.Second, timeout: timeout, want: ciDecision{state: CIPending}},
		{
			name: "nothing after the grace is none", ci: CI{State: CINone}, elapsed: ciStartGrace, timeout: timeout,
			want: ciDecision{final: true, state: CINone, waited: ciStartGrace},
		},
		{
			name: "a timeout shorter than the grace ends none at the timeout", ci: CI{State: CINone}, elapsed: 5 * time.Minute, timeout: 5 * time.Minute,
			want: ciDecision{final: true, state: CINone, waited: 5 * time.Minute},
		},
		{name: "running before the timeout goes on", ci: CI{State: CIPending, Total: 3}, elapsed: timeout - time.Second, timeout: timeout, want: ciDecision{state: CIPending}},
		{
			name: "running at the timeout is timed out", ci: CI{State: CIPending, Total: 3, Passed: 1}, elapsed: timeout, timeout: timeout,
			want: ciDecision{final: true, state: CITimedOut, waited: timeout},
		},
		{
			name: "running at the timeout with a failure already is failed", ci: CI{State: CIPending, Total: 3, FailedCount: 1}, elapsed: timeout, timeout: timeout,
			want: ciDecision{final: true, state: CIFailed},
		},
		{name: "a read that failed goes on before the timeout", ci: CI{State: CIPending}, readFailed: true, elapsed: time.Hour, timeout: timeout, want: ciDecision{state: CIPending}},
		{name: "a read that failed until the timeout is unknown", ci: CI{State: CIPending}, readFailed: true, elapsed: timeout, timeout: timeout, want: ciDecision{final: true, state: CIUnknown}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, decideCI(tt.ci, tt.readFailed, tt.elapsed, tt.timeout))
		})
	}
}

func TestWaitAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		checks int32
		want   time.Duration
	}{
		{checks: 0, want: 30 * time.Second},
		{checks: 1, want: time.Minute},
		{checks: 2, want: 2 * time.Minute},
		{checks: 3, want: 4 * time.Minute},
		{checks: 4, want: 5 * time.Minute},
		{checks: 1000, want: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.want.String(), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, waitAfter(tt.checks))
		})
	}
}

func TestCleanName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text is kept", in: "build (linux, amd64)", want: "build (linux, amd64)"},
		{name: "markup is kept as text, for the sink to escape", in: "<img src=x onerror=1> *b*", want: "<img src=x onerror=1> *b*"},
		{name: "control characters go", in: "a\x00b\x1bc", want: "abc"},
		{name: "zero-width and bidirectional characters go", in: "pa\u200bss\u202eed\u2066x\ufeff", want: "passedx"},
		{name: "line breaks and runs of space become one space", in: "  one\r\n\ttwo   three  ", want: "one two three"},
		{name: "line and paragraph separators go", in: "a\u2028b\u2029c", want: "abc"},
		{name: "a long name is cut", in: strings.Repeat("é", 200), want: strings.Repeat("é", maxCheckNameChars-1) + "…"},
		{name: "an empty name stays empty", in: "\u200b", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, CleanName(tt.in))
		})
	}
}

func TestCIConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		timeout     time.Duration
		wantTimeout time.Duration
		wantErr     string
	}{
		{name: "unset is 2 hours", wantTimeout: 2 * time.Hour},
		{name: "a set value is used", timeout: 6 * time.Hour, wantTimeout: 6 * time.Hour},
		{name: "24 hours is the most", timeout: 24 * time.Hour, wantTimeout: 24 * time.Hour},
		{name: "more than 24 hours is refused", timeout: 25 * time.Hour, wantTimeout: 25 * time.Hour, wantErr: "ciReportTimeout: must be from 1m0s to 24h0m0s, got 25h0m0s"},
		{name: "less than a minute is refused", timeout: 10 * time.Second, wantTimeout: 10 * time.Second, wantErr: "ciReportTimeout: must be from 1m0s to 24h0m0s, got 10s"},
		{name: "a negative value is refused", timeout: -time.Hour, wantTimeout: -time.Hour, wantErr: "ciReportTimeout: must be from 1m0s to 24h0m0s, got -1h0m0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := CIConfig{ReportTimeout: tt.timeout}

			if tt.wantErr == "" {
				require.Empty(t, c.Check())
			} else {
				errs := c.Check()
				require.Len(t, errs, 1)
				require.EqualError(t, errs[0], tt.wantErr)
			}

			require.Equal(t, tt.wantTimeout, c.Timeout())
		})
	}
}
