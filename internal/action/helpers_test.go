package action

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeParams(t *testing.T) {
	t.Parallel()

	type params struct {
		Name string `json:"name"`
	}

	tests := []struct {
		name    string
		raw     string
		want    params
		wantErr string
	}{
		{name: "known keys", raw: `{"name": "ops"}`, want: params{Name: "ops"}},
		{name: "an unknown key", raw: `{"name": "ops", "other": 1}`, wantErr: `invalid parameters: json: unknown field "other"`},
		{name: "not an object", raw: `[1]`, wantErr: "invalid parameters: json: cannot unmarshal array into Go value of type action.params"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got params

			err := DecodeParams(json.RawMessage(tt.raw), &got)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.ErrorIs(t, err, ErrParams)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestLimitError(t *testing.T) {
	t.Parallel()

	errCause := errors.New("the team is not allowed")

	err := LimitError(errCause, "team %q", "OPS")
	require.EqualError(t, err, `team "OPS": outside configured limits: the team is not allowed`)
	require.ErrorIs(t, err, ErrLimit)
	require.ErrorIs(t, err, errCause)
}

func TestApprovalNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		approvedBy string
		verb       string
		want       string
		wantPolicy bool
	}{
		{name: "a person", approvedBy: "0192d3a4-0000-7000-8000-0000000000d1", verb: "Posted", want: "Posted by Sidekick after an approver approved it."},
		{name: "a Slack user", approvedBy: "U123", verb: "Sent", want: "Sent by Sidekick after an approver approved it."},
		{name: "the policy", approvedBy: "policy", verb: "Posted", want: "Posted by Sidekick, approved by the read + write policy of its route.", wantPolicy: true},
		{name: "the policy, sent", approvedBy: "policy", verb: "Sent", want: "Sent by Sidekick, approved by the read + write policy of its route.", wantPolicy: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv := Invocation{ApprovedBy: tt.approvedBy}
			require.Equal(t, tt.want, inv.ApprovalNote(tt.verb))
			require.Equal(t, tt.wantPolicy, inv.ApprovedByPolicy())
		})
	}
}
