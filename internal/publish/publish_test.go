package publish

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

// testInvestigation is the investigation ID in the unit tests.
var testInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")

func TestUniqueKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, "publish:0192d3a4-0000-7000-8000-0000000000c1:slack-thread", UniqueKey(testInvestigation, "slack-thread"))
}

func TestEnqueueRejectsBadUpdates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		update  Update
		wantErr error
	}{
		{name: "the queued seq", update: Update{InvestigationID: testInvestigation, Phase: output.PhaseRunning}, wantErr: errSeqTooLow},
		{name: "a negative seq", update: Update{InvestigationID: testInvestigation, Phase: output.PhaseRunning, Seq: -1}, wantErr: errSeqTooLow},
		{name: "no phase", update: Update{InvestigationID: testInvestigation, Seq: 1}, wantErr: errNoPhase},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// The checks run before the database is used, so no
			// connection is needed.
			require.ErrorIs(t, Enqueue(t.Context(), nil, tt.update), tt.wantErr)
		})
	}
}

func TestDecodePayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    Payload
		wantErr error
	}{
		{
			name: "a payload",
			raw:  `{"investigation_id": "0192d3a4-0000-7000-8000-0000000000c1", "sink": "slack-thread"}`,
			want: Payload{InvestigationID: testInvestigation, Sink: "slack-thread"},
		},
		{name: "no investigation", raw: `{"sink": "slack-thread"}`, wantErr: errNoInvestigation},
		{name: "no sink", raw: `{"investigation_id": "0192d3a4-0000-7000-8000-0000000000c1"}`, wantErr: errUnknownSink},
		{name: "not JSON", raw: `nope`, wantErr: errAny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodePayload([]byte(tt.raw))

			switch {
			case errors.Is(tt.wantErr, errAny):
				require.Error(t, err)
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

// errAny marks a test case that expects some error.
var errAny = errors.New("any error")
